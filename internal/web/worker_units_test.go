package web

// worker_units_test.go — unit-keyed worker projection (R7/4zreco identity).
//
// The visible worker is the intent (work unit), not the agent instance: the
// unit's graph state is the row status, every dispatch that ran it is an
// execution detail, and a unit lane's transcript is the union of its attempts.

import (
	"context"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
)

func TestWorkerUnits_GraphStateIsPrimary(t *testing.T) {
	s := newTestServer(t, nil)
	store := graph.NewMemoryStore()
	withGraphBackend(t, store)
	ctx := context.Background()

	intent, err := store.AddNode(ctx, graph.NodeInput{
		ScanID: "run-1", Kind: graph.KindIntent, Priority: 900,
		Payload: `{"task_id":"t-1","title":"Fuzz the login form","phase":6,"vuln_class":"sqli"}`,
	})
	if err != nil {
		t.Fatalf("add intent: %v", err)
	}

	rows := []agentEntryView{{
		DelegatedAgent: agent.DelegatedAgent{
			AgentID: "sub_1", UnitID: "t-1", Lane: "login", Status: "completed",
			Started: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z",
		},
	}}
	units := s.workerUnits("run-1", rows)
	if len(units) != 1 {
		t.Fatalf("units = %d, want 1: %#v", len(units), units)
	}
	unit := units[0]
	// The intent is still open: the graph state wins over the finished agent,
	// so the row reads "queued" (not a completed worker) until the scheduler
	// concludes the unit.
	if unit.UnitID != "t-1" || unit.State != "open" || unit.Status != "queued" {
		t.Fatalf("unit = %#v, want t-1/open/queued", unit)
	}
	if unit.Title != "Fuzz the login form" {
		t.Fatalf("title = %q", unit.Title)
	}
	if len(unit.AgentIDs) != 1 || unit.AgentIDs[0] != "sub_1" || unit.AgentStatus != "completed" {
		t.Fatalf("agent detail = %#v", unit)
	}

	// Concluding the intent flips the visible row to completed. The graph
	// contract requires the conclusion transaction (done needs a product),
	// not a bare Transition.
	if _, err := store.ConcludePlanned(ctx, "run-1", intent.ID, graph.StateDone, []graph.NodeInput{{
		ScanID: "run-1", Kind: graph.KindFact, UnitKey: "intent-result:t-1", Payload: `{}`,
	}}); err != nil {
		t.Fatalf("conclude: %v", err)
	}
	units = s.workerUnits("run-1", rows)
	if len(units) != 1 || units[0].Status != "completed" {
		t.Fatalf("after done: %#v", units)
	}
}

func TestWorkerUnits_TerminalWithoutAgentsIsHidden(t *testing.T) {
	s := newTestServer(t, nil)
	store := graph.NewMemoryStore()
	withGraphBackend(t, store)
	ctx := context.Background()

	// Retired before dispatch (or a pre-0029 row): no agent instance ran it,
	// so there is no worker history to open. The Plan module keeps the intent.
	retired, err := store.AddNode(ctx, graph.NodeInput{
		ScanID: "run-5", Kind: graph.KindIntent, Priority: 700,
		Payload: `{"task_id":"t-5","title":"Retired lane"}`,
	})
	if err != nil {
		t.Fatalf("add intent: %v", err)
	}
	if _, err := store.ConcludePlanned(ctx, "run-5", retired.ID, graph.StateDone, []graph.NodeInput{{
		ScanID: "run-5", Kind: graph.KindFact, UnitKey: "intent-result:t-5", Payload: `{}`,
	}}); err != nil {
		t.Fatalf("conclude: %v", err)
	}
	// A queued unit with no agent is still visible (waiting for a worker).
	if _, err := store.AddNode(ctx, graph.NodeInput{
		ScanID: "run-5", Kind: graph.KindIntent, Priority: 600,
		Payload: `{"task_id":"t-6","title":"Queued lane"}`,
	}); err != nil {
		t.Fatalf("add queued intent: %v", err)
	}

	units := s.workerUnits("run-5", nil)
	if len(units) != 1 || units[0].UnitID != "t-6" || units[0].Status != "queued" {
		t.Fatalf("units = %#v, want only the queued lane", units)
	}
}

