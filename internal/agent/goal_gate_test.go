package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

// goalGateState wires a fresh scan context + state carrying the given
// objective criteria, mirroring newTestCtxState's isolation conventions.
func goalGateState(t *testing.T, criteria []ObjectiveCriterion) (*scanctx.ScanContext, *ScanState) {
	t.Helper()
	ctx, state := newTestCtxState(t)
	state.Objective = &ObjectiveSpec{Statement: "operator goal", Criteria: criteria}
	return ctx, state
}

// TestGoalFinishGateBlocksUntilCriterionVerified: an unverified candidate is
// "unmet" (the criterion demands the verified tag), so finish is refused with
// a per-criterion reason; an independently verified finding satisfies it.
func TestGoalFinishGateBlocksUntilCriterionVerified(t *testing.T) {
	criteria := []ObjectiveCriterion{
		{ID: "C1", Kind: "finding_class", VulnClass: "sql injection", VerifyBy: "verifier", Required: true},
	}
	_, state := goalGateState(t, criteria)

	res := hookGoalFinishGate(state, nil)
	if !res.Block {
		t.Fatal("gate allowed finish with no evidence for a required criterion")
	}
	if !strings.Contains(res.BlockReason, "C1") || !strings.Contains(res.BlockReason, "sql injection") {
		t.Errorf("BlockReason = %q, want per-criterion detail", res.BlockReason)
	}
	if res.Nudge == "" {
		t.Error("rejection carries no actionable nudge")
	}

	// A candidate that is only needs-manual-verification is still unmet: the
	// gate must not let an honest-gap finding pose as goal achievement.
	reporting.SeedVulnsForContext(state.ScanContextID, []reporting.Vulnerability{
		{ID: "V-1", Title: "SQL injection in login form", Tags: []string{reporting.TagManualReview}},
	})
	if res := hookGoalFinishGate(state, nil); !res.Block {
		t.Fatal("gate allowed finish on an unverified candidate")
	}

	// Independent verification satisfies the criterion and the gate passes.
	// (SeedVulnsForContext dedups by title, so the verified twin is a
	// different finding — the evaluator then picks the strongest tag.)
	reporting.SeedVulnsForContext(state.ScanContextID, []reporting.Vulnerability{
		{ID: "V-2", Title: "SQL injection in search page confirmed", Tags: []string{reporting.TagVerified}},
	})
	if res := hookGoalFinishGate(state, nil); res.Block {
		t.Fatalf("gate still blocking after verification: %s", res.BlockReason)
	}
}

func TestGoalFinishGatePassesWithoutObjective(t *testing.T) {
	ctx, state := newTestCtxState(t)
	state.Objective = nil
	_ = ctx
	if res := hookGoalFinishGate(state, nil); res.Block {
		t.Fatalf("gate blocked a scan with no objective: %s", res.BlockReason)
	}
}

// TestGoalFinishGateManualCriteriaNeverBlock: verify_by=manual marks an
// outcome the engine can never prove itself, so it can only ever surface as
// manual_pending — blocking on it would deadlock every run toward budget.
func TestGoalFinishGateManualCriteriaNeverBlock(t *testing.T) {
	_, state := goalGateState(t, []ObjectiveCriterion{
		{ID: "C1", Kind: "capability", Capability: "shell", VerifyBy: "manual", Required: true},
	})
	if res := hookGoalFinishGate(state, nil); res.Block {
		t.Fatalf("manual criterion blocked finish: %s", res.BlockReason)
	}
}

func TestGoalFinishGateIgnoresOptionalCriteria(t *testing.T) {
	_, state := goalGateState(t, []ObjectiveCriterion{
		{ID: "C1", Kind: "finding_class", VulnClass: "ssrf", VerifyBy: "verifier", Required: false},
	})
	if res := hookGoalFinishGate(state, nil); res.Block {
		t.Fatalf("optional criterion blocked finish: %s", res.BlockReason)
	}
}

