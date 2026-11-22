package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
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

// drainEvents empties the agent's event channel so blocking tool_call /
// tool_result sends can never wedge a test.
func drainEvents(a *Agent) {
	for {
		select {
		case <-a.events:
		default:
			return
		}
	}
}

// newGateTestAgent wires a fresh observer test agent whose event channel is
// drained on cleanup.
func newGateTestAgent(t *testing.T, name string) (*Agent, *ScanState) {
	t.Helper()
	sctx, state := newTestCtxState(t)
	a := NewAgent(&config.Config{MaxIterations: 10}, name,
		make(chan Event, 64), scopeguard.Config{}, sctx)
	t.Cleanup(a.Stop)
	a.state = state
	return a, state
}

// TestEngineObserverBeforeToolMirrorsLegacyBlockGates asserts each block gate
// fires in the eino path with the exact legacy tool-result text: the dispatch
// loop's prefix plus the same guard reason the legacy loop would compose
// (agent.go activity → phase → auth → out-of-scope → target scope).
func TestEngineObserverBeforeToolMirrorsLegacyBlockGates(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		tool   string
		args   map[string]string
		wire   func(a *Agent, state *ScanState)
	}{
		{
			name:   "activity_policy",
			prefix: "⛔ ACTIVITY POLICY BLOCKED TOOL — ",
			tool:   "browser_action",
			args:   map[string]string{"action": "goto", "url": "https://target.example/"},
			wire: func(a *Agent, _ *ScanState) {
				a.scanIntensity = activityModePassive
			},
		},
		{
			name:   "phase_restriction",
			prefix: "⛔ PHASE RESTRICTION BLOCKED TOOL — ",
			tool:   "report_vulnerability",
			args:   map[string]string{"title": "recon finding"},
			wire: func(a *Agent, _ *ScanState) {
				a.allowedPhases = []int{1, 22}
			},
		},
		{
			name:   "auth_prerequisites",
			prefix: "⛔ AUTH PREREQUISITE GUARD BLOCKED TOOL — ",
			tool:   "terminal_execute",
			args:   map[string]string{"command": "hashcat -m 0 hashes.txt rockyou.txt"},
			wire: func(_ *Agent, state *ScanState) {
				state.AuthContextKnown = true
				state.AuthContextAvailable = false
			},
		},
		{
			name:   "out_of_scope",
			prefix: "⛔ OUT-OF-SCOPE TARGET BLOCKED — ",
			tool:   "terminal_execute",
			args:   map[string]string{"command": "curl -s http://127.0.0.1:9137/api/scans"},
			wire:   func(*Agent, *ScanState) {},
		},
		{
			name:   "target_scope",
			prefix: "⛔ OUTSIDE SCAN BOUNDARY — ",
			tool:   "http_request",
			args:   map[string]string{"url": "https://cdn.unrelated.org/api"},
			wire: func(a *Agent, _ *ScanState) {
				a.singleHostScope = true
				a.targets = []string{"https://apps.example.com"}
				// The out-of-scope gate runs first and would otherwise resolve
				// the fixture host; an unresolvable public name is not local,
				// so failing DNS keeps the path deterministic and lets the
				// lexical target-scope boundary do the blocking.
				withScopeguardLookupHost(t, func(string) ([]string, error) {
					return nil, fmt.Errorf("no DNS in gate alignment test")
				})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, state := newGateTestAgent(t, "eino-gate-"+tc.name)
			tc.wire(a, state)
			observer := a.EngineObserver()

			// The legacy dispatch loop composes prefix + the same guard's
			// reason; on the first block noteBlockedToolCall adds nothing.
			var reason string
			switch tc.name {
			case "activity_policy":
				_, reason = a.shouldBlockForActivityPolicy(tc.tool, tc.args)
			case "phase_restriction":
				_, reason = a.shouldBlockForPhaseRestriction(tc.tool, tc.args)
			case "auth_prerequisites":
				_, reason = a.shouldBlockForMissingAuthPrerequisites(tc.tool, tc.args)
			case "out_of_scope":
				_, reason = a.shouldBlockForOutOfScope(tc.tool, tc.args)
			case "target_scope":
				_, reason = a.shouldBlockForTargetScope(tc.tool, tc.args)
			}
			if reason == "" {
				t.Fatal("fixture must select a guard reason; gate did not fire")
			}
			want := tc.prefix + reason

			drainEvents(a)
			verdict := observer.BeforeTool(context.Background(), tc.tool, tc.args)
			if verdict.Allowed {
				t.Fatalf("expected %s to block, verdict=%+v", tc.name, verdict)
			}
			if verdict.Output != want {
				t.Fatalf("block text diverged from legacy:\n want=%q\n  got=%q", want, verdict.Output)
			}
			if state.ConsecutiveBlockedCalls != 1 {
				t.Fatalf("blocked call was not recorded: ConsecutiveBlockedCalls=%d", state.ConsecutiveBlockedCalls)
			}
			// The denial reaches the model through both the ADK tool result
			// (verdict.Output) and the emitted tool_call/tool_result events.
			var sawCall, sawResult bool
			for {
				select {
				case ev := <-a.events:
					switch ev.Type {
					case "tool_call":
						sawCall = ev.ToolName == tc.tool
					case "tool_result":
						sawResult = ev.ToolName == tc.tool && ev.ToolResult.Output == want
					}
				default:
					if !sawCall || !sawResult {
						t.Fatalf("expected blocked tool_call+tool_result events, got call=%v result=%v", sawCall, sawResult)
					}
					return
				}
			}
		})
	}
}

