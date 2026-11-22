package agent

import (
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/tools/reporting"
)

// newIdleGoalTestAgent is newIdleTestAgent with an operator objective whose
// required criterion is still open — the "plan done, goal not achieved" state
// the auto-finalize tail must no longer treat as terminal.
func newIdleGoalTestAgent(t *testing.T, limit int) *Agent {
	t.Helper()
	a := newIdleTestAgent(t, limit)
	a.state.Objective = &ObjectiveSpec{
		Statement: "operator goal",
		Criteria: []ObjectiveCriterion{
			{ID: "C1", Kind: "finding_class", VulnClass: "sql injection", VerifyBy: "verifier", Required: true},
		},
	}
	return a
}

// TestAutoFinalizeDefersForUnmetGoal: the plan-exhausted finding-idle tail is
// no longer terminal while a required goal criterion is open. Each tail
// refire converts into one bounded goal obligation (idle restart, counter
// bump); after goalObligationLimit deferrals the finalize proceeds so an
// unreachable goal still terminates honestly.
func TestAutoFinalizeDefersForUnmetGoal(t *testing.T) {
	a := newIdleGoalTestAgent(t, 2)

	// fireTail drives iterations until the tail fires once: it returns true
	// when the scan finalized, false when the tail was deferred into a goal
	// obligation (distinguished by the counter moving).
	fireTail := func() bool {
		for i := 0; i < 10; i++ {
			a.state.Iteration++
			before := a.state.GoalObligationCount
			if a.autoFinalizeIfPlanComplete() {
				return true
			}
			if a.state.GoalObligationCount > before {
				return false
			}
		}
		t.Fatal("tail never fired within 10 iterations")
		return false
	}

	if fireTail() {
		t.Fatal("auto-finalize fired even though the goal is unmet (first tail)")
	}
	if a.state.GoalObligationCount != 1 {
		t.Fatalf("GoalObligationCount = %d, want 1 after the first deferral", a.state.GoalObligationCount)
	}
	if a.state.GoalObligation == "" || !strings.Contains(a.state.GoalObligation, "C1") {
		t.Errorf("GoalObligation = %q, want the open criterion", a.state.GoalObligation)
	}
	if a.state.FindingIdleIterations != 0 {
		t.Errorf("idle counter not reset on deferral: %d", a.state.FindingIdleIterations)
	}

	// Second tail → second bounded deferral.
	if fireTail() {
		t.Fatal("auto-finalize fired on the second tail before the deferral budget was spent")
	}
	if a.state.GoalObligationCount != 2 {
		t.Fatalf("GoalObligationCount = %d, want 2 after the second deferral", a.state.GoalObligationCount)
	}

	// Third tail → third (last) deferral; fourth tail → finalize.
	if fireTail() {
		t.Fatal("auto-finalize fired before goalObligationLimit deferrals elapsed")
	}
	if a.state.GoalObligationCount != goalObligationLimit {
		t.Fatalf("GoalObligationCount = %d, want %d", a.state.GoalObligationCount, goalObligationLimit)
	}
	if !fireTail() {
		t.Fatal("auto-finalize never proceeded after goalObligationLimit deferrals")
	}
}

// TestAutoFinalizeGoalMetFinalizesAsBefore: with every required criterion
// satisfied the tail stays a terminal stop — the obligation machinery never
// engages.
func TestAutoFinalizeGoalMetFinalizesAsBefore(t *testing.T) {
	ctxID := "idle-goal-met-ctx"
	dir := t.TempDir()
	sctx := scanctx.New(ctxID, dir)
	t.Cleanup(sctx.Close)
	t.Cleanup(func() { reporting.CleanupContext(sctx.ID) })

	a := newIdleGoalTestAgent(t, 2)
	a.scanCtx = sctx
	// Hooks read findings via state.ScanContextID (the production wiring at
	// agent.go sets it from a.scanCtx before the loop starts).
	a.state.ScanContextID = sctx.ID
	if err := reporting.RestoreContext(sctx.ID, dir, []reporting.Vulnerability{
		{ID: "XALG-1", Title: "SQL injection in login", Target: "https://t.invalid", Tags: []string{reporting.TagVerified}},
	}); err != nil {
		t.Fatal(err)
	}
	a.state.Iteration = 1
	a.autoFinalizeIfPlanComplete() // counts the seeded finding as "new", arms the tail
	finalized := false
	for i := 0; i < 6 && !finalized; i++ {
		a.state.Iteration++
		finalized = a.autoFinalizeIfPlanComplete()
	}
	if !finalized {
		t.Fatalf("goal met but finalize did not proceed at the tail (idle=%d, obligations=%d)",
			a.state.FindingIdleIterations, a.state.GoalObligationCount)
	}
	if a.state.GoalObligationCount != 0 {
		t.Errorf("GoalObligationCount = %d, want 0 when the goal is met", a.state.GoalObligationCount)
	}
}

// TestHookGoalObligationDeliversOncePerContent: the deferred obligation is
// delivered as an iteration-start directive exactly once per distinct
// content; a deliberate re-nudge (delivered marker cleared by the deferral)
// delivers again.
func TestHookGoalObligationDeliversOncePerContent(t *testing.T) {
	state := NewScanState()
	state.GoalObligation = "C1 (finding_class: sql injection): unmet"

	res := hookGoalObligation(state, nil)
	if len(res.Directives) != 1 || !strings.Contains(res.Directives[0].Content, "C1") {
		t.Fatalf("directive = %+v", res.Directives)
	}
	for _, d := range res.Directives {
		if d.OnDelivered != nil {
			d.OnDelivered(state)
		}
	}
	if state.GoalObligationDelivered != state.GoalObligation {
		t.Errorf("delivered marker = %q", state.GoalObligationDelivered)
	}
	if res := hookGoalObligation(state, nil); len(res.Directives) != 0 {
		t.Errorf("identical obligation re-delivered: %+v", res.Directives)
	}
}

// TestGoalObligationHookRegistered ensures the delivery hook is on the
// default registry (wiring regression guard). Fire merges composed
// directives into Nudge (the transcript channel), so the assertion reads
// Nudge; OnDelivered still fires for the delivered-marker side effect.
func TestGoalObligationHookRegistered(t *testing.T) {
	reg := NewHookRegistry()
	RegisterDefaultHooks(reg)
	state := NewScanState()
	state.GoalObligation = "C1 unmet"
	res := reg.Fire(OnIterationStart, state, nil)
	if !strings.Contains(res.Nudge, "GOAL OBLIGATION") || !strings.Contains(res.Nudge, "C1") {
		t.Fatalf("goal obligation not delivered through the registry: nudge=%q", res.Nudge)
	}
	if state.GoalObligationDelivered != state.GoalObligation {
		t.Errorf("delivered marker = %q, want %q", state.GoalObligationDelivered, state.GoalObligation)
	}
}