// TestGoalFinishGateCapabilityNeedsConcreteProof: deterministic capability
// criteria accept only a verified/exploit-proven finding that carries concrete
// exploitation proof.
func TestGoalFinishGateCapabilityNeedsConcreteProof(t *testing.T) {
	_, state := goalGateState(t, []ObjectiveCriterion{
		{ID: "C1", Kind: "capability", Capability: "shell", VerifyBy: "deterministic", Required: true},
	})
	reporting.SeedVulnsForContext(state.ScanContextID, []reporting.Vulnerability{
		{ID: "V-1", Title: "Command injection leads to remote shell", Tags: []string{reporting.TagExploitProven}},
	})
	if res := hookGoalFinishGate(state, nil); !res.Block {
		t.Fatal("exploit-proven tag without concrete proof satisfied a deterministic capability criterion")
	}

	reporting.SeedVulnsForContext(state.ScanContextID, []reporting.Vulnerability{
		{ID: "V-2", Title: "Remote shell via command injection (proven)",
			Tags: []string{reporting.TagExploitProven}, ExploitationProof: "uid=0(root) … cat /etc/passwd head -5"},
	})
	if res := hookGoalFinishGate(state, nil); res.Block {
		t.Fatalf("concrete exploitation proof did not satisfy the gate: %s", res.BlockReason)
	}
}

// TestGoalFinishGateCoverage: a required coverage criterion tracks plan
// completion with the same counters the web evaluator persists
// (PlanDisposition: planned=len(tasks), completed=completed count).
func TestGoalFinishGateCoverage(t *testing.T) {
	_, state := goalGateState(t, []ObjectiveCriterion{
		{ID: "C1", Kind: "coverage", VerifyBy: "verifier", Required: true},
	})
	plan := NewPlan()
	plan.add(&Task{ID: "t1", Title: "T1", Status: TaskCompleted})
	plan.add(&Task{ID: "t2", Title: "T2", Status: TaskPending})
	state.Plan = plan

	res := hookGoalFinishGate(state, nil)
	if !res.Block || !strings.Contains(res.BlockReason, "1/2") {
		t.Fatalf("coverage 1/2 did not block with counters: %+v", res)
	}

	plan.add(&Task{ID: "t3", Title: "T3", Status: TaskCompleted})
	plan.add(&Task{ID: "t4", Title: "T4", Status: TaskCompleted})
	// The bar is completed >= planned (skipped does not count): t2 is the
	// last pending task, completing it makes coverage 4/4 = met.
	for _, task := range plan.Tasks {
		if task.ID == "t2" {
			task.Status = TaskCompleted
		}
	}
	if res := hookGoalFinishGate(state, nil); res.Block {
		t.Fatalf("coverage 3/3 did not pass: %s", res.BlockReason)
	}
}

// TestGoalFinishGateValve: after MaxFinishRejections rejections the goal gate
// stands down like every other gate — the honest finish_gate_exhausted
// terminal beats a deadlock, and the objective disposition records the gap.
func TestGoalFinishGateValve(t *testing.T) {
	_, state := goalGateState(t, []ObjectiveCriterion{
		{ID: "C1", Kind: "finding_class", VulnClass: "sqli", VerifyBy: "verifier", Required: true},
	})
	state.FinishAttempts = 16 // default valve is 15
	if res := hookGoalFinishGate(state, nil); res.Block {
		t.Fatalf("valve did not stand the gate down: %s", res.BlockReason)
	}
}

func TestGoalFinishGateSkipsDelegatedAgents(t *testing.T) {
	_, state := goalGateState(t, []ObjectiveCriterion{
		{ID: "C1", Kind: "finding_class", VulnClass: "sqli", VerifyBy: "verifier", Required: true},
	})
	state.DelegatedAgent = true
	if res := hookGoalFinishGate(state, nil); res.Block {
		t.Fatalf("gate applied to a delegated specialist: %s", res.BlockReason)
	}
}

