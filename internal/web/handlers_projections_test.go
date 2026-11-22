package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
)

func projectionGet(t *testing.T, handler http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	handler(rr, req)
	return rr
}

// TestScanPlanProjection_ServesPersistedPlan pins the fix for the empty
// Planner pane: the plan detail must come from the run's state projection, not
// from build_plan/update_plan tool-call telemetry (which the engine never emits
// for engine-authored plans).
func TestScanPlanProjection_ServesPersistedPlan(t *testing.T) {
	s := newTestServer(t, nil)
	id := "plan-proj-1"
	dir := ledgerTestScan(t, s, id)
	rec, ok := loadScanRecordFromDir(dir)
	if !ok {
		t.Fatal("record missing")
	}
	rec.PlanPresent = true
	rec.PlanTasksTotal = 2
	rec.PlanTasksCompleted = 1
	rec.PlanTasksUnfinished = 1
	rec.PlanView = &agent.PlanView{
		Present:         true,
		Source:          "engine",
		PlanRevision:    "surf-1",
		SurfaceRevision: "surf-1",
		Counters:        agent.PlanCountersView{Total: 2, Completed: 1, Unfinished: 1},
		Tasks: []agent.PlanTaskView{
			{ID: "recon", Title: "Map surface", Phase: 1, Status: "completed", Origin: "auto"},
			{ID: "test-sqli", Title: "Test SQLi", Phase: 6, Status: "pending", VulnClass: "sqli", Origin: "auto-plan"},
		},
	}
	s.saveScanRecordTo(rec, dir)

	rr := projectionGet(t, s.handleScanPlan, "/api/scans/"+id+"/plan")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var view agent.PlanView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !view.Present || view.Source != "engine" || len(view.Tasks) != 2 {
		t.Fatalf("unexpected plan view: %#v", view)
	}
	if view.Partial {
		t.Fatal("persisted plan must not be marked partial")
	}
	if view.Tasks[1].VulnClass != "sqli" || view.Tasks[1].Phase != 6 {
		t.Fatalf("task detail lost: %#v", view.Tasks[1])
	}
}

// TestScanPlanProjection_CountersOnlyIsPartial keeps the honest distinction
// between "no plan" and "plan detail not persisted" (older runs).
func TestScanPlanProjection_CountersOnlyIsPartial(t *testing.T) {
	s := newTestServer(t, nil)
	id := "plan-proj-2"
	dir := ledgerTestScan(t, s, id)
	rec, _ := loadScanRecordFromDir(dir)
	rec.PlanPresent = true
	rec.PlanTasksTotal = 4
	rec.PlanTasksCompleted = 3
	rec.PlanTasksSkipped = 1
	s.saveScanRecordTo(rec, dir)

	rr := projectionGet(t, s.handleScanPlan, "/api/scans/"+id+"/plan")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var view agent.PlanView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !view.Present || !view.Partial || view.PartialReason != "not_persisted" {
		t.Fatalf("counters-only projection must be partial: %#v", view)
	}
	if view.Counters.Total != 4 || view.Counters.Completed != 3 || view.Counters.Skipped != 1 {
		t.Fatalf("counters lost: %#v", view.Counters)
	}
	if len(view.Tasks) != 0 {
		t.Fatalf("partial projection must not invent tasks: %#v", view.Tasks)
	}
}

func TestScanPlanProjection_UnknownScanAndMethod(t *testing.T) {
	s := newTestServer(t, nil)
	rr := projectionGet(t, s.handleScanPlan, "/api/scans/missing/plan")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	rr = httptest.NewRecorder()
	s.handleScanPlan(rr, httptest.NewRequest(http.MethodPost, "/api/scans/x/plan", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestInstanceAgentsProjection_CheckpointFallback pins the terminal read: after
// the live agent is released, worker state comes from the durable graph
// checkpoint the scanner already writes (never from event replay).
func TestInstanceAgentsProjection_CheckpointFallback(t *testing.T) {
	s := newTestServer(t, nil)
	id := "agents-proj-1"
	dir := ledgerTestScan(t, s, id)
	checkpoint := `{"version":1,"counter":2,"delegated":2,"agents":[
		{"id":"sub_1_1","name":"injection-serverside","task":"test sqli","targets":["https://example.com"],"status":"completed","started_at":"2026-01-01T00:00:00Z","completed_at":"2026-01-01T00:10:00Z"},
		{"id":"sub_2_2","name":"client-source","task":"test xss","targets":["https://example.com"],"status":"completed","started_at":"2026-01-01T00:00:05Z","completed_at":"2026-01-01T00:08:00Z"}
	]}`
	if err := os.WriteFile(filepath.Join(dir, "execution-graph.json"), []byte(checkpoint), 0o600); err != nil {
		t.Fatal(err)
	}

	view := s.instanceAgentsView(id)
	if !view.Available {
		t.Fatal("checkpoint projection must be available")
	}
	if len(view.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(view.Agents))
	}
	if view.Concurrency.Running != 0 {
		t.Fatalf("terminal projection must not claim running work: %#v", view.Concurrency)
	}
	if view.PartialReason != "checkpoint" {
		t.Fatalf("partial reason = %q, want checkpoint", view.PartialReason)
	}
	for _, worker := range view.Agents {
		if worker.Status != "completed" {
			t.Fatalf("worker %s status = %q", worker.AgentID, worker.Status)
		}
		if worker.Lane == "" {
			t.Fatalf("worker lane missing: %#v", worker)
		}
	}
}

// TestInstanceAgentsProjection_NoRunIsNeutral pins that an unknown instance
// renders a neutral state instead of an error or an invented worker list.
func TestInstanceAgentsProjection_NoRunIsNeutral(t *testing.T) {
	s := newTestServer(t, nil)
	view := s.instanceAgentsView("nope")
	if view.Available {
		t.Fatal("unknown instance must not be available")
	}
	if view.PartialReason != "no_run" {
		t.Fatalf("partial reason = %q, want no_run", view.PartialReason)
	}
	if len(view.Agents) != 0 {
		t.Fatalf("no agents expected: %#v", view.Agents)
	}
}
