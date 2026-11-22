package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

// newApprovalsTestServer builds a Server with a file-backed platform store
// and (optionally) an enforce-mode approval service. cfg is applied before
// NewServer so the service reads the intended policy values.
func newApprovalsTestServer(t *testing.T, cfg *config.Config) (*Server, platform.Repository) {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	s := newTestServer(t, cfg)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	return s, store
}

// seedApproval inserts one approval row directly through the store.
func seedApproval(t *testing.T, store platform.Repository, projectID, taskID, instanceID, kind, deadlineIn string) *platform.Approval {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	if deadlineIn != "" {
		var err error
		deadline, err = time.Parse(time.RFC3339, deadlineIn)
		if err != nil {
			t.Fatal(err)
		}
	}
	payload := platform.ApprovalPayload{Reason: "shell command execution requires approval"}
	if kind == platform.ApprovalKindHTTP {
		payload = platform.ApprovalPayload{Method: "POST", URL: "http://example.com/admin", Reason: "admin panel path"}
	}
	row, err := store.CreateApproval(context.Background(), platform.ApprovalInput{
		ProjectID:  projectID,
		TaskID:     taskID,
		InstanceID: instanceID,
		Kind:       kind,
		Payload:    payload,
		DeadlineAt: deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// ── wiring ───────────────────────────────────────────────────────────────────

// TestApprovalRoutesRegistered extends the lockstep invariant to the R2a
// surface; guidance rides the existing "/api/tasks/" prefix (contract §3.1).
func TestApprovalRoutesRegistered(t *testing.T) {
	want := []string{"/api/approvals", "/api/approvals/summary", "/api/approvals/", "/api/tasks/"}
	counts := make(map[string]int, len(dashboardRoutes))
	for _, route := range dashboardRoutes {
		counts[route]++
	}
	for _, pattern := range want {
		if counts[pattern] != 1 {
			t.Errorf("dashboardRoutes contains %q %d time(s), want exactly 1", pattern, counts[pattern])
		}
	}
}

// TestApprovalHandlersNilStore503 preserves the platform 503 posture: no
// store → every approval/guidance endpoint surfaces 503 instead of panicking.
func TestApprovalHandlersNilStore503(t *testing.T) {
	s := &Server{} // platformStore == nil, approvalSvc == nil

	cases := []struct {
		name   string
		method string
		path   string
		call   func(w http.ResponseWriter, r *http.Request)
	}{
		{"GET approvals", http.MethodGet, "/api/approvals", s.handleApprovals},
		{"GET approvals summary", http.MethodGet, "/api/approvals/summary", s.handleApprovals},
		{"POST decision", http.MethodPost, "/api/approvals/x/decision", s.handleApprovalDetail},
		{"POST batch", http.MethodPost, "/api/approvals/batch-decision", s.handleApprovalDetail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			tc.call(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("code = %d, want 503; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
	// Guidance rides handleTaskDetail's store guard.
	t.Run("GET guidance", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/tasks/x/guidance", nil)
		s.handleTaskDetail(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("code = %d, want 503", rr.Code)
		}
	})
	t.Run("POST guidance", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/x/guidance", strings.NewReader(`{}`))
		s.handleTaskDetail(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("code = %d, want 503", rr.Code)
		}
	})
}

// TestApprovalMethodGuards checks the 405 surface of the R2a handlers.
func TestApprovalMethodGuards(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	t.Run("PUT /api/approvals → 405", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/approvals", nil)
		s.handleApprovals(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code = %d, want 405", rr.Code)
		}
	})
	t.Run("GET decision → 405", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/approvals/x/decision", nil)
		s.handleApprovalDetail(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code = %d, want 405", rr.Code)
		}
	})
	t.Run("PUT guidance → 405", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/tasks/x/guidance", nil)
		s.handleTaskDetail(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code = %d, want 405", rr.Code)
		}
	})
	_ = store
}

// TestApprovalsSummaryRoute exercises the real mux dispatch (the same helper
// Start mounts): "/api/approvals/summary" must not fall into the
// "/api/approvals/" subtree, where GET is rejected with 405.
func TestApprovalsSummaryRoute(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	mux := http.NewServeMux()
	registerPlatformAPIRoutes(mux, s)

	t.Run("GET summary → 200", func(t *testing.T) {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/approvals/summary", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		var body struct {
			PendingTotal int `json:"pending_total"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
		}
		if body.PendingTotal != 0 {
			t.Fatalf("pending_total = %d, want 0", body.PendingTotal)
		}
	})

	t.Run("POST summary → 405", func(t *testing.T) {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/approvals/summary", strings.NewReader(`{}`)))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code = %d, want 405; body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("POST decision still dispatches to detail", func(t *testing.T) {
		proj, err := store.CreateProject(context.Background(), "Summary Route Project", platform.Scope{})
		if err != nil {
			t.Fatal(err)
		}
		row := seedApproval(t, store, proj.ID, "", "", platform.ApprovalKindCommand, "")
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/approvals/"+row.ID+"/decision", strings.NewReader(`{"decision":"approved"}`))
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("decision code = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
	})
}

// ── approvals list / decision contract (mock-backend.mjs:887-936 parity) ────

// TestApprovalsListShape pins the response contract: {approvals,
// pending_total} with pending_total ALWAYS cross-project (D7 exception) and
// the project_name/task_name projections filled by the web layer.
func TestApprovalsListShape(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()

	projA, err := store.CreateProject(ctx, "Approval Shape A", platform.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	projB, err := store.CreateProject(ctx, "Approval Shape B", platform.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	taskA, err := store.CreateTask(ctx, projA.ID, platform.TaskInput{Name: "shape task A"})
	if err != nil {
		t.Fatal(err)
	}
	seedApproval(t, store, projA.ID, taskA.ID, "inst-a", platform.ApprovalKindCommand, "")
	seedApproval(t, store, projB.ID, "", "inst-b", platform.ApprovalKindHTTP, "")
	seedApproval(t, store, projB.ID, "", "inst-b", platform.ApprovalKindHTTP, "")
	decided := seedApproval(t, store, projA.ID, "", "inst-a", platform.ApprovalKindCommand, "")
	if _, err := store.DecideApproval(ctx, decided.ID, platform.ApprovalDecision{Decision: platform.ApprovalRejected}); err != nil {
		t.Fatal(err)
	}

	t.Run("pending default counts both projects", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/approvals", nil)
		s.handleApprovals(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
		}
		var body struct {
			Approvals    []platform.Approval `json:"approvals"`
			PendingTotal int                 `json:"pending_total"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Approvals) != 3 {
			t.Errorf("pending list = %d rows, want 3", len(body.Approvals))
		}
		if body.PendingTotal != 3 {
			t.Errorf("pending_total = %d, want 3 (cross-project, D7 exception)", body.PendingTotal)
		}
		for _, a := range body.Approvals {
			if a.Status != platform.ApprovalPending {
				t.Errorf("pending filter leaked a %s row", a.Status)
			}
		}
	})

	t.Run("project filter and projections", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/approvals?status=all&project_id="+projA.ID, nil)
		s.handleApprovals(rr, req)
		var body struct {
			Approvals    []platform.Approval `json:"approvals"`
			PendingTotal int                 `json:"pending_total"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Approvals) != 2 {
			t.Errorf("project filter = %d rows, want 2", len(body.Approvals))
		}
		for _, a := range body.Approvals {
			if a.ProjectName != "Approval Shape A" {
				t.Errorf("project_name projection missing: %+v", a)
			}
			if a.TaskID == taskA.ID && a.TaskName != "shape task A" {
				t.Errorf("task_name projection missing: %+v", a)
			}
		}
		// pending_total stays cross-project even under a project filter.
		if body.PendingTotal != 3 {
			t.Errorf("pending_total under filter = %d, want 3", body.PendingTotal)
		}
	})

	t.Run("decided excludes pending", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/approvals?status=decided", nil)
		s.handleApprovals(rr, req)
		var body struct {
			Approvals []platform.Approval `json:"approvals"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Approvals) != 1 || body.Approvals[0].Status == platform.ApprovalPending {
			t.Errorf("decided filter = %+v, want only the rejected row", body.Approvals)
		}
	})

	t.Run("bad status filter 400", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/approvals?status=wat", nil)
		s.handleApprovals(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400", rr.Code)
		}
	})
}