// TestGoalAchievedAutoStop pins the R-goal complement: once every required,
// engine-decidable criterion is satisfied by verified evidence, the run
// closes itself through the graceful terminal path — exactly once — and
// scans without an objective (or with one still open) behave as before.
func TestGoalAchievedAutoStop(t *testing.T) {
	ctx, state := goalGateState(t, []ObjectiveCriterion{
		{ID: "C1", Kind: "finding_class", VulnClass: "sql injection", VerifyBy: "verifier", Required: true},
	})
	a := NewAgent(&config.Config{MaxIterations: 10}, "goal-achieved-test",
		make(chan Event, 64), scopeguard.Config{}, ctx)
	t.Cleanup(a.Stop)
	a.state = state

	// Criterion open: the finish gate refuses and the auto-stop must not fire.
	if a.goalAchievedAutoStop() {
		t.Fatal("auto-stop fired while a required criterion was unmet")
	}

	// Verified evidence satisfies the criterion → auto-stop fires exactly once
	// and walks the same terminal event path a model-issued finish would.
	reporting.SeedVulnsForContext(state.ScanContextID, []reporting.Vulnerability{
		{ID: "V-1", Title: "SQL injection in search page confirmed", Tags: []string{reporting.TagVerified}},
	})
	if !a.goalAchievedAutoStop() {
		t.Fatal("auto-stop did not fire with all required criteria met")
	}
	sawFinished, sawNotice := false, false
	for {
		select {
		case ev := <-a.events:
			switch ev.Type {
			case "finished":
				sawFinished = true
			case "message":
				if strings.Contains(ev.Content, "goal_achieved") {
					sawNotice = true
				}
			}
		default:
			if !sawFinished {
				t.Fatal("goal-achieved auto-stop emitted no finished event")
			}
			if !sawNotice {
				t.Fatal("goal-achieved auto-stop emitted no operator notice")
			}
			if a.goalAchievedAutoStop() {
				t.Fatal("auto-stop fired twice")
			}
			return
		}
	}
}

// TestGoalAchievedAutoStopWithoutObjective: no structured objective → zero
// behavior change for ordinary scans.
func TestGoalAchievedAutoStopWithoutObjective(t *testing.T) {
	ctx, state := newTestCtxState(t)
	a := NewAgent(&config.Config{MaxIterations: 10}, "goal-none-test",
		make(chan Event, 16), scopeguard.Config{}, ctx)
	t.Cleanup(a.Stop)
	a.state = state
	if a.goalAchievedAutoStop() {
		t.Fatal("auto-stop fired without a structured objective")
	}
}

// TestMainChatFinishRechecksRequiredObjective pins XALG012: an empty frontier
// is not goal achievement. A model/completion finish must pass the same
// deterministic required-objective gate, while an explicit operator stop remains
// immediate.
func TestMainChatFinishRechecksRequiredObjective(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx, state := goalGateState(t, []ObjectiveCriterion{
		{ID: "C1", Kind: "finding_class", VulnClass: "sql injection", VerifyBy: "verifier", Required: true},
	})
	sctx.SetInstanceID("run-goal-finish")
	state.PlannerWorkerMode = true
	a := NewAgent(&config.Config{}, "finish-gate-chat", make(chan Event, 32), scopeguard.Config{}, sctx)
	t.Cleanup(a.Stop)
	a.state = state

	call := llm.ToolCall{Name: "finish", Args: map[string]string{"reason": "frontier drained"}}
	out, finished := a.mainChatToolResult(call, "please finish the scan")
	if finished {
		t.Fatal("finish marked the run complete with an unmet required objective")
	}
	if !strings.Contains(out, "C1") {
		t.Fatalf("finish refusal = %q, want the unmet criterion", out)
	}

	// An explicit operator stop is still immediate.
	if out, finished := a.mainChatToolResult(call, "停止扫描"); !finished {
		t.Fatalf("explicit stop was blocked: %q", out)
	}
	if !a.scanFinished.Load() {
		t.Fatal("explicit stop did not finish the run")
	}
}
