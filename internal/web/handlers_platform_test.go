package web

// Handler-level tests for the R1 platform API (docs/API_CONTRACT.md §1/§2/§3.2):
// projects/tasks CRUD envelopes, POST /api/tasks/{id}/runs dispatch + scope
// isolation, the scan.json projection onto runs, and the optional project_id
// filter compatibility on the pre-existing list endpoints.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// newPlatformTestServer wires a fresh server with a file store rooted at the
// server's data dir.
func newPlatformTestServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	return s
}

func doJSON(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	routePath := strings.SplitN(path, "?", 2)[0]
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	switch {
	case routePath == "/api/projects":
		s.handleProjects(rr, req)
	case strings.HasPrefix(routePath, "/api/projects"):
		s.handleProjectDetail(rr, req)
	case strings.HasPrefix(routePath, "/api/tasks/"):
		s.handleTaskDetail(rr, req)
	case routePath == "/api/instances":
		s.handleInstances(rr, req)
	case routePath == "/api/findings":
		s.handleFindingsList(rr, req)
	case routePath == "/api/findings/summary":
		s.handleFindingsSummary(rr, req)
	case routePath == "/api/scans":
		s.handleListScans(rr, req)
	case strings.HasPrefix(routePath, "/api/scans/") && strings.HasSuffix(routePath, "/decisions"):
		s.handleScanDecisions(rr, req)
	case strings.HasPrefix(routePath, "/api/scans/") && strings.HasSuffix(routePath, "/intel-access"):
		s.handleScanIntelAccess(rr, req)
	case routePath == "/api/schedules":
		s.handleSchedules(rr, req)
	case strings.HasPrefix(routePath, "/api/schedules"):
		s.handleScheduleDetail(rr, req)
	default:
		t.Fatalf("no handler routed for %s %s", method, path)
	}
	return rr
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rr.Body.String())
	}
	return body
}

