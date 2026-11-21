package tools

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGate is the minimal ApprovalGate stub for registry-level tests: it
// records whether Execute reached the gate and can block before answering
// (letting tests simulate the pending-approval wait without the web layer).
type fakeGate struct {
	seen          atomic.Bool
	sawArgs       map[string]string
	denialText    string
	allowed       bool
	block         <-chan struct{}
	execToolFirst atomic.Bool
}

func (g *fakeGate) BeforeExecute(req ApprovalRequest) ApprovalVerdict {
	g.seen.Store(true)
	g.sawArgs = req.Args
	if g.block != nil {
		<-g.block
	}
	if g.allowed {
		return ApprovalVerdict{Allowed: true}
	}
	return ApprovalVerdict{Allowed: false, Output: g.denialText}
}

func newGateTestRegistry(executed *atomic.Bool) *Registry {
	r := NewRegistry()
	r.Register(&Tool{
		Name: "terminal_execute",
		Parameters: []Parameter{
			{Name: "command", Required: true},
		},
		Execute: func(args map[string]string) (Result, error) {
			executed.Store(true)
			return Result{Output: "ran: " + args["command"]}, nil
		},
	})
	return r
}

// TestExecute_ApprovalGateAllows keeps the allow path transparent: the tool
// executes and the circuit breaker never hears about it.
func TestExecute_ApprovalGateAllows(t *testing.T) {
	var executed atomic.Bool
	r := newGateTestRegistry(&executed)
	gate := &fakeGate{allowed: true, denialText: "unused"}
	r.SetApprovalGate(gate)

	res, err := r.Execute("terminal_execute", map[string]string{"command": "id"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.Success || !strings.Contains(res.Output, "ran: id") {
		t.Errorf("allowed call must execute normally, got %+v", res)
	}
	if !executed.Load() {
		t.Error("tool must run when the gate allows")
	}
	if !gate.seen.Load() {
		t.Error("gate must be consulted")
	}
	if got := r.circuitBreaker.failures["terminal_execute"]; got != 0 {
		t.Errorf("breaker recorded %d failures on the allow path, want 0", got)
	}
}

// TestExecute_ApprovalGateDenialShortCircuits pins the R2a contract: a denial
// returns BEFORE the tool and BEFORE the circuit breaker — the denial is an
// operator-facing verdict, not a tool failure (AGENTS.md registry failure
// semantics; registry_test.go pins the breaker contract).
func TestExecute_ApprovalGateDenialShortCircuits(t *testing.T) {
	var executed atomic.Bool
	r := newGateTestRegistry(&executed)
	denial := "⛔ APPROVAL REJECTED — the operator declined this request."
	r.SetApprovalGate(&fakeGate{allowed: false, denialText: denial})

	res, err := r.Execute("terminal_execute", map[string]string{"command": "shutdown -h now"})
	if err != nil {
		t.Fatalf("denial must not surface a Go error (expected outcome): %v", err)
	}
	if res.Success {
		t.Error("denial must carry Success=false")
	}
	if res.Error != "" {
		t.Errorf("denial must travel via Output, not Error (Error feeds the breaker); got %q", res.Error)
	}
	if res.Output != denial {
		t.Errorf("output = %q, want the gate's denial text", res.Output)
	}
	if executed.Load() {
		t.Error("tool must NOT execute after a denial")
	}
	if got := r.circuitBreaker.failures["terminal_execute"]; got != 0 {
		t.Errorf("circuit breaker failures = %d, want 0 — expected rejections never feed the breaker", got)
	}
	if r.circuitBreaker.IsOpen("terminal_execute") {
		t.Error("circuit must stay closed after a denial")
	}
}

// TestExecute_ApprovalGateReceivesArgsCopy verifies the gate cannot corrupt
// the caller's args: it gets a copy, and the registry's later defensive copy
// still sees the original values.
func TestExecute_ApprovalGateReceivesArgsCopy(t *testing.T) {
	var executed atomic.Bool
	r := newGateTestRegistry(&executed)
	gate := &fakeGate{allowed: true}
	r.SetApprovalGate(gate)

	callerArgs := map[string]string{"command": "dig example.com", "extra": "keep"}
	if _, err := r.Execute("terminal_execute", callerArgs); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(gate.sawArgs) != 2 || gate.sawArgs["command"] != "dig example.com" {
		t.Errorf("gate saw %v, want the full args copy", gate.sawArgs)
	}
	gate.sawArgs["command"] = "mutated"
	if callerArgs["command"] != "dig example.com" {
		t.Error("gate mutated the caller's map")
	}
	if !executed.Load() || !strings.Contains(mustOutput(r, "terminal_execute", map[string]string{"command": "x"}), "ran") {
		t.Error("tool execution must be unaffected by gate arg handling")
	}
}

func mustOutput(r *Registry, name string, args map[string]string) string {
	res, _ := r.Execute(name, args)
	return res.Output
}

// TestExecute_ApprovalGateNilKeepsLegacyBehavior pins the byte-identical
// legacy contract: no gate installed (or SetApprovalGate(nil)) → execution
// path identical to pre-gate behavior, including the breaker.
func TestExecute_ApprovalGateNilKeepsLegacyBehavior(t *testing.T) {
	var executed atomic.Bool
	r := newGateTestRegistry(&executed)
	// No SetApprovalGate call at all.
	res, err := r.Execute("terminal_execute", map[string]string{"command": "id"})
	if err != nil || !res.Success || res.Output != "ran: id" {
		t.Fatalf("legacy path changed: %+v, %v", res, err)
	}
	// Explicit nil install behaves the same.
	r.SetApprovalGate(nil)
	res, err = r.Execute("terminal_execute", map[string]string{"command": "id"})
	if err != nil || !res.Success || res.Output != "ran: id" {
		t.Fatalf("explicit nil gate changed the path: %+v, %v", res, err)
	}
	// And a soft tool failure still feeds the breaker exactly as before.
	r.Register(&Tool{Name: "flaky", Execute: func(map[string]string) (Result, error) {
		return Result{Error: "boom"}, nil
	}})
	if _, err := r.Execute("flaky", nil); err != nil {
		t.Fatal(err)
	}
	if got := r.circuitBreaker.failures["flaky"]; got != 1 {
		t.Errorf("legacy failure semantics changed: failures=%d, want 1", got)
	}
}

// TestExecute_ApprovalGateBlockingWait documents that Execute blocks inside
// the gate until it answers — the pending-approval suspension happens inside
// BeforeExecute, so the agent's own toolWaitContext (hardTimeoutFor(tool)+30s)
// is the outer ceiling. When the configured approval timeout EXCEEDS that
// ceiling, the agent hard-timeout wins the race and abandons the result; the
// approval row is still closed by the gate service's own deadline (web-layer
// tests pin that path). The gate must therefore eventually answer even
// without an operator decision.
func TestExecute_ApprovalGateBlockingWait(t *testing.T) {
	var executed atomic.Bool
	r := newGateTestRegistry(&executed)
	block := make(chan struct{})
	gate := &fakeGate{allowed: false, denialText: "timeout", block: block}
	r.SetApprovalGate(gate)

	done := make(chan Result, 1)
	go func() {
		res, err := r.Execute("terminal_execute", map[string]string{"command": "sleep 999"})
		if err != nil {
			t.Errorf("execute: %v", err)
		}
		done <- res
	}()
	// While the gate is pending, the tool never ran.
	select {
	case res := <-done:
		t.Fatalf("execute returned while the gate was still pending: %+v", res)
	case <-time.After(50 * time.Millisecond):
	}
	if executed.Load() {
		t.Error("tool must not execute while the gate is pending")
	}
	close(block)
	select {
	case res := <-done:
		if res.Success || !strings.Contains(res.Output, "timeout") {
			t.Errorf("denial text lost after a blocking wait: %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("gate answer never reached Execute")
	}
	if executed.Load() {
		t.Error("tool must not execute after a late denial")
	}
}

// TestExecute_ApprovalGateSkippedOnBreakerOpenAndBadArgs pins the L2
// placement: the gate is the LAST step before tool.Execute — a breaker-OPEN
// tool or a call missing required parameters fails before the gate is ever
// consulted, so neither burns an approval slot.
func TestExecute_ApprovalGateSkippedOnBreakerOpenAndBadArgs(t *testing.T) {
	var executed atomic.Bool
	r := newGateTestRegistry(&executed)
	gate := &fakeGate{allowed: true}
	r.SetApprovalGate(gate)

	// Trip the breaker with 5 soft failures.
	r.Register(&Tool{Name: "flaky", Execute: func(map[string]string) (Result, error) {
		return Result{Error: "boom"}, nil
	}})
	for i := 0; i < 5; i++ {
		if _, err := r.Execute("flaky", nil); err != nil {
			t.Fatal(err)
		}
	}
	if !r.circuitBreaker.IsOpen("flaky") {
		t.Fatal("breaker should be open")
	}
	seenBefore := gate.seen.Load()
	res, err := r.Execute("flaky", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "Circuit breaker OPEN") {
		t.Errorf("breaker message lost: %+v", res)
	}
	if gate.seen.Load() != seenBefore {
		t.Error("gate must NOT be consulted while the breaker is open (no approval slot burn)")
	}

	// Missing required parameter → validation error before the gate.
	if _, err := r.Execute("terminal_execute", nil); err == nil {
		t.Fatal("expected missing-param error")
	}
	if gate.seen.Load() != seenBefore {
		t.Error("gate must NOT be consulted for a call missing required params")
	}
	if executed.Load() {
		t.Error("tool must not execute")
	}

	// Unknown tool → lookup error before the gate.
	if _, err := r.Execute("no_such_tool", nil); err == nil {
		t.Fatal("expected unknown-tool error")
	}
	if gate.seen.Load() != seenBefore {
		t.Error("gate must NOT be consulted for an unknown tool")
	}
}
