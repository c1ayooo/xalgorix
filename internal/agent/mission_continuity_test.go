package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

func missionAgent(t *testing.T, criteria ...ObjectiveCriterion) (*Agent, *scanctx.ScanContext, *graph.MemoryStore) {
	t.Helper()
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })
	sctx, state := newTestCtxState(t)
	sctx.SetInstanceID("run-mission-" + t.Name())
	state.Objective = &ObjectiveSpec{Statement: "mission goal", Criteria: criteria}
	ag := &Agent{scanCtx: sctx, state: state}
	return ag, sctx, store
}

func addMissionIntent(t *testing.T, store *graph.MemoryStore, scanID, unit, ref string) {
	t.Helper()
	_, err := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: scanID, Kind: graph.KindIntent, UnitKey: unit, State: graph.StateOpen,
		Payload: `{"task_id":"` + unit + `","title":"` + unit + `","objective_ref":"` + ref + `"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRootGoalAggregatesMultipleIntents pins P0 test 1: multiple intents
// advance the same root goal, including the legacy "GOAL" sentinel.
func TestRootGoalAggregatesMultipleIntents(t *testing.T) {
	ag, sctx, store := missionAgent(t, ObjectiveCriterion{
		ID: "C1", Kind: "finding_class", VulnClass: "sqli", VerifyBy: "verifier", Required: true,
	})
	addMissionIntent(t, store, sctx.InstanceID, "i-1", "GOAL")
	addMissionIntent(t, store, sctx.InstanceID, "i-2", "C1")
	ag.syncExplorationGraph()

	goals, _ := store.ListNodes(context.Background(), sctx.InstanceID, graph.KindGoal)
	goalIDs := map[string]int64{}
	for _, g := range goals {
		goalIDs[g.UnitKey] = g.ID
	}
	if goalIDs["GOAL"] == 0 || goalIDs["C1"] == 0 {
		t.Fatalf("goal nodes missing: %+v", goals)
	}
	tasks, _ := store.PlanTasks(context.Background(), sctx.InstanceID)
	intentIDs := map[string]int64{}
	for _, task := range tasks {
		intentIDs[task.TaskID] = task.NodeID
	}
	edges, _ := store.ListEdges(context.Background(), sctx.InstanceID)
	rootToI1, c1ToI2 := false, false
	for _, e := range edges {
		if e.Rel != graph.RelSpawns {
			continue
		}
		if e.SrcID == goalIDs["GOAL"] && e.DstID == intentIDs["i-1"] {
			rootToI1 = true
		}
		if e.SrcID == goalIDs["C1"] && e.DstID == intentIDs["i-2"] {
			c1ToI2 = true
		}
	}
	if !rootToI1 || !c1ToI2 {
		t.Fatalf("goal lineage missing: root->i1=%v C1->i2=%v edges=%+v", rootToI1, c1ToI2, edges)
	}
}

// TestRootGoalStaysOpenOnPartialCriterion pins P0 test 7: one met criterion
// never closes the root while a required decidable criterion is still open.
func TestRootGoalStaysOpenOnPartialCriterion(t *testing.T) {
	ag, sctx, store := missionAgent(t,
		ObjectiveCriterion{ID: "C1", Kind: "finding_class", VulnClass: "sqli", VerifyBy: "verifier", Required: true},
		ObjectiveCriterion{ID: "C2", Kind: "finding_class", VulnClass: "xss", VerifyBy: "verifier", Required: true},
	)
	reporting.SeedVulnsForContext(sctx.ID, []reporting.Vulnerability{{
		ID: "V-1", Title: "SQLi in login", Severity: "high", Tags: []string{reporting.TagVerified},
	}})
	ag.syncExplorationGraph()

	goals, _ := store.ListNodes(context.Background(), sctx.InstanceID, graph.KindGoal)
	states := map[string]graph.State{}
	for _, g := range goals {
		states[g.UnitKey] = g.State
	}
	if states["C1"] != graph.StateMet || states["C2"] == graph.StateMet {
		t.Fatalf("criterion states = %+v", states)
	}
	if states["GOAL"] == graph.StateMet {
		t.Fatalf("root goal met with an open required criterion: %+v", states)
	}
}

// TestRootGoalMetWhenAllRequiredMet pins the positive half of the root
// completion rule.
func TestRootGoalMetWhenAllRequiredMet(t *testing.T) {
	ag, sctx, store := missionAgent(t,
		ObjectiveCriterion{ID: "C1", Kind: "finding_class", VulnClass: "sqli", VerifyBy: "verifier", Required: true},
		ObjectiveCriterion{ID: "C2", Kind: "finding_class", VulnClass: "xss", VerifyBy: "verifier", Required: true},
	)
	reporting.SeedVulnsForContext(sctx.ID, []reporting.Vulnerability{
		{ID: "V-1", Title: "SQLi in login", Severity: "high", Tags: []string{reporting.TagVerified}},
		{ID: "V-2", Title: "XSS in search", Severity: "medium", Tags: []string{reporting.TagVerified}},
	})
	ag.syncExplorationGraph()
	goals, _ := store.ListNodes(context.Background(), sctx.InstanceID, graph.KindGoal)
	for _, g := range goals {
		if g.UnitKey == "GOAL" && g.State != graph.StateMet {
			t.Fatalf("root goal = %s, want met", g.State)
		}
	}
}

// TestCanceledWorkerDoesNotCompleteGoal pins P0 test 4: a canceled worker does
// not synthesize goal completion.
func TestCanceledWorkerDoesNotCompleteGoal(t *testing.T) {
	ag, sctx, store := missionAgent(t, ObjectiveCriterion{
		ID: "C1", Kind: "finding_class", VulnClass: "sqli", VerifyBy: "verifier", Required: true,
	})
	ag.state.PlannerWorkerMode = true
	ag.syncExplorationGraph()
	status, reasons := scanCompletionAssessment(ag.state)
	if status != CompletionStatusIncomplete {
		t.Fatalf("status = %s, want incomplete (reasons=%v)", status, reasons)
	}
	if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, " "), "C1") {
		t.Fatalf("incomplete reasons = %v", reasons)
	}
	goals, _ := store.ListNodes(context.Background(), sctx.InstanceID, graph.KindGoal)
	for _, g := range goals {
		if g.UnitKey == "GOAL" && g.State == graph.StateMet {
			t.Fatal("root goal was marked met without any verified criterion")
		}
	}
}

// TestOutOfScopeLocatorRejected pins P0 test 6 at the authorization layer: a
// locator recorded in the asset graph is a reference, never an authorization.
// Only the engagement fence decides whether the platform may act on it.
func TestOutOfScopeLocatorRejected(t *testing.T) {
	ag := &Agent{}
	ag.SetEngagementScope([]string{"10.0.0.0/24"}, nil, nil)

	if blocked, reason := ag.shouldBlockForOutOfScope("http_request", map[string]string{
		"url": "http://10.0.0.5:8080/admin", "method": "GET",
	}); blocked {
		t.Fatalf("in-scope locator rejected: %s", reason)
	}
	blocked, reason := ag.shouldBlockForOutOfScope("http_request", map[string]string{
		"url": "http://10.0.1.5:8080/admin", "method": "GET",
	})
	if !blocked {
		t.Fatal("out-of-scope locator was allowed")
	}
	if !strings.Contains(reason, "outside the engagement scope") {
		t.Fatalf("unexpected refusal: %s", reason)
	}
	// The same check covers command-class platform operations.
	if blocked, _ := ag.shouldBlockForOutOfScope("terminal_execute", map[string]string{
		"command": "curl -sS http://10.0.1.5:8080/admin",
	}); !blocked {
		t.Fatal("out-of-scope locator reached terminal_execute")
	}
}

// TestConcurrentGoalProjectionKeepsSingleRoot pins P0 test 8: concurrent
// projections of the same ledger/objective converge on one root Goal and one
// node per criterion, with no duplicated lineage.
func TestConcurrentGoalProjectionKeepsSingleRoot(t *testing.T) {
	ag, sctx, store := missionAgent(t, ObjectiveCriterion{
		ID: "C1", Kind: "finding_class", VulnClass: "sqli", VerifyBy: "verifier", Required: true,
	})
	addMissionIntent(t, store, sctx.InstanceID, "i-1", "GOAL")
	addMissionIntent(t, store, sctx.InstanceID, "i-2", "C1")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ag.syncExplorationGraph()
		}()
	}
	wg.Wait()

	goals, _ := store.ListNodes(context.Background(), sctx.InstanceID, graph.KindGoal)
	counts := map[string]int{}
	for _, g := range goals {
		counts[g.UnitKey]++
	}
	if counts["GOAL"] != 1 || counts["C1"] != 1 {
		t.Fatalf("goal nodes = %+v", counts)
	}
	edges, _ := store.ListEdges(context.Background(), sctx.InstanceID)
	spawns := 0
	for _, e := range edges {
		if e.Rel == graph.RelSpawns && e.DstID > 0 {
			spawns++
		}
	}
	// root->C1, root->i-1, C1->i-2. MemoryStore dedupes identical
	// (src, dst, rel) tuples, so concurrent projections must not grow this set.
	if spawns != 3 {
		t.Fatalf("spawns edges = %d, want 3: %+v", spawns, edges)
	}
}
