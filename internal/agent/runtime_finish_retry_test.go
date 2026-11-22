package agent

// Runtime lifecycle tests for the finish-retry semantics of the stop probe.
// Companion to the single-runtime golden test and verifier timing tests.

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/tools"
)

func eventTypesOf(evs []Event) []string {
	out := make([]string, 0, len(evs))
	for i := range evs {
		out = append(out, evs[i].Type)
	}
	return out
}

// TestEinoFinishAcceptedThenToolFailureAllowsRetry pins the LOW fix: a finish
// whose GATE accepts but whose TOOL EXECUTION fails must not end the scan.
// completeWithFinish never ran (callErr != nil), so the stop probe is disarmed
// in AfterTool and the next model turn — a finish retry — is allowed. Legacy
// semantics are preserved upstream of this: the failed execution reaches the
// model as a tool result ("⛔ TOOL EXECUTION FAILED — …"), not as a
// graph-killing error.
func TestEinoFinishAcceptedThenToolFailureAllowsRetry(t *testing.T) {
	vc := newVerifierChainServer(t)
	vc.nextMainResponse = func(mainTurn int) map[string]any {
		switch mainTurn {
		case 1, 2:
			// Filler bookkeeping turns so the first finish attempt lands at
			// turn 3, past the finish gate's iteration floor (iter < 3).
			return goldenScriptedToolCall(mainTurn, "read_ledger", map[string]string{"filter": "schedulable"})
		default:
			return goldenScriptedToolCall(mainTurn, "finish", map[string]string{"summary": "finish retry probe complete"})
		}
	}
	vc.nextVerifierTurn = func(int) string { return "" } // no verifier turns in this scenario

	ag, _, events := newEinoVerifierAgent(t, vc.URL)

	// One-shot failing finish: the first execution explodes, the retry
	// succeeds. Register overwrites by name, so this swaps the Execute behind
	// the already-constructed runtime's lazy registry lookup.
	var failArmed atomic.Bool
	failArmed.Store(true)
	ag.registry.Register(&tools.Tool{
		Name:        "finish",
		Description: "Signal that the agent has completed its task. Provide a summary of findings.",
		Parameters: []tools.Parameter{
			{Name: "summary", Description: "Summary of what was done and key findings", Required: true},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			if failArmed.CompareAndSwap(true, false) {
				return tools.Result{}, errors.New("finish backend exploded once")
			}
			return tools.Result{Output: args["summary"], Metadata: map[string]any{"finished": true}}, nil
		},
	})

	drained := collectAgentEventsAsync(events)
	ag.Run([]string{"https://target.example"}, "ctf drill: capture the flag on the isolated lab host")
	close(events)
	evs := <-drained

	const wantTurns = 4 // two fillers + the failed finish + the successful retry
	if got := vc.mainTurns; got != wantTurns {
		t.Fatalf("model turns = %d, want %d — the failed finish must cost exactly one retry turn, not the scan (order=%v events=%v)",
			got, wantTurns, vc.order(), eventTypesOf(evs))
	}
	var finished *Event
	finishToolCalls := 0
	for i := range evs {
		switch evs[i].Type {
		case "tool_call":
			if evs[i].ToolName == "finish" {
				finishToolCalls++
			}
		case "finished":
			finished = &evs[i]
		}
	}
	if finishToolCalls != 2 {
		t.Fatalf("finish tool_call events = %d, want 2 (the failed attempt plus the successful retry)", finishToolCalls)
	}
	if finished == nil {
		t.Fatalf("no finished event emitted; events=%v", eventTypesOf(evs))
	}
	if finished.Aborted {
		t.Fatalf("finished event aborted with content %q — a failed finish execution must not end the scan as aborted", finished.Content)
	}
	if !strings.Contains(finished.Content, "finish retry probe complete") {
		t.Fatalf("terminal summary does not carry the retried finish output: %s", finished.Content)
	}
}
