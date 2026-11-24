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

	n, err := store.UpsertNode(ctx, graph.NodeInput{
		ScanID: runID, Kind: graph.KindIntent, UnitKey: "recon", State: graph.StateOpen,
		Priority: 990, Payload: `{"task_id":"recon","phase":1}`,
	})
	if err != nil {
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
	if ok, err := store.Transition(ctx, runID, n.ID, graph.KindIntent, graph.StateRunning, graph.StateDone); err != nil || !ok {
		t.Fatalf("settle: ok=%v err=%v", ok, err)
	}
	if gate := hookFinishGatekeeper(state, nil); gate.Block {
		t.Fatalf("finish must be allowed after the frontier drains: %+v", gate)
	}
}