func TestWorkerUnits_AggregatesRetries(t *testing.T) {
	s := newTestServer(t, nil)
	store := graph.NewMemoryStore()
	withGraphBackend(t, store)
	ctx := context.Background()

	intent, err := store.AddNode(ctx, graph.NodeInput{
		ScanID: "run-2", Kind: graph.KindIntent, Priority: 800,
		Payload: `{"task_id":"t-2","title":"Retry probe"}`,
	})
	if err != nil {
		t.Fatalf("add intent: %v", err)
	}
	if _, err := store.ClaimNextIntent(ctx, "run-2", "work#1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	_ = intent

	rows := []agentEntryView{
		{DelegatedAgent: agent.DelegatedAgent{AgentID: "sub_1", UnitID: "t-2", Status: "failed", Error: "boom"}},
		{DelegatedAgent: agent.DelegatedAgent{AgentID: "sub_2", UnitID: "t-2", Status: "running", Started: "2026-01-01T00:00:00Z"}},
	}
	units := s.workerUnits("run-2", rows)
	if len(units) != 1 {
		t.Fatalf("retries must collapse into one unit, got %#v", units)
	}
	unit := units[0]
	if len(unit.AgentIDs) != 2 || unit.AgentIDs[0] != "sub_1" || unit.AgentIDs[1] != "sub_2" {
		t.Fatalf("agent ids = %#v", unit.AgentIDs)
	}
	if unit.AgentStatus != "running" {
		t.Fatalf("agent status = %q, want running", unit.AgentStatus)
	}
	if unit.Status != "running" {
		t.Fatalf("unit status = %q, want running (claimed intent)", unit.Status)
	}
}

func TestUnitTranscriptItems_UnionOfAttempts(t *testing.T) {
	items := []transcriptItem{
		{Seq: 1, AgentID: "sub_1", Text: "attempt one"},
		{Seq: 2, AgentID: "root_agent", Text: "main turn"},
		{Seq: 3, AgentID: "run-1/sub_2", Text: "attempt two"},
		{Seq: 4, AgentID: "sub_3", Text: "other unit"},
	}
	got := unitTranscriptItems(items, []string{"sub_1", "sub_2"})
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2: %#v", len(got), got)
	}
	if got[0].Text != "attempt one" || got[1].Text != "attempt two" {
		t.Fatalf("rows = %#v", got)
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("seq must renumber contiguously: %#v", got)
	}
	if empty := unitTranscriptItems(items, nil); len(empty) != 0 {
		t.Fatalf("unknown unit must not leak rows: %#v", empty)
	}
}

func TestUnitAgentIDs_UnionsDurableRows(t *testing.T) {
	s := newTestServer(t, nil)
	fake := useFakeWorkerBackend(t)
	for _, rec := range []runstate.WorkerRecord{
		{InstanceID: "run-3", ScanID: "scan-3", AgentID: "sub_1", UnitID: "t-3", Status: "failed"},
		{InstanceID: "run-3", ScanID: "scan-3", AgentID: "sub_2", UnitID: "t-3", Status: "running"},
		{InstanceID: "run-3", ScanID: "scan-3", AgentID: "sub_9", UnitID: "t-9", Status: "running"},
	} {
		if err := fake.UpsertWorker(context.Background(), rec); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	ids := s.unitAgentIDs(context.Background(), "run-3", "t-3")
	if len(ids) != 2 {
		t.Fatalf("ids = %#v, want both attempts", ids)
	}
	seen := map[string]bool{ids[0]: true, ids[1]: true}
	if !seen["sub_1"] || !seen["sub_2"] {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestPersistWorkerLifecycle_CarriesUnitID(t *testing.T) {
	s := newTestServer(t, nil)
	fake := useFakeWorkerBackend(t)
	sess := &scanSession{id: "scan-4", instanceID: "run-4"}

	s.persistWorkerLifecycle(agent.Event{
		Type: "agent_started",
		Meta: map[string]any{
			"agent_id": "sub_1", "unit_id": "t-4", "lane": "login", "status": "running",
		},
	}, sess)
	rec := waitForWorkerRow(t, fake, "run-4", "sub_1", "running")
	if rec.UnitID != "t-4" {
		t.Fatalf("unit_id = %q, want t-4 (%#v)", rec.UnitID, rec)
	}
}

func TestWorkerUnits_NewestWorkFirst(t *testing.T) {
	s := newTestServer(t, nil)
	store := graph.NewMemoryStore()
	withGraphBackend(t, store)
	ctx := context.Background()

	if _, err := store.AddNode(ctx, graph.NodeInput{
		ScanID: "run-order", Kind: graph.KindIntent, Priority: 100,
		Payload: `{"task_id":"old","title":"Old lane"}`,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := store.AddNode(ctx, graph.NodeInput{
		ScanID: "run-order", Kind: graph.KindIntent, Priority: 100,
		Payload: `{"task_id":"new","title":"New lane"}`,
	}); err != nil {
		t.Fatal(err)
	}

	units := s.workerUnits("run-order", nil)
	if len(units) != 2 {
		t.Fatalf("units = %#v", units)
	}
	if units[0].UnitID != "new" {
		t.Fatalf("newest work must be first, got %#v", units)
	}
}