// TestEngineObserverBeforeToolPreservesLegacyGateOrder pins the dispatch
// order: when several guards could reject the same call, the message names
// the guard legacy would report first.
func TestEngineObserverBeforeToolPreservesLegacyGateOrder(t *testing.T) {
	t.Run("activity_before_out_of_scope", func(t *testing.T) {
		a, _ := newGateTestAgent(t, "eino-order-activity")
		a.scanIntensity = activityModePassive
		verdict := a.EngineObserver().BeforeTool(context.Background(), "browser_action",
			map[string]string{"action": "goto", "url": "http://127.0.0.1:9137/"})
		if verdict.Allowed || !strings.Contains(verdict.Output, "ACTIVITY POLICY") {
			t.Fatalf("expected activity policy to win over out-of-scope, got %+v", verdict)
		}
		if strings.Contains(verdict.Output, "OUT-OF-SCOPE") {
			t.Fatalf("out-of-scope must not preempt activity policy: %q", verdict.Output)
		}
	})
	t.Run("auth_before_out_of_scope", func(t *testing.T) {
		a, state := newGateTestAgent(t, "eino-order-auth")
		state.AuthContextKnown = true
		state.AuthContextAvailable = false
		verdict := a.EngineObserver().BeforeTool(context.Background(), "terminal_execute",
			map[string]string{"command": "hashcat -m 0 hashes.txt rockyou.txt # http://127.0.0.1:9137/x"})
		if verdict.Allowed || !strings.Contains(verdict.Output, "AUTH PREREQUISITE") {
			t.Fatalf("expected auth prerequisite to win over out-of-scope, got %+v", verdict)
		}
		if strings.Contains(verdict.Output, "OUT-OF-SCOPE") {
			t.Fatalf("out-of-scope must not preempt auth prerequisites: %q", verdict.Output)
		}
	})
	t.Run("out_of_scope_before_target_scope", func(t *testing.T) {
		a, _ := newGateTestAgent(t, "eino-order-scope")
		a.singleHostScope = true
		a.targets = []string{"https://apps.example.com"}
		verdict := a.EngineObserver().BeforeTool(context.Background(), "http_request",
			map[string]string{"url": "http://127.0.0.1:9137/api"})
		if verdict.Allowed || !strings.Contains(verdict.Output, "OUT-OF-SCOPE") {
			t.Fatalf("expected out-of-scope to win over target boundary, got %+v", verdict)
		}
	})
}

// TestEngineObserverAfterToolCarriesMetadata pins the tool_result metadata
// passthrough: the web layer gates the live report_vulnerability persist on
// ToolResult.Metadata["vuln_id"], so a dropped map strands accepted findings
// in the reporting store until session teardown.
func TestEngineObserverAfterToolCarriesMetadata(t *testing.T) {
	a, _ := newGateTestAgent(t, "eino-metadata")
	observer := a.EngineObserver()

	drainEvents(a)
	metadata := map[string]any{"vuln_id": "XALG-1", "upgraded": true}
	observer.AfterTool(context.Background(), "report_vulnerability",
		map[string]string{"title": "test finding"}, "reported", metadata, nil)

	var result *tools.Result
	for {
		select {
		case ev := <-a.events:
			if ev.Type == "tool_result" && ev.ToolName == "report_vulnerability" {
				r := ev.ToolResult
				result = &r
			}
		default:
			if result == nil {
				t.Fatal("no tool_result event emitted for report_vulnerability")
			}
			if got, _ := result.Metadata["vuln_id"].(string); got != "XALG-1" {
				t.Fatalf("tool_result metadata lost vuln_id: got %q", got)
			}
			if upgraded, _ := result.Metadata["upgraded"].(bool); !upgraded {
				t.Fatal("tool_result metadata lost upgraded flag")
			}
			return
		}
	}
}

// TestEngineObserverBlockedCallEscalationMatchesLegacy pins the escalating
// blocked-call note (noteBlockedToolCall) and the reset-on-allowed rule the
// legacy loop applies after every guard accepts a call.
func TestEngineObserverBlockedCallEscalationMatchesLegacy(t *testing.T) {
	a, state := newGateTestAgent(t, "eino-escalation")
	a.scanIntensity = activityModePassive
	observer := a.EngineObserver()
	blockedArgs := map[string]string{"action": "goto", "url": "https://target.example/"}

	for i := 1; i <= BlockedCallSoftNudge; i++ {
		drainEvents(a)
		verdict := observer.BeforeTool(context.Background(), "browser_action", blockedArgs)
		if verdict.Allowed {
			t.Fatalf("call %d unexpectedly allowed", i)
		}
		if i < BlockedCallSoftNudge && strings.Contains(verdict.Output, "⚠️") {
			t.Fatalf("escalation fired before the soft threshold at call %d: %q", i, verdict.Output)
		}
	}
	if state.ConsecutiveBlockedCalls != BlockedCallSoftNudge {
		t.Fatalf("expected %d consecutive blocks, got %d", BlockedCallSoftNudge, state.ConsecutiveBlockedCalls)
	}

	// An allowed call resets the counter exactly like the legacy loop.
	drainEvents(a)
	if verdict := observer.BeforeTool(context.Background(), "add_note",
		map[string]string{"key": "k", "value": "passive note"}); !verdict.Allowed {
		t.Fatalf("allowlisted tool was blocked in passive mode: %+v", verdict)
	}
	if state.ConsecutiveBlockedCalls != 0 {
		t.Fatalf("allowed call must reset the blocked counter, got %d", state.ConsecutiveBlockedCalls)
	}

	// The next block starts a fresh escalation window.
	drainEvents(a)
	verdict := observer.BeforeTool(context.Background(), "browser_action", blockedArgs)
	if verdict.Allowed || strings.Contains(verdict.Output, "⚠️") {
		t.Fatalf("escalation did not reset after an allowed call: %+v", verdict)
	}
}