// TestApprovalsDecisionContract pins POST decision: 200 with the updated row,
// 409 on a second decision, 400 on a bad decision value, 404 unknown id.
func TestApprovalsDecisionContract(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	s.cfg.Username = "admin"
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	row := seedApproval(t, store, project.ID, "", "inst-dec", platform.ApprovalKindCommand, "")

	post := func(id, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/approvals/"+id+"/decision", strings.NewReader(body))
		s.handleApprovalDetail(rr, req)
		return rr
	}

	rr := post(row.ID, `{"decision":"approved","reason":"looks legit"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("first decision code = %d body=%s", rr.Code, rr.Body.String())
	}
	var decided platform.Approval
	if err := json.Unmarshal(rr.Body.Bytes(), &decided); err != nil {
		t.Fatal(err)
	}
	if decided.Status != platform.ApprovalApproved || decided.DecidedBy != "operator:admin" {
		t.Errorf("decision lost: %+v", decided)
	}
	if decided.Reason != "looks legit" {
		t.Errorf("reason lost: %+v", decided)
	}

	if rr := post(row.ID, `{"decision":"rejected"}`); rr.Code != http.StatusConflict {
		t.Errorf("second decision = %d, want 409 (mock-backend:908-918)", rr.Code)
	}
	if rr := post("no-such-id", `{"decision":"approved"}`); rr.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", rr.Code)
	}
	if rr := post(row.ID, `{"decision":"timeout"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("operator timeout decision = %d, want 400 (operators choose approved/rejected only)", rr.Code)
	}
}

// TestApprovalsBatchDecision pins the skip semantics (mock-backend:919-936):
// missing/decided ids count as skipped, the batch never fails.
func TestApprovalsBatchDecision(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p1 := seedApproval(t, store, project.ID, "", "inst-batch", platform.ApprovalKindCommand, "")
	p2 := seedApproval(t, store, project.ID, "", "inst-batch", platform.ApprovalKindCommand, "")
	already := seedApproval(t, store, project.ID, "", "inst-batch", platform.ApprovalKindCommand, "")
	if _, err := store.DecideApproval(ctx, already.ID, platform.ApprovalDecision{Decision: platform.ApprovalRejected}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	body := `{"ids":["` + p1.ID + `","` + p2.ID + `","` + already.ID + `","ghost"],"decision":"approved","reason":"ok"}`
	req := httptest.NewRequest(http.MethodPost, "/api/approvals/batch-decision", strings.NewReader(body))
	s.handleApprovalDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	var counts struct {
		Approved int `json:"approved"`
		Rejected int `json:"rejected"`
		Skipped  int `json:"skipped"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &counts); err != nil {
		t.Fatal(err)
	}
	if counts.Approved != 2 || counts.Skipped != 2 {
		t.Errorf("counts = %+v, want approved=2 skipped=2 (decided + ghost)", counts)
	}
}

// TestApprovalsSummary pins the OpsPulse endpoint: pending_total cross-project
// plus the nearest pending deadline, omitted when nothing pends.
func TestApprovalsSummary(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("empty", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/approvals/summary", nil)
		s.handleApprovals(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code = %d", rr.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["pending_total"].(float64) != 0 {
			t.Errorf("pending_total = %v, want 0", body["pending_total"])
		}
		if _, present := body["nearest_deadline"]; present {
			t.Error("nearest_deadline must be omitted when nothing pends")
		}
	})

	early := time.Now().Add(30 * time.Second).Format(time.RFC3339)
	late := time.Now().Add(2 * time.Minute).Format(time.RFC3339)
	seedApproval(t, store, project.ID, "", "inst-sum-1", platform.ApprovalKindCommand, late)
	seedApproval(t, store, project.ID, "", "inst-sum-2", platform.ApprovalKindCommand, early)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/approvals/summary", nil)
	s.handleApprovals(rr, req)
	var body struct {
		PendingTotal    int    `json:"pending_total"`
		NearestDeadline string `json:"nearest_deadline"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.PendingTotal != 2 {
		t.Errorf("pending_total = %d, want 2", body.PendingTotal)
	}
	nearest, err := time.Parse(time.RFC3339, body.NearestDeadline)
	if err != nil {
		t.Fatalf("nearest_deadline not RFC3339: %v (%q)", err, body.NearestDeadline)
	}
	wantEarly, err := time.Parse(time.RFC3339, early)
	if err != nil {
		t.Fatal(err)
	}
	if !nearest.Equal(wantEarly) {
		t.Errorf("nearest_deadline = %s, want %s (compare instants — the wire is UTC, the seed is local)", body.NearestDeadline, early)
	}
}

// ── guidance contract (mock-backend:941-980 parity) ─────────────────────────

func TestGuidanceValidation(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "guidance task"})
	if err != nil {
		t.Fatal(err)
	}

	post := func(taskID, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/guidance", strings.NewReader(body))
		s.handleTaskDetail(rr, req)
		return rr
	}

	if rr := post(task.ID, `{"message":"","request_id":"r1"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("empty message = %d, want 400", rr.Code)
	}
	huge := strings.Repeat("字", 4001) // 4001 code points, 12003 bytes
	if rr := post(task.ID, `{"message":"`+huge+`","request_id":"r1"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("4001 code points = %d, want 400 (code-point ceiling, not bytes)", rr.Code)
	}
	if rr := post(task.ID, `{"message":"ok"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("missing request_id = %d, want 400", rr.Code)
	}
	if rr := post("no-such-task", `{"message":"ok","request_id":"r1"}`); rr.Code != http.StatusNotFound {
		t.Errorf("unknown task = %d, want 404", rr.Code)
	}
}

// TestGuidanceQueuedAndIdempotent pins: no active run → queued + persisted;
// a replayed request_id returns the FIRST outcome with accepted:false and
// does not duplicate (contract §3.1, mock-backend:945-958).
func TestGuidanceQueuedAndIdempotent(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "queued guidance"})
	if err != nil {
		t.Fatal(err)
	}
	// The named instance must be mounted on THIS task (L5 ownership check) —
	// the instance is not running, so the turn queues for the next session.
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-q", Mode: "single"}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	body := `{"message":"watch the staging host","request_id":"req-q1","instance_id":"inst-q"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/guidance", strings.NewReader(body))
	req = req.WithContext(withSessionClaims(req.Context(), &sessionClaims{Username: "alice", Role: RoleOperator}))
	s.handleTaskDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	var first struct {
		Status     string `json:"status"`
		GuidanceID string `json:"guidance_id"`
		RunID      string `json:"run_id"`
		Accepted   bool   `json:"accepted"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Status != "queued" || first.Accepted != true || first.GuidanceID == "" {
		t.Fatalf("first outcome = %+v, want queued/accepted", first)
	}

	// Replay: same request_id → first result, accepted:false.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/guidance", strings.NewReader(body))
	s.handleTaskDetail(rr, req)
	var replay struct {
		Status     string `json:"status"`
		GuidanceID string `json:"guidance_id"`
		Accepted   bool   `json:"accepted"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Accepted != false || replay.GuidanceID != first.GuidanceID || replay.Status != "queued" {
		t.Fatalf("replay = %+v, want accepted:false with the first ids", replay)
	}

	// GET list: newest first, persisted row carries the message.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/guidance", nil)
	s.handleTaskDetail(rr, req)
	var list struct {
		Items []platform.Guidance `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Message != "watch the staging host" {
		t.Fatalf("list = %+v, want the single queued row", list.Items)
	}
	if list.Items[0].CreatedBy != "alice" {
		t.Fatalf("created_by = %q, want verified session user alice", list.Items[0].CreatedBy)
	}
}

// TestGuidanceInjectedToRunningAgent pins the in-scan injection: a running
// instance with a live agent receives the guidance as an operator message
// (Agent.SendMessage), the row flips to injected, and the WS event lands in
// the instance buffer.
func TestGuidanceInjectedToRunningAgent(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "injected guidance"})
	if err != nil {
		t.Fatal(err)
	}
	// Mount a run on the instance so run_id resolves.
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-live", Mode: "single"}); err != nil {
		t.Fatal(err)
	}

	events := make(chan agent.Event, 16)
	sctx := scanctx.New("guidance-live", s.dataDir)
	agnt := agent.NewAgent(&config.Config{}, "XalgorixAgent", events, scopeguard.Config{}, sctx)
	s.instancesMu.Lock()
	s.instances["inst-live"] = &ScanInstance{ID: "inst-live", Status: "running", agent: agnt}
	s.instancesMu.Unlock()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/guidance",
		strings.NewReader(`{"message":"check /admin first","request_id":"req-live","instance_id":"inst-live"}`))
	s.handleTaskDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Status     string `json:"status"`
		GuidanceID string `json:"guidance_id"`
		RunID      string `json:"run_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "injected" {
		t.Fatalf("status = %s, want injected (SendMessage path)", out.Status)
	}
	if out.RunID == "" {
		t.Error("run_id must resolve through the mounted run")
	}

	// The agent buffered the operator intent for its next iteration.
	select {
	case evt := <-events:
		if evt.Type != "message" || !strings.Contains(evt.Content, "check /admin first") {
			t.Errorf("agent event = %+v, want the guidance message", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("no agent event emitted")
	}

	// Row flipped to injected.
	items, err := store.ListGuidance(ctx, task.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != platform.GuidanceInjected {
		t.Fatalf("guidance rows = %+v, want one injected row", items)
	}

	// WS guidance_injected landed in the instance buffer.
	s.instancesMu.RLock()
	inst := s.instances["inst-live"]
	s.instancesMu.RUnlock()
	inst.mu.RLock()
	found := false
	for _, evt := range inst.events {
		if evt.Type == "guidance_injected" && evt.Guidance != nil && evt.Guidance.ID == out.GuidanceID {
			found = true
		}
	}
	inst.mu.RUnlock()
	if !found {
		t.Error("guidance_injected WS event missing from the instance buffer")
	}
}

// TestGuidanceFallsBackWhenAgentStopped pins the resilient path: a running-
// flagged instance whose agent is gone (or SendMessage fails) queues the
// guidance instead of failing the POST.
func TestGuidanceFallsBackWhenAgentStopped(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "fallback guidance"})
	if err != nil {
		t.Fatal(err)
	}
	// The named instance is mounted on the task (L5 ownership gate) but
	// flagged running with NO live agent — SendMessage cannot succeed.
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-ghost", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	s.instancesMu.Lock()
	s.instances["inst-ghost"] = &ScanInstance{ID: "inst-ghost", Status: "running"}
	s.instancesMu.Unlock()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/guidance",
		strings.NewReader(`{"message":"hold the report","request_id":"req-fb","instance_id":"inst-ghost"}`))
	s.handleTaskDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d", rr.Code)
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "queued" {
		t.Fatalf("status = %s, want queued fallback", out.Status)
	}
	items, _ := store.ListGuidance(ctx, task.ID, 10)
	if len(items) != 1 || items[0].Status != platform.GuidanceQueued {
		t.Fatalf("rows = %+v, want one queued row", items)
	}
}

// TestGuidanceForeignInstance400 pins the L5 ownership gate: an instance_id
// mounted on a DIFFERENT task (or not mounted anywhere) must be a 400 —
// never a silent delivery to a foreign run.
func TestGuidanceForeignInstance400(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "owner task"})
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "other task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, otherTask.ID, platform.RunMount{InstanceID: "inst-foreign", Mode: "single"}); err != nil {
		t.Fatal(err)
	}

	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/guidance", strings.NewReader(body))
		s.handleTaskDetail(rr, req)
		return rr
	}
	// Mounted on another task → 400.
	if rr := post(`{"message":"hi","request_id":"r-f1","instance_id":"inst-foreign"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("foreign mounted instance = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	// Not mounted anywhere → 400.
	if rr := post(`{"message":"hi","request_id":"r-f2","instance_id":"inst-nowhere"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("unmounted instance = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	// Nothing was persisted for either attempt.
	items, _ := store.ListGuidance(ctx, task.ID, 10)
	if len(items) != 0 {
		t.Errorf("rejected guidance must not persist: %d rows", len(items))
	}
}

// ── run-stop ⇒ pending approvals cancelled (REDESIGN §5.3) ──────────────────

// TestStopCancelsPendingApprovals wires the per-instance stop flip point:
// every pending approval of the run flips to cancelled with decided_by=
// "system" and a waiting tool call would wake (verdict delivery asserted via
// the batch WS events in the instance buffer).
func TestStopCancelsPendingApprovals(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r1 := seedApproval(t, store, project.ID, "", "inst-stop", platform.ApprovalKindCommand, "")
	r2 := seedApproval(t, store, project.ID, "", "inst-stop", platform.ApprovalKindHTTP, "")

	s.instancesMu.Lock()
	s.instances["inst-stop"] = &ScanInstance{ID: "inst-stop", Status: "running"}
	s.instancesMu.Unlock()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/instances/inst-stop/stop", nil)
	s.handleInstanceAction(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("stop code = %d body=%s", rr.Code, rr.Body.String())
	}

	for _, id := range []string{r1.ID, r2.ID} {
		row, err := store.GetApproval(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != platform.ApprovalCancelled || row.DecidedBy != "system" || row.DecidedAt == nil {
			t.Errorf("row %s not cancelled on run stop: %+v", id, row)
		}
		if !strings.Contains(row.Reason, "stopped") {
			t.Errorf("cancel reason lost: %q", row.Reason)
		}
	}
	assertApprovalDecidedEvents(t, s, "inst-stop", 2)
}

// TestPauseCancelsPendingApprovals pins the R2a decision: pausing a run
// cancels its pending approvals (no approval lingers while the run is idle;
// resume issues fresh approvals for new calls).
func TestPauseCancelsPendingApprovals(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	row := seedApproval(t, store, project.ID, "", "inst-pause", platform.ApprovalKindCommand, "")

	s.instancesMu.Lock()
	s.instances["inst-pause"] = &ScanInstance{ID: "inst-pause", Status: "running"}
	s.instancesMu.Unlock()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/instances/inst-pause/pause", nil)
	s.handleInstanceAction(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("pause code = %d body=%s", rr.Code, rr.Body.String())
	}

	got, err := store.GetApproval(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != platform.ApprovalCancelled {
		t.Errorf("pause did not cancel the pending approval: %+v", got)
	}
	if !strings.Contains(got.Reason, "paused") {
		t.Errorf("pause reason lost: %q", got.Reason)
	}
}

// TestCancelPendingApprovalsForInstanceNilService keeps the hook inert when
// the pipeline is absent (mode off / zero-value server in other tests).
func TestCancelPendingApprovalsForInstanceNilService(t *testing.T) {
	s := &Server{}
	s.cancelPendingApprovalsForInstance("whatever", "run stopped") // must not panic
}

// ── queued guidance merges into the session briefing ─────────────────────────

// TestMergeQueuedGuidance pins the scan_session merge: queued turns join the
// instruction as a chronological [OPERATOR GUIDANCE] section, each row flips
// to injected, and the WS event reaches the instance buffer. Already-injected
// rows are not repeated.
func TestMergeQueuedGuidance(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "briefing task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-brief", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	g1, _, err := store.CreateGuidance(ctx, platform.GuidanceInput{TaskID: task.ID, Message: "first: check robots.txt", RequestID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // distinct created_at for ordering
	g2, _, err := store.CreateGuidance(ctx, platform.GuidanceInput{TaskID: task.ID, Message: "second: avoid the paid API", RequestID: "b2"})
	if err != nil {
		t.Fatal(err)
	}
	// A pre-injected turn must not repeat in the briefing.
	if _, _, err := store.CreateGuidance(ctx, platform.GuidanceInput{TaskID: task.ID, Message: "already delivered live", RequestID: "b3"}); err != nil {
		t.Fatal(err)
	}
	items, _ := store.ListGuidance(ctx, task.ID, 10)
	if _, err := store.UpdateGuidanceStatus(ctx, items[0].ID, platform.GuidanceInjected); err != nil {
		t.Fatal(err)
	}

	s.instancesMu.Lock()
	s.instances["inst-brief"] = &ScanInstance{ID: "inst-brief", Status: "running"}
	s.instancesMu.Unlock()

	sess := &scanSession{instanceID: "inst-brief"}
	merged := s.mergeQueuedGuidance(sess, "base instruction")

	if !strings.HasPrefix(merged, "base instruction") {
		t.Errorf("merged instruction lost its base: %q", merged[:min(len(merged), 40)])
	}
	if !strings.Contains(merged, "[OPERATOR GUIDANCE]:") {
		t.Errorf("missing [OPERATOR GUIDANCE] section: %q", merged)
	}
	firstIdx := strings.Index(merged, "first: check robots.txt")
	secondIdx := strings.Index(merged, "second: avoid the paid API")
	if firstIdx < 0 || secondIdx < 0 || firstIdx > secondIdx {
		t.Errorf("chronological order broken (first@%d second@%d): %q", firstIdx, secondIdx, merged)
	}
	if strings.Contains(merged, "already delivered live") {
		t.Error("already-injected guidance must not repeat in the briefing")
	}

	// Both queued rows flipped to injected.
	rows, _ := store.ListGuidance(ctx, task.ID, 10)
	statuses := map[string]string{}
	for _, row := range rows {
		statuses[row.ID] = row.Status
	}
	for _, g := range []*platform.Guidance{g1, g2} {
		if statuses[g.ID] != platform.GuidanceInjected {
			t.Errorf("guidance %s status = %q, want injected", g.ID, statuses[g.ID])
		}
	}

	// WS events reached the instance buffer.
	s.instancesMu.RLock()
	inst := s.instances["inst-brief"]
	s.instancesMu.RUnlock()
	inst.mu.RLock()
	count := 0
	for _, evt := range inst.events {
		if evt.Type == "guidance_injected" {
			count++
		}
	}
	inst.mu.RUnlock()
	if count != 2 {
		t.Errorf("guidance_injected events = %d, want 2", count)
	}
}

// TestMergeQueuedGuidanceNoStore keeps the merge inert without a platform
// store or instance mapping (legacy direct scans pass through untouched).
func TestMergeQueuedGuidanceNoStore(t *testing.T) {
	s := &Server{}
	sess := &scanSession{instanceID: "inst-x"}
	if got := s.mergeQueuedGuidance(sess, "base instruction"); got != "base instruction" {
		t.Errorf("store-less merge changed the instruction: %q", got)
	}
}

// assertApprovalDecidedEvents counts approval_decided events in an instance
// buffer (ws_hub.go:296-317 replay path).
func assertApprovalDecidedEvents(t *testing.T, s *Server, instanceID string, want int) {
	t.Helper()
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		t.Fatal("instance missing")
	}
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	n := 0
	for _, evt := range inst.events {
		if evt.Type == "approval_decided" && evt.Approval != nil {
			n++
		}
	}
	if n != want {
		t.Errorf("approval_decided events = %d, want %d", n, want)
	}
}

// The approval gate exercise in enforce mode relies on the per-server
// service; a tiny helper keeps gate tests expressive.
func gateServiceFor(t *testing.T, s *Server) *ApprovalService {
	t.Helper()
	svc := s.approvalService()
	if svc == nil {
		t.Fatal("approval service must exist in enforce mode")
	}
	return svc
}
