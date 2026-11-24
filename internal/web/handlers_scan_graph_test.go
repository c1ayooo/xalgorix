package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func withGraphBackend(t *testing.T, store graph.Repository) {
	t.Helper()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })
}

func TestScanGraphProjection_ReadsAuthoritativeGraph(t *testing.T) {
	s := newTestServer(t, nil)
	store := graph.NewMemoryStore()
	withGraphBackend(t, store)
	ctx := context.Background()
	sid := "graph-proj-1"

	intent, err := store.AddNode(ctx, graph.NodeInput{
		ScanID: sid, Kind: graph.KindIntent, Priority: 940, Origin: "llm",
		Payload: `{"task_id":"test-sqli","title":"Test /api/users for SQLi","phase":6,"vuln_class":"sqli","endpoint":"/api/users","origin":"llm"}`,
	})
	if err != nil {
		t.Fatalf("add intent: %v", err)
	}
	goal, err := store.AddNode(ctx, graph.NodeInput{
		ScanID: sid, Kind: graph.KindGoal, Payload: `{"criterion":"criterion-1","title":"Prove SQLi"}`,
	})
	if err != nil {
		t.Fatalf("add goal: %v", err)
	}
	if err := store.AddEdge(ctx, graph.EdgeInput{ScanID: sid, SrcID: goal.ID, DstID: intent.ID, Rel: graph.RelSpawns}); err != nil {
		t.Fatalf("add edge: %v", err)
	}
	if _, err := store.AddNode(ctx, graph.NodeInput{
		ScanID: sid, Kind: graph.KindBelief, Payload: `{"hypothesis_id":"H-1","subject":"sqli @ /api/users","vuln_class":"sqli","phase":6}`,
	}); err != nil {
		t.Fatalf("add belief: %v", err)
	}
	if _, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// /graph returns the whole projection.
	rr := projectionGet(t, s.handleScanGraph, "/api/scans/"+sid+"/graph")
	if rr.Code != http.StatusOK {
		t.Fatalf("/graph status = %d body=%s", rr.Code, rr.Body.String())
	}
	var view struct {
		Source      string          `json:"source"`
		Nodes       []graphNodeView `json:"nodes"`
		Edges       []graph.Edge    `json:"edges"`
		Counters    graph.Counts    `json:"counters"`
		Conformance struct {
			OK       bool `json:"ok"`
			Errors   int  `json:"errors"`
			Warnings int  `json:"warnings"`
		} `json:"conformance"`
		Partial bool `json:"partial"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode graph: %v", err)
	}
	if view.Source != "graph" || view.Partial {
		t.Fatalf("source=%q partial=%v", view.Source, view.Partial)
	}
	if !view.Conformance.OK || view.Conformance.Errors != 0 {
		t.Fatalf("conformance = %+v", view.Conformance)
	}
	if len(view.Nodes) != 3 || len(view.Edges) != 1 {
		t.Fatalf("nodes=%d edges=%d", len(view.Nodes), len(view.Edges))
	}
	if view.Counters.RunningIntents != 1 || view.Counters.QueuedBeliefs != 1 {
		t.Fatalf("counters = %+v", view.Counters)
	}
	for _, n := range view.Nodes {
		if n.Kind == graph.KindIntent && n.State != graph.StateRunning {
			t.Fatalf("claimed intent state = %s", n.State)
		}
		if n.Kind == graph.KindIntent && n.PayloadObject["task_id"] != "test-sqli" {
			t.Fatalf("payload object lost: %#v", n.PayloadObject)
		}
	}

	// /plan reads the same authority and keeps the legacy PlanView shape.
	rr = projectionGet(t, s.handleScanPlan, "/api/scans/"+sid+"/plan")
	if rr.Code != http.StatusOK {
		t.Fatalf("/plan status = %d body=%s", rr.Code, rr.Body.String())
	}
	var plan agent.PlanView
	if err := json.Unmarshal(rr.Body.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if !plan.Present || len(plan.Tasks) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	task := plan.Tasks[0]
	if task.ID != "test-sqli" || task.VulnClass != "sqli" || task.Status != "active" ||
		task.Owner != "work#1" || task.LeaseUntil.IsZero() {
		t.Fatalf("plan task = %+v", task)
	}
	if plan.Source != "model" || plan.Counters.Total != 1 || plan.Counters.Unfinished != 1 {
		t.Fatalf("plan header = %#v", plan)
	}
}

func TestGuidanceProjectsHintAndConsumed(t *testing.T) {
	s := newTestServer(t, nil)
	store := graph.NewMemoryStore()
	withGraphBackend(t, store)
	ctx := context.Background()
	g := &platform.Guidance{
		ID: "g-1", TaskID: "task-1", RunID: "run-1", InstanceID: "run-guidance",
		Message: "focus on authz", Status: platform.GuidanceQueued, CreatedBy: "operator",
	}
	s.projectGuidanceHint(ctx, g)
	hints, err := store.ListNodes(ctx, "run-guidance", graph.KindHint)
	if err != nil || len(hints) != 1 {
		t.Fatalf("hints = %d err=%v", len(hints), err)
	}
	if hints[0].UnitKey != "g-1" || hints[0].State != graph.StateActive {
		t.Fatalf("hint node = %+v", hints[0])
	}
	g.Status = platform.GuidanceConsumed
	s.projectGuidanceHint(ctx, g)
	hints, _ = store.ListNodes(ctx, "run-guidance", graph.KindHint)
	if len(hints) != 1 || hints[0].State != graph.StateConsumed {
		t.Fatalf("consumed hint = %+v", hints)
	}
}

func TestGraphUnavailableIsResumablePause(t *testing.T) {
	if !isProviderPauseReason("graph_unavailable") {
		t.Fatal("graph_unavailable must map to a resumable pause")
	}
}

func TestScanGraphValidate_ReportsDoneWithoutYield(t *testing.T) {
	s := newTestServer(t, nil)
	store := graph.NewMemoryStore()
	withGraphBackend(t, store)
	ctx := context.Background()
	sid := "graph-validate-1"
	if _, err := store.AddNode(ctx, graph.NodeInput{
		ScanID: sid, Kind: graph.KindIntent, UnitKey: "T-drift",
		State: graph.StateDone, Payload: `{"task_id":"T-drift"}`,
	}); err != nil {
		t.Fatalf("add intent: %v", err)
	}
	rr := projectionGet(t, s.handleScanGraphValidate, "/api/scans/"+sid+"/graph/validate")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var report graph.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.OK || report.Errors == 0 {
		t.Fatalf("report = %+v, want a failing conformance report", report)
	}
	found := false
	for _, v := range report.Violations {
		if v.Code == graph.CodeIntentDoneNoYield {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing %s in %+v", graph.CodeIntentDoneNoYield, report.Violations)
	}
}

func TestScanGraphProjection_NoBackendIsHonestEmpty(t *testing.T) {
	s := newTestServer(t, nil)
	rr := projectionGet(t, s.handleScanGraph, "/api/scans/no-graph/graph")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Source  string `json:"source"`
		Partial bool   `json:"partial"`
		Nodes   []any  `json:"nodes"`
		Edges   []any  `json:"edges"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != "none" || !body.Partial || len(body.Nodes) != 0 || len(body.Edges) != 0 {
		t.Fatalf("empty graph projection = %#v", body)
	}
}
