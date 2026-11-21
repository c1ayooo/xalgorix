package agent

// Eino engine-gate lifecycle tests: the protocol-capability fail-closed gate
// and the finish-retry semantics of the stop probe. Companion to the R4 gates
// in engine_parity_golden_test.go and verifier_timing_eino_test.go.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/engine"
	"github.com/xalgord/xalgorix/v4/internal/llm"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

// TestEinoEngineFailsClosedOnNonNativeProtocol pins the protocol gate: the
// eino loop requires a native-tool-calling-capable endpoint (the ADK has no
// XML tool parser and the llm client attaches the tools array on the
// OpenAI-compatible branch only). An Anthropic-class provider must abort with
// eino_init_failed BEFORE any request is issued, with the legacy rollback
// named in the message — never a silent one-turn empty scan, never a
// fallback to legacy.
func TestEinoEngineFailsClosedOnNonNativeProtocol(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		LLM:           "anthropic/claude-3-test",
		APIBase:       srv.URL,
		APIKey:        "sk-test",
		MaxIterations: 5,
		Engine:        "eino",
	}
	sctx, state := newTestCtxState(t)
	events := make(chan Event, 64)
	store, err := engine.NewFileCheckpointStore(t.TempDir())
	if err != nil {
		t.Fatalf("checkpoint store: %v", err)
	}
	ag := NewAgent(cfg, "eino-fail-closed", events, scopeguard.Config{}, sctx,
		WithLLMClient(llm.NewClient(cfg)), WithEinoRuntime(store, nil))
	t.Cleanup(ag.Stop)
	ag.state = state

	collected := collectAgentEventsAsync(events)
	ag.Run([]string{"https://target.example"}, "ctf drill: capture the flag on the isolated lab host")
	close(events)
	evs := <-collected

	if got := requests.Load(); got != 0 {
		t.Fatalf("a non-native endpoint received %d requests; the gate must fail closed before any LLM call", got)
	}
	var finished *Event
	for i := range evs {
		if evs[i].Type == "finished" {
			finished = &evs[i]
		}
	}
	if finished == nil {
		t.Fatalf("no finished event emitted; events=%v", eventTypesOf(evs))
	}
	if !finished.Aborted || finished.AbortReason != "eino_init_failed" {
		t.Fatalf("finished event aborted=%v reason=%q, want aborted with eino_init_failed", finished.Aborted, finished.AbortReason)
	}
	for _, want := range []string{"native tool calling", "XALGORIX_ENGINE=legacy"} {
		if !strings.Contains(finished.Content, want) {
			t.Fatalf("init-failure message missing %q: %s", want, finished.Content)
		}
	}
}

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