func TestPlatformProjectAPIContractShapes(t *testing.T) {
	s := newPlatformTestServer(t)

	// POST → 201 with the contract shape.
	rr := doJSON(t, s, http.MethodPost, "/api/projects",
		`{"name":"Acme Pentest","scope":{"targets":["acme.example","*.acme.example"],"allow_ports":[443,8443]}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /api/projects code = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeBody(t, rr)
	projID, _ := body["id"].(string)
	if projID == "" {
		t.Fatal("POST response missing id")
	}
	if body["slug"] != "acme-pentest" {
		t.Errorf("slug = %v, want acme-pentest", body["slug"])
	}
	scope, _ := body["scope"].(map[string]any)
	if len(scope["targets"].([]any)) != 2 {
		t.Errorf("scope.targets = %v", scope["targets"])
	}

	// GET list → {"projects":[...]} with task_count/active_runs fields.
	rr = doJSON(t, s, http.MethodGet, "/api/projects", "")
	body = decodeBody(t, rr)
	projects, _ := body["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("GET /api/projects returned %d projects, want 1 (archived excluded)", len(projects))
	}

	// GET one.
	rr = doJSON(t, s, http.MethodGet, "/api/projects/"+projID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET project code = %d", rr.Code)
	}

	// PATCH archive → drops from list.
	rr = doJSON(t, s, http.MethodPatch, "/api/projects/"+projID, `{"archived":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH project code = %d; body=%s", rr.Code, rr.Body.String())
	}
	rr = doJSON(t, s, http.MethodGet, "/api/projects", "")
	body = decodeBody(t, rr)
	if n := len(body["projects"].([]any)); n != 0 {
		t.Errorf("archived project still listed: %d", n)
	}

	// Unknown id → 404 envelope.
	rr = doJSON(t, s, http.MethodGet, "/api/projects/does-not-exist", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown project code = %d, want 404", rr.Code)
	}
	if _, ok := decodeBody(t, rr)["error"]; !ok {
		t.Error("404 must carry the {\"error\"} envelope")
	}
}

func TestPlatformDefaultProjectProtection(t *testing.T) {
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	proj, err := store.EnsureDefaultProject(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rr := doJSON(t, s, http.MethodDelete, "/api/projects/"+proj.ID, "")
	if rr.Code != http.StatusConflict {
		t.Fatalf("DELETE default project code = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPlatformProjectDeleteGuardedAgainstActiveRuns(t *testing.T) {
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()

	project, err := store.CreateProject(ctx, "Doomed", platform.Scope{Targets: []string{"doomed.example"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Active task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "doomed-inst", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	// The instance's scan.json says "running": the delete guard must refuse.
	writeScanFixture(t, s.dataDir, "doomed.example", "2026-06-01", ScanRecord{
		ID: "doomed-scan-1", InstanceID: "doomed-inst", Target: "doomed.example",
		StartedAt: time.Now().Format(time.RFC3339), Status: "running", ScanMode: "single",
	})

	rr := doJSON(t, s, http.MethodDelete, "/api/projects/"+project.ID, "")
	if rr.Code != http.StatusConflict {
		t.Fatalf("DELETE with active run code = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	// After the run terminates the delete goes through.
	writeScanFixture(t, s.dataDir, "doomed.example", "2026-06-01", ScanRecord{
		ID: "doomed-scan-1", InstanceID: "doomed-inst", Target: "doomed.example",
		StartedAt: time.Now().Format(time.RFC3339), Status: "finished", ScanMode: "single",
	})
	s.invalidateScanListCache()
	rr = doJSON(t, s, http.MethodDelete, "/api/projects/"+project.ID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE after terminal run code = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPlatformTaskAPIAndProjectedStats(t *testing.T) {
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()

	project, err := store.CreateProject(ctx, "Stats Project", platform.Scope{Targets: []string{"stats.example"}})
	if err != nil {
		t.Fatal(err)
	}

	// POST task → 201 with contract shape.
	rr := doJSON(t, s, http.MethodPost, "/api/projects/"+project.ID+"/tasks",
		`{"name":"Edge review","goal":"Assess the edge","instruction":"Focus auth","phase_policy":{"scan_mode":"wildcard","severity_filter":["critical"]},"schedule":{"interval":"daily","time":"08:00"}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST tasks code = %d; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeBody(t, rr)
	if body["project_id"] != project.ID {
		t.Errorf("task.project_id = %v, want %v", body["project_id"], project.ID)
	}
	if body["phase_policy"].(map[string]any)["scan_mode"] != "wildcard" {
		t.Errorf("phase_policy = %v", body["phase_policy"])
	}
	if body["schedule"].(map[string]any)["time"] != "08:00" {
		t.Errorf("schedule = %v", body["schedule"])
	}

	// GET list envelope with projections (0 runs yet).
	rr = doJSON(t, s, http.MethodGet, "/api/projects/"+project.ID+"/tasks", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET tasks code = %d", rr.Code)
	}
	body = decodeBody(t, rr)
	tasks, _ := body["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("GET tasks returned %d, want 1", len(tasks))
	}
	taskMap := tasks[0].(map[string]any)
	if taskMap["run_count"] != float64(0) {
		t.Errorf("run_count = %v, want 0", taskMap["run_count"])
	}

	// GET single with projected stats after mounting a finished run.
	realTask, err := store.ListTasks(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeScanFixture(t, s.dataDir, "stats.example", "2026-06-02", ScanRecord{
		ID: "stats-scan-1", InstanceID: "stats-inst-1", Target: "stats.example",
		StartedAt: time.Now().Add(-time.Hour).Format(time.RFC3339), Status: "finished", ScanMode: "wildcard",
		TotalTokens: 1234,
		Vulns:       []VulnSummary{{ID: "v1", Title: "XSS", Severity: "high", Endpoint: "/q"}},
	})
	if _, err := store.CreateRun(ctx, realTask[0].ID, platform.RunMount{InstanceID: "stats-inst-1", Mode: "wildcard", Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}

	rr = doJSON(t, s, http.MethodGet, "/api/tasks/"+realTask[0].ID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET task code = %d", rr.Code)
	}
	taskMap = decodeBody(t, rr)
	if taskMap["run_count"] != float64(1) {
		t.Errorf("run_count = %v, want 1", taskMap["run_count"])
	}
	if taskMap["last_run_status"] != "finished" {
		t.Errorf("last_run_status = %v, want finished", taskMap["last_run_status"])
	}
	if taskMap["finding_count"] != float64(1) {
		t.Errorf("finding_count = %v, want 1", taskMap["finding_count"])
	}
	if lastAt, _ := taskMap["last_run_at"].(string); lastAt == "" {
		t.Error("last_run_at not projected")
	}

	// Run list projection: status/tokens flow from scan.json.
	rr = doJSON(t, s, http.MethodGet, "/api/tasks/"+realTask[0].ID+"/runs", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET runs code = %d", rr.Code)
	}
	runsBody := decodeBody(t, rr)
	if runsBody["total"] != float64(1) {
		t.Errorf("runs total = %v", runsBody["total"])
	}
	run := runsBody["items"].([]any)[0].(map[string]any)
	if run["status"] != "finished" {
		t.Errorf("run.status = %v, want finished (scan.json projection)", run["status"])
	}
	if run["total_tokens"] != float64(1234) {
		t.Errorf("run.total_tokens = %v, want 1234", run["total_tokens"])
	}
	if run["vuln_count"] != float64(1) {
		t.Errorf("run.vuln_count = %v, want 1", run["vuln_count"])
	}

	// Task findings: the run's vulns flattened.
	rr = doJSON(t, s, http.MethodGet, "/api/tasks/"+realTask[0].ID+"/findings", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET findings code = %d", rr.Code)
	}
	var findings []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &findings); err != nil {
		t.Fatalf("findings decode: %v; body=%s", err, rr.Body.String())
	}
	if len(findings) != 1 || findings[0]["title"] != "XSS" {
		t.Errorf("findings = %+v", findings)
	}
	if findings[0]["scan_id"] != "stats-scan-1" {
		t.Errorf("finding scan_id = %v", findings[0]["scan_id"])
	}

	// PATCH + DELETE task.
	rr = doJSON(t, s, http.MethodPatch, "/api/tasks/"+realTask[0].ID, `{"goal":"Updated goal"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH task code = %d", rr.Code)
	}
	if decodeBody(t, rr)["goal"] != "Updated goal" {
		t.Error("goal patch not applied")
	}
	rr = doJSON(t, s, http.MethodDelete, "/api/tasks/"+realTask[0].ID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE task code = %d", rr.Code)
	}
	rr = doJSON(t, s, http.MethodGet, "/api/tasks/"+realTask[0].ID, "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("deleted task GET = %d, want 404", rr.Code)
	}
}

func TestPlatformTaskRunStartScopeIsolation(t *testing.T) {
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()

	project, err := store.CreateProject(ctx, "Scoped", platform.Scope{
		Targets:    []string{"acme.example"},
		AllowPorts: []int{8443},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{
		Name:        "Scoped task",
		Instruction: "check auth",
		PhasePolicy: &platform.PhasePolicy{ScanMode: "single", SeverityFilter: []string{"high"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// ① Range isolation: out-of-scope target → 400, no dispatch, no run.
	rr := doJSON(t, s, http.MethodPost, "/api/tasks/"+task.ID+"/runs", `{"targets":["https://evil.example"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("out-of-scope target code = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	_, total, err := store.ListRuns(ctx, task.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("out-of-scope dispatch mounted a run (%d)", total)
	}

	// allow_ports: explicit port outside the allowlist → 400.
	rr = doJSON(t, s, http.MethodPost, "/api/tasks/"+task.ID+"/runs", `{"targets":["https://acme.example:9443"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("port outside allow_ports code = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}

	// A request body with no targets falls back to the project scope; with an
	// empty scope (and no override) it must 400 rather than dispatch an empty
	// scan.
	emptyProject, err := store.CreateProject(ctx, "Empty Scope", platform.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	emptyTask, err := store.CreateTask(ctx, emptyProject.ID, platform.TaskInput{Name: "No scope"})
	if err != nil {
		t.Fatal(err)
	}
	rr = doJSON(t, s, http.MethodPost, "/api/tasks/"+emptyTask.ID+"/runs", `{}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing targets with empty scope code = %d, want 400", rr.Code)
	}
}

// TestPlatformTaskRunStartGuardsPreDispatch pins the two /api/scan parity
// guards the platform dispatch must apply before any state exists:
//   - HIGH: a discord_webhook pointing at the operator's machine (loopback)
//     is refused with 400 — no dispatch, no run mount;
//   - R11.6: an unresolvable provider_profile fails fast with 400 instead of
//     silently falling back to the default client.
func TestPlatformTaskRunStartGuardsPreDispatch(t *testing.T) {
	s := newPlatformTestServer(t)
	bestEffortDataDir(t, s)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	ctx := context.Background()

	project, err := store.CreateProject(ctx, "Guards", platform.Scope{Targets: []string{"example.com", "*.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Guard task"})
	if err != nil {
		t.Fatal(err)
	}

	assertNoDispatch := func(label string) {
		t.Helper()
		_, total, err := store.ListRuns(ctx, task.ID, 1, 10)
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 {
			t.Fatalf("%s: a run was mounted for a refused request (%d)", label, total)
		}
		s.instancesMu.RLock()
		live := len(s.instances)
		s.instancesMu.RUnlock()
		if live != 0 {
			t.Fatalf("%s: an instance was dispatched for a refused request (%d live)", label, live)
		}
	}

	// HIGH-1: loopback webhook → 400 (checkScanRequestDestinations).
	rr := doJSON(t, s, http.MethodPost, "/api/tasks/"+task.ID+"/runs",
		`{"targets":["https://app.example.com"],"discord_webhook":"http://127.0.0.1:9137/discord-hook"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("loopback webhook code = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if msg := rr.Body.String(); !strings.Contains(msg, "error") {
		t.Errorf("loopback webhook response lacks the error envelope: %s", msg)
	}
	assertNoDispatch("loopback webhook")

	// MEDIUM-1: unknown provider_profile → 400 (resolveScanCredentials fail-fast).
	rr = doJSON(t, s, http.MethodPost, "/api/tasks/"+task.ID+"/runs",
		`{"targets":["https://app.example.com"],"provider_profile":"openai:no-such-profile-12345"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown provider_profile code = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	assertNoDispatch("unknown provider_profile")

	// Control: the same request without offending fields dispatches fine.
	rr = doJSON(t, s, http.MethodPost, "/api/tasks/"+task.ID+"/runs", `{"targets":["https://app.example.com"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("control dispatch code = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPlatformScopeRejectsPortEntries pins MEDIUM-4(a): scope targets are
// hostnames only — an entry carrying its own ":port" is refused at write time
// (ports are governed by allow_ports exclusively; a per-entry port would make
// the port policy fail-open).
func TestPlatformScopeRejectsPortEntries(t *testing.T) {
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()

	// POST /api/projects with a port-carrying scope entry → 400 naming allow_ports.
	rr := doJSON(t, s, http.MethodPost, "/api/projects",
		`{"name":"Porty","scope":{"targets":["acme.example:8443"],"allow_ports":[443]}}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("port-carrying scope create code = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if msg := rr.Body.String(); !strings.Contains(msg, "allow_ports") {
		t.Errorf("error message should point at allow_ports: %s", msg)
	}

	// PATCH the scope of an existing project → 400, scope unchanged.
	project, err := store.CreateProject(ctx, "Portless", platform.Scope{Targets: []string{"clean.example"}})
	if err != nil {
		t.Fatal(err)
	}
	rr = doJSON(t, s, http.MethodPatch, "/api/projects/"+project.ID,
		`{"scope":{"targets":["patched.example:9443"]}}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("port-carrying scope patch code = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	got, err := store.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Scope.Targets) != 1 || got.Scope.Targets[0] != "clean.example" {
		t.Errorf("refused patch mutated the scope: %+v", got.Scope)
	}

	// Bracketed IPv6 with a port is refused too; a bare IPv6 host is not.
	rr = doJSON(t, s, http.MethodPost, "/api/projects",
		`{"name":"V6bracket","scope":{"targets":["[2001:db8::1]:443"]}}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bracketed IPv6 with port code = %d, want 400", rr.Code)
	}
	if _, err := store.CreateProject(ctx, "V6bare", platform.Scope{Targets: []string{"2001:db8::1"}}); err != nil {
		t.Errorf("bare IPv6 scope entry rejected: %v", err)
	}
}

func TestPlatformTaskRunStartDispatchesAndMounts(t *testing.T) {
	s := newPlatformTestServer(t)
	// The dispatch goroutine keeps writing under dataDir after the test
	// returns (same pattern as TestHandleScanAckIsPendingNotStarted).
	bestEffortDataDir(t, s)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	ctx := context.Background()

	project, err := store.CreateProject(ctx, "Dispatch", platform.Scope{Targets: []string{"example.com", "*.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{
		Name:        "Dispatch task",
		Goal:        "Baseline goal from task",
		PhasePolicy: &platform.PhasePolicy{ScanMode: "single"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// In-scope subdomain → dispatch accepted. targets come from the request
	// override; instruction falls back task.instruction → task.goal.
	rr := doJSON(t, s, http.MethodPost, "/api/tasks/"+task.ID+"/runs", `{"targets":["https://app.example.com"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("in-scope dispatch code = %d; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeBody(t, rr)
	if body["status"] != "pending" {
		t.Errorf("ack status = %v, want \"pending\" (real dispatch state, same semantics as /api/scan)", body["status"])
	}
	instanceID, _ := body["instance_id"].(string)
	runID, _ := body["run_id"].(string)
	if instanceID == "" || runID == "" {
		t.Fatalf("ack missing ids: %+v", body)
	}

	// The mount record exists and carries the instance + mode.
	runs, total, err := store.ListRuns(ctx, task.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("mounted runs = %d, want 1", total)
	}
	if runs[0].InstanceID != instanceID {
		t.Errorf("mount instance = %q, want %q", runs[0].InstanceID, instanceID)
	}
	if runs[0].Mode != "single" {
		t.Errorf("mount mode = %q, want single (phase_policy fallback)", runs[0].Mode)
	}
	if runs[0].Trigger != "manual" {
		t.Errorf("mount trigger = %q, want manual", runs[0].Trigger)
	}

	// Project-scoped instance filter finds the dispatched instance via
	// runs.instance_id (§3.2), while an unrelated project matches nothing.
	// The live instance is injected deterministically — the dispatch
	// goroutine registers its own copy asynchronously.
	s.instancesMu.Lock()
	s.instances[instanceID] = &ScanInstance{ID: instanceID, Name: "Dispatch task", Status: "pending", Targets: "app.example.com"}
	s.instancesMu.Unlock()
	other, err := store.CreateProject(ctx, "Other", platform.Scope{Targets: []string{"other.example"}})
	if err != nil {
		t.Fatal(err)
	}
	rr = doJSON(t, s, http.MethodGet, "/api/instances?project_id="+project.ID, "")
	body = decodeBody(t, rr)
	found := false
	for _, inst := range body["instances"].([]any) {
		if inst.(map[string]any)["id"] == instanceID {
			found = true
		}
	}
	if !found {
		t.Errorf("project filter lost the dispatched instance %s: %v", instanceID, body["instances"])
	}
	rr = doJSON(t, s, http.MethodGet, "/api/instances?project_id="+other.ID, "")
	body = decodeBody(t, rr)
	if n := len(body["instances"].([]any)); n != 0 {
		t.Errorf("row-level isolation: other project sees %d instances", n)
	}

	_ = runID
}

func TestPlatformProjectFilterBackwardCompatible(t *testing.T) {
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()

	// Two fixture scans: one modern (InstanceID set), one legacy (no
	// InstanceID — matched via runs.legacy_scan_id == record ID).
	writeScanFixture(t, s.dataDir, "modern.example", "2026-06-03", ScanRecord{
		ID: "modern-scan", InstanceID: "modern-inst", Target: "modern.example",
		StartedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339), Status: "finished", ScanMode: "single",
		Vulns: []VulnSummary{{ID: "vm", Title: "Modern vuln", Severity: "critical"}},
	})
	writeScanFixture(t, s.dataDir, "legacy.example", "2026-06-04", ScanRecord{
		ID: "legacy-scan", Target: "legacy.example",
		StartedAt: time.Now().Add(-time.Hour).Format(time.RFC3339), Status: "finished", ScanMode: "wildcard",
		Vulns: []VulnSummary{{ID: "vl", Title: "Legacy vuln", Severity: "high"}},
	})

	project, err := store.CreateProject(ctx, "Filter", platform.Scope{Targets: []string{"modern.example"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Filter task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "modern-inst", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	legacyTask, err := store.EnsureImportTask(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ImportLegacyRun(ctx, legacyTask.ID, platform.RunMount{
		LegacyScanID: "legacy-scan", InstanceID: "legacy-scan", Mode: "wildcard",
	}); err != nil {
		t.Fatal(err)
	}

	// ① /api/instances — inject two live instances; the filter keeps only the
	// project's.
	s.instancesMu.Lock()
	s.instances["modern-inst"] = &ScanInstance{ID: "modern-inst", Name: "Modern", Status: "finished", Targets: "modern.example"}
	s.instances["legacy-scan"] = &ScanInstance{ID: "legacy-scan", Name: "Legacy", Status: "finished", Targets: "legacy.example"}
	s.instances["unrelated-inst"] = &ScanInstance{ID: "unrelated-inst", Name: "Unrelated", Status: "finished", Targets: "elsewhere.example"}
	s.instancesMu.Unlock()

	assertInstanceIDs := func(rr *httptest.ResponseRecorder, want map[string]bool, label string) {
		t.Helper()
		body := decodeBody(t, rr)
		got := map[string]bool{}
		for _, inst := range body["instances"].([]any) {
			got[inst.(map[string]any)["id"].(string)] = true
		}
		if len(got) != len(want) {
			t.Fatalf("%s: instances = %v, want %v", label, got, want)
		}
		for id := range want {
			if !got[id] {
				t.Fatalf("%s: instances = %v, want %v", label, got, want)
			}
		}
	}

	rr := doJSON(t, s, http.MethodGet, "/api/instances?project_id="+project.ID, "")
	assertInstanceIDs(rr, map[string]bool{"modern-inst": true, "legacy-scan": true}, "project filter")
	rr = doJSON(t, s, http.MethodGet, "/api/instances", "")
	assertInstanceIDs(rr, map[string]bool{"modern-inst": true, "legacy-scan": true, "unrelated-inst": true}, "no filter (legacy behavior)")

	// ② /api/findings — only the project's vulns, deduped; no param = both.
	rr = doJSON(t, s, http.MethodGet, "/api/findings?project_id="+project.ID, "")
	var findings []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &findings); err != nil {
		t.Fatalf("findings decode: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("project findings = %d, want 2 (modern + legacy via legacy_scan_id)", len(findings))
	}
	rr = doJSON(t, s, http.MethodGet, "/api/findings", "")
	if err := json.Unmarshal(rr.Body.Bytes(), &findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("unfiltered findings = %d, want 2", len(findings))
	}

	// ③ /api/findings/summary — totals scoped; no param = full totals.
	rr = doJSON(t, s, http.MethodGet, "/api/findings/summary?project_id="+project.ID, "")
	summary := decodeBody(t, rr)
	totals := summary["totals"].(map[string]any)
	if totals["critical"] != float64(1) || totals["high"] != float64(1) {
		t.Errorf("project summary totals = %v", totals)
	}
	rr = doJSON(t, s, http.MethodGet, "/api/findings/summary", "")
	summary = decodeBody(t, rr)
	totals = summary["totals"].(map[string]any)
	if totals["critical"] != float64(1) || totals["high"] != float64(1) {
		t.Errorf("unfiltered summary totals = %v", totals)
	}

	// ④ /api/scans — project filter keeps both mounts; no param unchanged.
	rr = doJSON(t, s, http.MethodGet, "/api/scans?project_id="+project.ID, "")
	var scans []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &scans); err != nil {
		t.Fatalf("scans decode: %v", err)
	}
	if len(scans) != 2 {
		t.Fatalf("project scans = %d, want 2", len(scans))
	}
	// Run/task linkage (D11, response-only): list rows carry their mount's
	// task and trigger. Empty would be honest "unmounted"; here both rows are
	// mounted, so both must resolve — the legacy one via legacy_scan_id.
	byID := map[string]map[string]any{}
	for _, row := range scans {
		byID[row["id"].(string)] = row
	}
	if row := byID["modern-scan"]; row["task_id"] != task.ID || row["task_name"] != "Filter task" {
		t.Errorf("modern-scan linkage = %v/%v, want %s/Filter task", row["task_id"], row["task_name"], task.ID)
	}
	if row := byID["legacy-scan"]; row["task_id"] != legacyTask.ID || row["task_name"] != legacyTask.Name {
		t.Errorf("legacy-scan linkage = %v/%v, want %s/%s", row["task_id"], row["task_name"], legacyTask.ID, legacyTask.Name)
	}
	rr = doJSON(t, s, http.MethodGet, "/api/scans", "")
	if err := json.Unmarshal(rr.Body.Bytes(), &scans); err != nil {
		t.Fatal(err)
	}
	if len(scans) != 2 {
		t.Fatalf("unfiltered scans = %d, want 2", len(scans))
	}

	// ⑤ Unknown project → empty views, never "everything".
	rr = doJSON(t, s, http.MethodGet, "/api/findings?project_id=no-such-project", "")
	if err := json.Unmarshal(rr.Body.Bytes(), &findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("unknown project findings = %d, want 0", len(findings))
	}
}

// TestPlatformTaskRunStartOneShotGuard pins the one-active-run-per-task rule
// (任务一次性的用户裁决): while a run of the task projects active
// (running/paused — pending counts too via the dispatch snapshot), a second
// POST /api/tasks/{id}/runs answers 409 carrying the active run identity and
// mounts nothing; once the run turns terminal, the continuation path (finish
// the goal, add a new one, run again) is open again.
func TestPlatformTaskRunStartOneShotGuard(t *testing.T) {
	s := newPlatformTestServer(t)
	bestEffortDataDir(t, s)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	ctx := context.Background()

	project, err := store.CreateProject(ctx, "OneShot", platform.Scope{Targets: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "One-shot task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "oneshot-inst", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	writeScanFixture(t, s.dataDir, "example.com", "2026-06-03", ScanRecord{
		ID: "oneshot-scan-1", InstanceID: "oneshot-inst", Target: "example.com",
		StartedAt: time.Now().Format(time.RFC3339), Status: "running", ScanMode: "single",
	})
	s.invalidateScanListCache()

	refuse := func(label, wantInstance string) {
		t.Helper()
		rr := doJSON(t, s, http.MethodPost, "/api/tasks/"+task.ID+"/runs", `{"targets":["https://app.example.com"]}`)
		if rr.Code != http.StatusConflict {
			t.Fatalf("%s: dispatch code = %d, want 409; body=%s", label, rr.Code, rr.Body.String())
		}
		body := decodeBody(t, rr)
		if body["run_id"] == "" || body["instance_id"] != wantInstance {
			t.Errorf("%s: 409 must carry the active run identity: %+v", label, body)
		}
		_, total, err := store.ListRuns(ctx, task.ID, 1, 10)
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 {
			t.Fatalf("%s: refused dispatch mounted a run (%d total)", label, total)
		}
	}

	refuse("while running", "oneshot-inst")

	// Paused counts as active: a paused run still owns the task.
	writeScanFixture(t, s.dataDir, "example.com", "2026-06-03", ScanRecord{
		ID: "oneshot-scan-1", InstanceID: "oneshot-inst", Target: "example.com",
		StartedAt: time.Now().Format(time.RFC3339), Status: "paused", ScanMode: "single",
	})
	s.invalidateScanListCache()
	refuse("while paused", "oneshot-inst")

	// Terminal run → the same request dispatches again (new goal continuation).
	writeScanFixture(t, s.dataDir, "example.com", "2026-06-03", ScanRecord{
		ID: "oneshot-scan-1", InstanceID: "oneshot-inst", Target: "example.com",
		StartedAt: time.Now().Format(time.RFC3339), Status: "finished", ScanMode: "single",
	})
	s.invalidateScanListCache()
	rr := doJSON(t, s, http.MethodPost, "/api/tasks/"+task.ID+"/runs", `{"targets":["https://app.example.com"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("dispatch after terminal run code = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if decodeBody(t, rr)["status"] != "pending" {
		t.Error("continuation dispatch ack status != pending")
	}
}

// writeScanFixture creates a scan directory (dataDir/<target>/<date>/<id>/)
// with a scan.json record, the same shape createScanDirFor produces.
func writeScanFixture(t *testing.T, dataDir, target, date string, rec ScanRecord) string {
	t.Helper()
	dir := filepath.Join(dataDir, target, date, rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scan.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestPlatformWritesNoCredentialMaterial pins the D7 credentials dimension
// for this milestone: project/task/run operations and the legacy import must
// not create or touch credential stores — the global auth-profiles.json
// remains the only credential file and is owned by internal/auth (the
// project-private credential table ships with the R2 approvals batch, so
// this surface must stay credential-free).
func TestPlatformWritesNoCredentialMaterial(t *testing.T) {
	s := newPlatformTestServer(t)
	ctx := context.Background()
	store := s.currentPlatformStore()

	project, err := store.CreateProject(ctx, "Credless", platform.Scope{Targets: []string{"credless.example"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Credless task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "credless-inst", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	s.importLegacyPlatformRecords()

	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if strings.Contains(name, "auth-profiles") || strings.Contains(name, "credential") || strings.Contains(name, "secret") {
			t.Errorf("platform operations created a credential-like file: %s", e.Name())
		}
	}
}
