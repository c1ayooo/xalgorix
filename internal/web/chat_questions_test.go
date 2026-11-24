package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
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

// waitForJournalEvent polls the run's persisted conversation journal — the
// channel a run without a live instance actually uses (broadcastToInstance
// only buffers when the instance is registered).
func waitForJournalEvent(t *testing.T, dir string, match func(WSEvent) bool) WSEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		events, _, ok := loadScanEventsWindow(dir, 0, 100)
		if ok {
			for _, evt := range events {
				if match(evt) {
					return evt
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected journal event in %s did not land within the deadline", dir)
	return WSEvent{}
}

// TestSnapshotChatInstance_PersistedMultiRecordRun pins the finished/
// re-dispatched run path: such a run has no live *ScanInstance (the D11 rebuild
// refuses ambiguous external instance ids), so the side-question snapshot must
// come from the durable scan records — newest session for status/timing, the
// merged findings corpus across siblings, and every distinct target. The old
// implementation returned nil here and the handler nil-dereferenced (500).
func TestSnapshotChatInstance_PersistedMultiRecordRun(t *testing.T) {
	s := newTestServer(t, nil)
	id := "inst-multi-record"

	writeRecord := func(scanID, target, started, finished, title string) {
		t.Helper()
		dir := filepath.Join(s.dataDir, scanID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		s.saveScanRecordTo(&ScanRecord{
			ID:         scanID,
			InstanceID: id,
			Target:     target,
			Status:     "finished",
			StartedAt:  started,
			FinishedAt: finished,
			Vulns: []VulnSummary{{
				Severity: "high",
				Title:    title,
				Endpoint: target + "/login",
			}},
		}, dir)
	}
	writeRecord("multi-old", "https://old.example", "2026-01-01T00:00:00Z", "2026-01-01T01:00:00Z", "old finding")
	writeRecord("multi-new", "https://new.example", "2026-01-02T00:00:00Z", "2026-01-02T01:00:00Z", "new finding")

	snap := s.snapshotChatInstance(id)
	if snap == nil {
		t.Fatal("persisted snapshot must exist for a run whose records are on disk")
	}
	if snap.ID != id || snap.Status != "finished" {
		t.Fatalf("snapshot identity/status = %q/%q", snap.ID, snap.Status)
	}
	if snap.StartedAt != "2026-01-01T00:00:00Z" || snap.FinishedAt != "2026-01-02T01:00:00Z" {
		t.Fatalf("run timing must span all sessions: start=%q finish=%q", snap.StartedAt, snap.FinishedAt)
	}
	if snap.VulnCount != 2 || len(snap.Vulns) != 2 {
		t.Fatalf("findings must merge across sibling records: count=%d rows=%d", snap.VulnCount, len(snap.Vulns))
	}
	if !strings.Contains(snap.Targets, "https://old.example") || !strings.Contains(snap.Targets, "https://new.example") {
		t.Fatalf("targets must list every sibling target: %q", snap.Targets)
	}
}

// TestSideQuestionFinishedRunUsesPersistedSnapshot pins the user-facing
// regression: a run that is no longer live (finished, or rebuilt after a
// restart / D11 re-dispatch) must still answer side questions from its durable
// scan record — the responder receives the persisted snapshot, and the answer
// lands as a chat_reply row instead of a 500.
func TestSideQuestionFinishedRunUsesPersistedSnapshot(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "finished chat"})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := "inst-finished-chat"
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: instanceID, Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.dataDir, "scan-finished-chat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s.saveScanRecordTo(&ScanRecord{
		ID:            "scan-finished-chat",
		InstanceID:    instanceID,
		Target:        "https://finished.example",
		Status:        "finished",
		StartedAt:     "2026-01-01T00:00:00Z",
		FinishedAt:    "2026-01-01T01:00:00Z",
		EventsJournal: true,
		Vulns:         []VulnSummary{{Severity: "critical", Title: "auth bypass", Endpoint: "https://finished.example/admin"}},
	}, dir)

	var gotSystem string
	s.scanQuestionFn = func(ctx context.Context, systemPrompt, userMessage string) (string, error) {
		gotSystem = systemPrompt
		return "扫描已完成，存在一个认证绕过。", nil
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/questions",
		strings.NewReader(`{"message":"有什么发现？","request_id":"rq-finished","instance_id":"`+instanceID+`"}`))
	s.handleTaskDetail(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	reply := waitForJournalEvent(t, dir, func(e WSEvent) bool { return e.Type == "chat_reply" })
	if reply.ResultMeta["status"] != "answered" || reply.Content == "" {
		t.Fatalf("persisted-run answer missing: %#v", reply)
	}
	if !strings.Contains(gotSystem, "finished.example") || !strings.Contains(gotSystem, "auth bypass") {
		t.Fatalf("responder prompt lost the persisted snapshot: %q", gotSystem)
	}
}

// TestSideQuestionNoContextIs409 pins the honest failure: a mounted run with
// neither a live instance nor durable scan records must be refused with a 409
// and must not queue a chat_question row the responder can never answer.
func TestSideQuestionNoContextIs409(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "no context"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-noscan", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	s.scanQuestionFn = func(ctx context.Context, systemPrompt, userMessage string) (string, error) {
		t.Fatal("the responder must not run without a snapshot")
		return "", nil
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/questions",
		strings.NewReader(`{"message":"hi","request_id":"rq-nil","instance_id":"inst-noscan"}`))
	s.handleTaskDetail(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("code = %d body=%s, want 409", rr.Code, rr.Body.String())
	}
}

// TestQuestionPromptWithoutSnapshotDoesNotPanic pins the nil guard: even if a
// future caller loses the snapshot, prompt assembly states the gap instead of
// dereferencing nil in the request goroutine.
func TestQuestionPromptWithoutSnapshotDoesNotPanic(t *testing.T) {
	prompt := buildQuestionAnswerPrompt(nil, "en")
	if !strings.Contains(prompt, "Snapshot unavailable") {
		t.Fatalf("missing-snapshot prompt must state the gap: %q", prompt)
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
