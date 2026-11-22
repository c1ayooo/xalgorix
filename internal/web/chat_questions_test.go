package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// newSideQuestionFixture mounts a task + run on instance "inst-q" flagged
// running, mirroring the approvals fixture shape.
func newSideQuestionFixture(t *testing.T) (*Server, platform.Repository, string) {
	t.Helper()
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "side questions"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-q", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	s.instancesMu.Lock()
	s.instances["inst-q"] = &ScanInstance{
		ID:      "inst-q",
		Status:  "running",
		Targets: "https://example.com",
	}
	s.instancesMu.Unlock()
	return s, store, task.ID
}

// waitForInstanceEvent polls the instance buffer until the predicate matches,
// bounding each wait so a lost goroutine fails the test instead of hanging it.
func waitForInstanceEvent(t *testing.T, s *Server, instanceID string, match func(WSEvent) bool) WSEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.instancesMu.RLock()
		inst := s.instances[instanceID]
		s.instancesMu.RUnlock()
		if inst != nil {
			inst.mu.RLock()
			for _, evt := range inst.events {
				if match(evt) {
					inst.mu.RUnlock()
					return evt
				}
			}
			inst.mu.RUnlock()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected event for %s did not land within the deadline", instanceID)
	return WSEvent{}
}

// TestSideQuestionRoundTrip pins the happy path: the question is accepted
// (202) with the chat_question row + WS event landing immediately, the
// responder receives the snapshot-bearing system prompt and the raw question,
// the busy slot rejects a concurrent second question with 409, and the answer
// lands as a chat_reply event once the (stubbed) LLM call returns.
func TestSideQuestionRoundTrip(t *testing.T) {
	s, _, taskID := newSideQuestionFixture(t)

	block := make(chan struct{})
	var gotSystem, gotQuestion string
	s.scanQuestionFn = func(ctx context.Context, systemPrompt, userMessage string) (string, error) {
		gotSystem, gotQuestion = systemPrompt, userMessage
		<-block
		return "我是 Xalgorix，一个自主渗透测试引擎。", nil
	}

	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/questions", strings.NewReader(body))
		s.handleTaskDetail(rr, req)
		return rr
	}

	rr := post(`{"message":"你是谁","request_id":"rq-1","instance_id":"inst-q"}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "pending" {
		t.Fatalf("status = %s, want pending", out.Status)
	}

	question := waitForInstanceEvent(t, s, "inst-q", func(e WSEvent) bool { return e.Type == "chat_question" })
	if question.Content != "你是谁" {
		t.Fatalf("chat_question content = %q", question.Content)
	}

	// While the stubbed responder is still blocked, a second question for the
	// same run must be refused — no queueing.
	if rr := post(`{"message":"第二条","request_id":"rq-2","instance_id":"inst-q"}`); rr.Code != http.StatusConflict {
		t.Fatalf("second question code = %d, want 409", rr.Code)
	}

	close(block)
	reply := waitForInstanceEvent(t, s, "inst-q", func(e WSEvent) bool { return e.Type == "chat_reply" })
	if reply.Content != "我是 Xalgorix，一个自主渗透测试引擎。" {
		t.Fatalf("chat_reply content = %q", reply.Content)
	}
	if reply.ResultMeta["status"] != "answered" {
		t.Fatalf("reply meta status = %v, want answered", reply.ResultMeta["status"])
	}
	if !strings.Contains(gotSystem, "https://example.com") {
		t.Fatalf("responder system prompt missing the scan snapshot: %q", gotSystem)
	}
	if gotQuestion != "你是谁" {
		t.Fatalf("responder question = %q", gotQuestion)
	}

	// The busy slot must be released once the answer landed. block is already
	// closed, so the second stubbed responder returns immediately.
	if rr := post(`{"message":"第三条","request_id":"rq-3","instance_id":"inst-q"}`); rr.Code != http.StatusAccepted {
		t.Fatalf("post-answer question code = %d, want 202 (slot released)", rr.Code)
	}
	waitForInstanceEvent(t, s, "inst-q", func(e WSEvent) bool {
		return e.Type == "chat_reply" && e.ResultMeta["request_id"] == "rq-3"
	})
}

// TestSideQuestionFailureKeepsRow pins the honest-failure path: a responder
// error lands as a failed chat_reply row (never silence) and releases the
// busy slot.
func TestSideQuestionFailureKeepsRow(t *testing.T) {
	s, _, taskID := newSideQuestionFixture(t)

	block := make(chan struct{})
	s.scanQuestionFn = func(ctx context.Context, systemPrompt, userMessage string) (string, error) {
		<-block
		return "", errors.New("provider unavailable")
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/questions",
		strings.NewReader(`{"message":"现在打到哪了","request_id":"rq-f1","instance_id":"inst-q"}`))
	s.handleTaskDetail(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", rr.Code)
	}

	close(block)
	reply := waitForInstanceEvent(t, s, "inst-q", func(e WSEvent) bool { return e.Type == "chat_reply" })
	if reply.ResultMeta["status"] != "failed" {
		t.Fatalf("reply meta status = %v, want failed", reply.ResultMeta["status"])
	}
	if !strings.Contains(reply.Error, "provider unavailable") {
		t.Fatalf("reply error = %q, want the provider error", reply.Error)
	}

	// Slot released: the retry is accepted.
	s.scanQuestionFn = func(ctx context.Context, systemPrompt, userMessage string) (string, error) {
		return "ok", nil
	}
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/questions",
		strings.NewReader(`{"message":"重试","request_id":"rq-f2","instance_id":"inst-q"}`))
	s.handleTaskDetail(rr2, req2)
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("retry code = %d, want 202", rr2.Code)
	}
}

// TestSideQuestionValidation pins the contract guards: GET is not allowed,
// an empty body is a 400, and an instance mounted on another task is a 400
// (never a silent foreign delivery — same L5 rule as guidance).
func TestSideQuestionValidation(t *testing.T) {
	s, store, taskID := newSideQuestionFixture(t)
	ctx := context.Background()

	otherTask, err := store.CreateTask(ctx, "proj-other", platform.TaskInput{Name: "foreign"})
	if err != nil {
		// proj-other may not exist in the file store; create through the
		// default project instead and mount the foreign instance there.
		project, perr := store.EnsureDefaultProject(ctx)
		if perr != nil {
			t.Fatal(perr)
		}
		otherTask, err = store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "foreign"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateRun(ctx, otherTask.ID, platform.RunMount{InstanceID: "inst-foreign", Mode: "single"}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	get := httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskID+"/questions", nil)
	s.handleTaskDetail(rr, get)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET code = %d, want 405", rr.Code)
	}

	rr = httptest.NewRecorder()
	empty := httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/questions", strings.NewReader(`{"message":"  "}`))
	s.handleTaskDetail(rr, empty)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty message code = %d, want 400", rr.Code)
	}

	rr = httptest.NewRecorder()
	foreign := httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/questions",
		strings.NewReader(`{"message":"hi","request_id":"rq-x","instance_id":"inst-foreign"}`))
	s.handleTaskDetail(rr, foreign)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("foreign instance code = %d, want 400", rr.Code)
	}
}
