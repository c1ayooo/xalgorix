package agent

import (
	"context"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func testPlanGraph(t *testing.T) (*graph.MemoryStore, string) {
	t.Helper()
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })
	return store, "scan-graph-test"
}

func sampleGraphPlan() *Plan {
	plan := NewPlan()
	plan.add(&Task{
		ID: "recon", Title: "Recon", Phase: 1, Origin: "auto", Status: TaskPending,
	})
	plan.add(&Task{
		ID: "test-sqli", Title: "Test /api/users for SQLi", Phase: 6,
		VulnClass: "sqli", Endpoint: "/api/users", Origin: "llm",
		DependsOn: []string{"recon"}, Status: TaskPending,
	})
	return plan
}

func TestPlanGraphRoundTrip(t *testing.T) {
	store, sid := testPlanGraph(t)
	ctx := context.Background()
	plan := sampleGraphPlan()

	if err := persistPlanToGraph(ctx, store, sid, plan); err != nil {
		t.Fatalf("persist plan: %v", err)
	}
	loaded, ok := loadPlanFromGraph(ctx, store, sid)
	if !ok {
		t.Fatal("loadPlanFromGraph returned no plan")
	}
	if len(loaded.Tasks) != 2 {
		t.Fatalf("loaded tasks = %d, want 2", len(loaded.Tasks))
	}
	recon := loaded.Get("recon")
	if recon == nil || recon.Phase != 1 || recon.Status != TaskPending {
		t.Fatalf("recon = %+v", recon)
	}
	sqli := loaded.Get("test-sqli")
	if sqli == nil || sqli.VulnClass != "sqli" || sqli.Endpoint != "/api/users" ||
		sqli.Origin != "llm" || len(sqli.DependsOn) != 1 || sqli.DependsOn[0] != "recon" {
		t.Fatalf("sqli = %+v", sqli)
	}
	tasks, err := store.PlanTasks(ctx, sid)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("plan tasks = %d, err=%v", len(tasks), err)
	}
}

func TestPlanGraphClaimAndDependency(t *testing.T) {
	store, sid := testPlanGraph(t)
	ctx := context.Background()
	if err := persistPlanToGraph(ctx, store, sid, sampleGraphPlan()); err != nil {
		t.Fatalf("persist plan: %v", err)
	}
	sctx := scanctx.New(sid, "")
	ag := &Agent{state: NewScanState(), scanCtx: sctx}
	ag.state.ScanContextID = sid

	first, err := ag.claimNextTaskFromGraph()
	if err != nil {
		t.Fatalf("claim first: %v", err)
	}
	if first == nil || first.ID != "recon" || first.Status != TaskActive {
		t.Fatalf("first claim = %+v, want recon active", first)
	}
	if first.Owner != "root:"+sid {
		t.Fatalf("owner = %q, want root:%s", first.Owner, sid)
	}
	if local := ag.state.Plan.Get("recon"); local == nil || local.Status != TaskActive {
		t.Fatalf("local plan did not mirror claim: %+v", local)
	}

	// The child depends on recon, which is still running.
	if child, err := ag.claimNextTaskFromGraph(); err != nil || child != nil {
		t.Fatalf("child claimed before recon done: %+v err=%v", child, err)
	}

	// Settle recon through the same facade, then claim the child.
	first.Status = TaskCompleted
	ag.persistTaskToGraph(first)
	child, err := ag.claimNextTaskFromGraph()
	if err != nil {
		t.Fatalf("claim child: %v", err)
	}
	if child == nil || child.ID != "test-sqli" {
		t.Fatalf("child claim = %+v", child)
	}
	// Plan-driven completion goes through ConcludePlanned: the graph must be
	// conformant (recon done => yields receipt), not "done without yield".
	rep, err := graph.ValidateStore(ctx, store, sid)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !rep.OK {
		t.Fatalf("plan completion left graph drift: %+v", rep.Violations)
	}
}

func TestPlanGraphPersistCompletedTaskGetsReceipt(t *testing.T) {
	store, sid := testPlanGraph(t)
	ctx := context.Background()
	plan := NewPlan()
	plan.add(&Task{ID: "done-at-create", Title: "Already done", Phase: 1, Origin: "auto", Status: TaskCompleted})
	if err := persistPlanToGraph(ctx, store, sid, plan); err != nil {
		t.Fatalf("persist: %v", err)
	}
	nodes, err := store.ListNodes(ctx, sid, graph.KindFact)
	if err != nil || len(nodes) != 1 || nodes[0].UnitKey != "intent-result:done-at-create" {
		t.Fatalf("receipt facts = %+v err=%v", nodes, err)
	}
	rep, err := graph.ValidateStore(ctx, store, sid)
	if err != nil || !rep.OK {
		t.Fatalf("report = %+v err=%v", rep, err)
	}
}

func TestSyncPlanFromGraphState(t *testing.T) {
	store, sid := testPlanGraph(t)
	ctx := context.Background()
	if err := persistPlanToGraph(ctx, store, sid, sampleGraphPlan()); err != nil {
		t.Fatalf("persist plan: %v", err)
	}
	state := NewScanState()
	state.ScanContextID = sid
	syncPlanFromGraphState(state)
	if !state.PlanBuilt || state.Plan == nil || len(state.Plan.Tasks) != 2 {
		t.Fatalf("synced plan = %+v built=%v", state.Plan, state.PlanBuilt)
	}
	if state.Plan.Get("test-sqli") == nil {
		t.Fatal("synced plan missing test-sqli")
	}
}

func TestPlanGraphSoftDeletesRemovedTasks(t *testing.T) {
	store, sid := testPlanGraph(t)
	ctx := context.Background()
	if err := persistPlanToGraph(ctx, store, sid, sampleGraphPlan()); err != nil {
		t.Fatalf("persist plan: %v", err)
	}
	next := NewPlan()
	next.add(&Task{ID: "recon", Title: "Recon", Phase: 1, Origin: "auto", Status: TaskPending})
	if err := persistPlanToGraph(ctx, store, sid, next); err != nil {
		t.Fatalf("persist replacement: %v", err)
	}
	loaded, _ := loadPlanFromGraph(ctx, store, sid)
	if loaded == nil || len(loaded.Tasks) != 1 || loaded.Get("test-sqli") != nil {
		t.Fatalf("after replacement = %+v", loaded)
	}
	// The removed node is soft-deleted, not physically dropped.
	nodes, err := store.ListNodes(ctx, sid, graph.KindIntent)
	if err != nil {
		t.Fatalf("list intents: %v", err)
	}
	var deleted bool
	for _, n := range nodes {
		if n.State == graph.StateDeleted {
			deleted = true
		}
	}
	if !deleted {
		t.Fatalf("test-sqli was not soft-deleted: %+v", nodes)
	}
}
