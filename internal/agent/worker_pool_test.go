package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/tools/agentsgraph"
)

// blockingGraph returns a pool whose single worker blocks until its context is
// cancelled, plus a started channel that closes when the runner begins.
func blockingGraph(t *testing.T) (*agentsgraph.Graph, <-chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	graph := agentsgraph.NewWithConfig(context.Background(), 4, 2, func(ctx context.Context, agentID, name string, targets []string, task string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	t.Cleanup(func() { graph.Stop() })
	return graph, started
}

func TestKillWorkerWithReasonStagnantValidation(t *testing.T) {
	graph, started := blockingGraph(t)
	sctx := progressTestContext(t, "kill-reason")
	ag := &Agent{agentGraph: graph, scanCtx: sctx}

	agentID, err := ag.DispatchWorkerTaskWithSurface("work#1", "do it", []string{"example.com"}, "unit-1", "")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-started

	// The dispatch path binds the unit to the delegated agent id, so the kill
	// check can read the entry the progress hook writes.
	if bound, ok := sctx.Progress.UnitAgent("unit-1"); !ok || bound != agentID {
		t.Fatalf("unit binding = %q ok=%v, want %q", bound, ok, agentID)
	}

	// Fresh information-layer progress blocks a stagnant kill.
	sctx.Progress.MarkProgress(agentID)
	if err := ag.KillWorkerWithReason("unit-1", "stagnant", "no new evidence"); err == nil {
		t.Fatal("stagnant kill must be refused while progress is recent")
	} else if !strings.Contains(err.Error(), "progress") {
		t.Fatalf("stagnant refusal = %v", err)
	}

	// An unknown reason code is rejected outright.
	if err := ag.KillWorkerWithReason("unit-1", "bored", "x"); err == nil || !strings.Contains(err.Error(), "reason_code") {
		t.Fatalf("unknown reason code = %v", err)
	}

	// A non-stagnant code is allowed and cancels the worker.
	if err := ag.KillWorkerWithReason("unit-1", "duplicate", "same target as unit-2"); err != nil {
		t.Fatalf("duplicate kill: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, status, _ := ag.WaitWorkerResult(ctx, agentID); status == "running" || status == "queued" {
		t.Fatalf("worker still %s after kill", status)
	}

	// A stagnant kill is allowed when the worker never made progress.
	graph2, started2 := blockingGraph(t)
	ag2 := &Agent{agentGraph: graph2, scanCtx: sctx}
	if _, err := ag2.DispatchWorkerTaskWithSurface("work#2", "do it", nil, "unit-2", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-started2
	if err := ag2.KillWorkerWithReason("unit-2", "stagnant", "never produced evidence"); err != nil {
		t.Fatalf("stagnant kill without progress must be allowed: %v", err)
	}
}

func TestKillWorkerDefaultsToHuman(t *testing.T) {
	graph, started := blockingGraph(t)
	sctx := progressTestContext(t, "kill-human")
	ag := &Agent{agentGraph: graph, scanCtx: sctx}
	if _, err := ag.DispatchWorkerTaskWithSurface("work#1", "do it", nil, "unit-h", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-started
	// KillWorker is the human path; the empty code also defaults to human.
	if err := ag.KillWorker("unit-h", "operator asked"); err != nil {
		t.Fatalf("KillWorker: %v", err)
	}
}

func TestKillWorkerWithReasonRequiresUnit(t *testing.T) {
	graph, _ := blockingGraph(t)
	ag := &Agent{agentGraph: graph, scanCtx: progressTestContext(t, "kill-empty")}
	if err := ag.KillWorkerWithReason("  ", "human", "x"); err == nil {
		t.Fatal("empty unit id must be rejected")
	}
}
