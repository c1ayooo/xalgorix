package graph

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func integrationPG(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	store, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open graph store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func scanID(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("graph-test-%d", time.Now().UnixNano())
}

func cleanupScan(t *testing.T, store *PGStore, scanID string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM exploration_nodes WHERE scan_id = $1`, scanID)
	})
}

func mustAddIntent(t *testing.T, store Repository, scanID, taskID string, priority int, payload string) *Node {
	t.Helper()
	n, err := store.AddNode(context.Background(), NodeInput{
		ScanID:   scanID,
		Kind:     KindIntent,
		Priority: priority,
		Payload:  payload,
	})
	if err != nil {
		t.Fatalf("add intent %s: %v", taskID, err)
	}
	return n
}

func TestPGStore_ClaimIsExclusiveAndOrdered(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	sid := scanID(t)
	cleanupScan(t, store, sid)

	low := mustAddIntent(t, store, sid, "phase-10", 10, `{"task_id":"phase-10","phase":10,"title":"low"}`)
	high := mustAddIntent(t, store, sid, "phase-2", 100, `{"task_id":"phase-2","phase":2,"title":"high"}`)
	mid := mustAddIntent(t, store, sid, "phase-5", 50, `{"task_id":"phase-5","phase":5,"title":"mid"}`)

	first, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute)
	if err != nil {
		t.Fatalf("claim first: %v", err)
	}
	if first == nil || first.ID != high.ID {
		t.Fatalf("first claim = %+v, want highest priority %d", first, high.ID)
	}
	if first.State != StateRunning || first.Owner != "work#1" || first.LeaseUntil == nil {
		t.Fatalf("claim did not set running/owner/lease: %+v", first)
	}

	second, err := store.ClaimNextIntent(ctx, sid, "work#2", time.Minute)
	if err != nil {
		t.Fatalf("claim second: %v", err)
	}
	if second == nil || second.ID != mid.ID {
		t.Fatalf("second claim = %+v, want %d", second, mid.ID)
	}
	if second.ID == first.ID {
		t.Fatal("two workers claimed the same intent")
	}

	third, err := store.ClaimNextIntent(ctx, sid, "work#3", time.Minute)
	if err != nil {
		t.Fatalf("claim third: %v", err)
	}
	if third == nil || third.ID != low.ID {
		t.Fatalf("third claim = %+v, want %d", third, low.ID)
	}

	none, err := store.ClaimNextIntent(ctx, sid, "work#4", time.Minute)
	if err != nil {
		t.Fatalf("claim none: %v", err)
	}
	if none != nil {
		t.Fatalf("expected no claimable intents, got %+v", none)
	}
}

func TestPGStore_LeaseReclaimAndReset(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	sid := scanID(t)
	cleanupScan(t, store, sid)

	n := mustAddIntent(t, store, sid, "lease", 10, `{"task_id":"lease","phase":1}`)
	claimed, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %+v", err, claimed)
	}
	if ok, err := store.RenewLease(ctx, sid, claimed.ID, "work#1", time.Minute); err != nil || !ok {
		t.Fatalf("renew: ok=%v err=%v", ok, err)
	}
	if ok, err := store.RenewLease(ctx, sid, claimed.ID, "wrong-owner", time.Minute); err != nil || ok {
		t.Fatalf("renew with wrong owner: ok=%v err=%v", ok, err)
	}

	// Force the lease into the past and reclaim.
	if _, err := store.pool.Exec(ctx, `UPDATE exploration_nodes SET lease_until = now() - interval '1 minute' WHERE id = $1`, claimed.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	reclaimed, err := store.ReclaimExpired(ctx, sid)
	if err != nil || reclaimed != 1 {
		t.Fatalf("reclaim = %d, err=%v; want 1", reclaimed, err)
	}
	got, err := store.GetNode(ctx, sid, n.ID)
	if err != nil {
		t.Fatalf("get reclaimed: %v", err)
	}
	if got.State != StateOpen || got.Owner != "" || got.LeaseUntil != nil {
		t.Fatalf("reclaimed node = %+v", got)
	}

	// Re-claim, then simulate a crash: ResetRunning must release it.
	if _, err := store.ClaimNextIntent(ctx, sid, "work#2", time.Minute); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	reset, err := store.ResetRunning(ctx, sid)
	if err != nil || reset != 1 {
		t.Fatalf("reset = %d, err=%v; want 1", reset, err)
	}
	got, _ = store.GetNode(ctx, sid, n.ID)
	if got.State != StateOpen || got.Owner != "" {
		t.Fatalf("after reset = %+v", got)
	}
}

func TestPGStore_DependenciesBlockUntilParentTerminal(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	sid := scanID(t)
	cleanupScan(t, store, sid)

	parent := mustAddIntent(t, store, sid, "parent", 100, `{"task_id":"parent","phase":1}`)
	child := mustAddIntent(t, store, sid, "child", 90, `{"task_id":"child","phase":2}`)
	if err := store.AddEdge(ctx, EdgeInput{ScanID: sid, SrcID: parent.ID, DstID: child.ID, Rel: RelSpawns}); err != nil {
		t.Fatalf("add edge: %v", err)
	}

	first, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute)
	if err != nil || first == nil || first.ID != parent.ID {
		t.Fatalf("first claim = %+v, err=%v; want parent %d", first, err, parent.ID)
	}
	next, err := store.ClaimNextIntent(ctx, sid, "work#2", time.Minute)
	if err != nil {
		t.Fatalf("claim child before parent done: %v", err)
	}
	if next != nil {
		t.Fatalf("child claimed before parent terminal: %+v", next)
	}
	if _, err := store.ConcludeIntent(ctx, sid, parent.ID, "work#1", StateDone, []NodeInput{{
		ScanID: sid, Kind: KindFact, State: StateOrigin,
		UnitKey: "intent-result:lease-parent", Payload: `{"summary":"parent settled","task_id":"parent"}`,
	}}); err != nil {
		t.Fatalf("parent done via ConcludeIntent: %v", err)
	}
	claimedChild, err := store.ClaimNextIntent(ctx, sid, "work#2", time.Minute)
	if err != nil || claimedChild == nil || claimedChild.ID != child.ID {
		t.Fatalf("child claim after parent done = %+v, err=%v", claimedChild, err)
	}
}

func TestPGStore_PlanTaskAndUnitViews(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	sid := scanID(t)
	cleanupScan(t, store, sid)

	_ = mustAddIntent(t, store, sid, "test-sqli", 100, `{
		"task_id":"test-sqli","title":"Test /api/users for SQLi","phase":6,
		"vuln_class":"sqli","endpoint":"/api/users","origin":"llm",
		"depends_on":["recon"],"notes":"grounded","prerequisite":false,
		"whole_target":false,"objective_ref":"criterion-1"}`)
	belief, err := store.AddNode(ctx, NodeInput{
		ScanID: sid, Kind: KindBelief, Priority: 50,
		Payload: `{"hypothesis_id":"H-1","subject":"sqli @ /api/users","vuln_class":"sqli","phase":6}`,
	})
	if err != nil {
		t.Fatalf("add belief: %v", err)
	}

	tasks, err := store.PlanTasks(ctx, sid)
	if err != nil {
		t.Fatalf("plan tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("plan tasks = %d, want 1", len(tasks))
	}
	got := tasks[0]
	if got.TaskID != "test-sqli" || got.Title == "" || got.Phase != 6 ||
		got.VulnClass != "sqli" || got.Status != "pending" ||
		got.Origin != "llm" || got.ObjectiveRef != "criterion-1" {
		t.Fatalf("plan task projection = %+v", got)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "recon" {
		t.Fatalf("depends_on = %#v", got.DependsOn)
	}

	if _, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	tasks, _ = store.PlanTasks(ctx, sid)
	if tasks[0].Status != "active" || tasks[0].Owner != "work#1" {
		t.Fatalf("active task projection = %+v", tasks[0])
	}

	units, err := store.Units(ctx, sid)
	if err != nil {
		t.Fatalf("units: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("units = %d, want 2", len(units))
	}
	var sawIntent, sawBelief bool
	for _, u := range units {
		switch u.Kind {
		case "intent":
			sawIntent = true
			if u.UnitID != "test-sqli" || u.Subject != "endpoint:/api/users" || u.State != "running" {
				t.Fatalf("intent unit = %+v", u)
			}
		case "belief":
			sawBelief = true
			if u.UnitID != "H-1" || u.Subject == "" {
				t.Fatalf("belief unit = %+v", u)
			}
			if u.NodeID != belief.ID {
				t.Fatalf("belief node id = %d, want %d", u.NodeID, belief.ID)
			}
		}
	}
	if !sawIntent || !sawBelief {
		t.Fatalf("missing unit kinds: intent=%v belief=%v", sawIntent, sawBelief)
	}
}

func TestPGStore_Counts(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	sid := scanID(t)
	cleanupScan(t, store, sid)

	mustAddIntent(t, store, sid, "a", 10, `{"task_id":"a","phase":1}`)
	mustAddIntent(t, store, sid, "b", 10, `{"task_id":"b","phase":1}`)
	if _, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.AddNode(ctx, NodeInput{ScanID: sid, Kind: KindBelief, Payload: `{"hypothesis_id":"H-1"}`}); err != nil {
		t.Fatalf("add belief: %v", err)
	}
	c, err := store.Counts(ctx, sid)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if c.OpenIntents != 1 || c.RunningIntents != 1 || c.QueuedBeliefs != 1 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestMemoryStore_ClaimAndReset(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sid := "mem"
	parent, _ := store.AddNode(ctx, NodeInput{ScanID: sid, Kind: KindIntent, Priority: 100, Payload: `{"task_id":"parent","phase":1}`})
	child, _ := store.AddNode(ctx, NodeInput{ScanID: sid, Kind: KindIntent, Priority: 90, Payload: `{"task_id":"child","phase":2}`})
	if err := store.AddEdge(ctx, EdgeInput{ScanID: sid, SrcID: parent.ID, DstID: child.ID, Rel: RelSpawns}); err != nil {
		t.Fatalf("edge: %v", err)
	}
	first, _ := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute)
	if first == nil || first.ID != parent.ID {
		t.Fatalf("first = %+v, want parent", first)
	}
	if next, _ := store.ClaimNextIntent(ctx, sid, "work#2", time.Minute); next != nil {
		t.Fatalf("child claimed before parent terminal: %+v", next)
	}
	if _, err := store.ConcludeIntent(ctx, sid, parent.ID, "work#1", StateDone, []NodeInput{{
		ScanID: sid, Kind: KindFact, State: StateOrigin,
		UnitKey: "intent-result:mem-parent", Payload: `{"summary":"parent settled","task_id":"parent"}`,
	}}); err != nil {
		t.Fatalf("parent ConcludeIntent: %v", err)
	}
	got, _ := store.ClaimNextIntent(ctx, sid, "work#2", time.Minute)
	if got == nil || got.ID != child.ID {
		t.Fatalf("child = %+v", got)
	}
	if n, _ := store.ResetRunning(ctx, sid); n != 1 {
		t.Fatalf("reset = %d, want 1", n)
	}
	if got, _ := store.GetNode(ctx, sid, child.ID); got.State != StateOpen {
		t.Fatalf("child after reset = %+v", got)
	}
}

// TestTransitionRefusesIntentDone pins the structural guarantee behind the
// INFO_GRAPH_CONTRACT red line: done is reachable only through ConcludeIntent
// (or ConcludePlanned), never a bare CAS transition.
func TestTransitionRefusesIntentDone(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sid := "mem-done-guard"
	n, _ := store.AddNode(ctx, NodeInput{ScanID: sid, Kind: KindIntent, Priority: 100, Payload: `{"task_id":"guard","phase":1}`})
	claimed, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute)
	if err != nil || claimed == nil || claimed.ID != n.ID {
		t.Fatalf("claim: %v %+v", err, claimed)
	}
	if _, err := store.Transition(ctx, sid, n.ID, KindIntent, StateRunning, StateDone); err == nil {
		t.Fatal("intent -> done must be structurally refused by Transition")
	}
	// Settling to a non-done terminal via CAS stays legal.
	if ok, err := store.Transition(ctx, sid, n.ID, KindIntent, StateRunning, StateBlocked); err != nil || !ok {
		t.Fatalf("blocked transition: ok=%v err=%v", ok, err)
	}
}
