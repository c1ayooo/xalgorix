package agent

import (
	"context"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
)

func TestPlannerWorkerFinishGate(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })
	ctx := context.Background()
	runID := "run-finish"
	state := NewScanState()
	state.ScanContextID = runID
	state.PlannerWorkerMode = true
	state.MaxFinishRejections = 15

	// Empty graph: finishing before any intent settles is refused.
	if gate := hookFinishGatekeeper(state, nil); !gate.Block {
		t.Fatal("finish must be blocked before any intent exists")
	}

	if _, err := store.UpsertNode(ctx, graph.NodeInput{
		ScanID: runID, Kind: graph.KindIntent, UnitKey: "recon", State: graph.StateOpen,
		Priority: 990, Payload: `{"task_id":"recon","phase":1}`,
	}); err != nil {
		t.Fatalf("add intent: %v", err)
	}
	if gate := hookFinishGatekeeper(state, nil); !gate.Block || gate.BlockReason == "" {
		t.Fatalf("finish must be blocked by an open intent: %+v", gate)
	}
	claimed, err := store.ClaimNextIntent(ctx, runID, "work#1", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %+v", err, claimed)
	}
	if gate := hookFinishGatekeeper(state, nil); !gate.Block {
		t.Fatal("finish must be blocked while the intent is running")
	}
	if _, err := store.ConcludeIntent(ctx, runID, claimed.ID, "work#1", graph.StateDone, []graph.NodeInput{{
		ScanID: runID, Kind: graph.KindFact, State: graph.StateOrigin,
		UnitKey: "intent-result:recon", Origin: "worker:work#1", Payload: `{"status":"completed"}`,
	}}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if gate := hookFinishGatekeeper(state, nil); gate.Block {
		t.Fatalf("finish must be allowed after the frontier drains: %+v", gate)
	}
}
