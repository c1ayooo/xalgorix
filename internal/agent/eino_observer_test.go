package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
)

func TestEngineObserverMatchesLegacyHookResults(t *testing.T) {
	sctx, state := newTestCtxState(t)
	a := NewAgent(&config.Config{MaxIterations: 10}, "eino-parity-test",
		make(chan Event, 64), scopeguard.Config{}, sctx)
	t.Cleanup(a.Stop)
	a.state = state
	a.hooks.Register(OnIterationStart, func(*ScanState, map[string]string) HookResult {
		return HookResult{Nudge: "PARITY NUDGE"}
	})
	a.hooks.Register(OnToolCall, func(*ScanState, map[string]string) HookResult {
		return HookResult{Block: true, BlockReason: "parallel-off"}
	})

	observer := a.EngineObserver().(*einoObserver)
	legacyTurn := a.hooks.Fire(OnIterationStart, state, nil)
	einoTurn := observer.BeforeTurn(context.Background(), 1)
	if legacyTurn.Nudge != einoTurn.Nudge {
		t.Fatalf("turn nudge diverged: legacy=%q eino=%q", legacyTurn.Nudge, einoTurn.Nudge)
	}

	args := map[string]string{"tool_name": "terminal_execute", "command": "id"}
	legacyTool := a.hooks.Fire(OnToolCall, state, args)
	einoTool := observer.BeforeTool(context.Background(), "terminal_execute", map[string]string{"command": "id"})
	if legacyTool.Block != (!einoTool.Allowed) || legacyTool.BlockReason != einoTool.Output {
		t.Fatalf("tool block diverged: legacy=%+v eino=%+v", legacyTool, einoTool)
	}
}

func TestEngineObserverBlocksFinishThroughLegacyGate(t *testing.T) {
	sctx, state := newTestCtxState(t)
	plan := NewPlan()
	plan.add(&Task{ID: "pending-test", Title: "Test authz", Phase: 6, Status: TaskPending})
	state.Plan = plan

	a := NewAgent(&config.Config{MaxIterations: 10}, "eino-observer-test",
		make(chan Event, 16), scopeguard.Config{}, sctx)
	t.Cleanup(a.Stop)
	a.state = state

	observer := a.EngineObserver()
	if observer == nil {
		t.Fatal("EngineObserver returned nil")
	}
	verdict := observer.BeforeTool(context.Background(), "finish", map[string]string{"summary": "done"})
	if verdict.Allowed {
		t.Fatalf("finish was allowed despite an open plan task: %+v", verdict)
	}
	if !strings.Contains(verdict.Output, "FINISH REJECTED") {
		t.Fatalf("finish rejection output = %q", verdict.Output)
	}
	if !state.FinishRecoveryPending {
		t.Fatal("finish rejection did not mark recovery pending")
	}
}
