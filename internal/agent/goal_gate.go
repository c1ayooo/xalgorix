package agent

// goal_gate.go — the operator goal as a finish-gate OBLIGATION (R-goal; this
// revises the pre-R-goal "observation only" boundary in REDESIGN §3.1).
//
// The objective keeps every property it always had: the evaluator still only
// reads findings the report pipeline accepted (charter §3.5), finding labels
// stay with report_vulnerability, and the gate grants nothing — it can only
// keep the loop OPEN, never widen scope, relax a phase, or change what counts
// as a finding. What changes is closing: while a required, engine-decidable
// criterion is not met, the ROOT agent may not voluntarily finish.
//
// Termination stays guaranteed (the gate can never deadlock a scan):
//   - verify_by=manual criteria never block — the engine cannot prove them
//     itself, they surface as manual_pending;
//   - the gate stands down after the same MaxFinishRejections valve as
//     hookFinishGatekeeper, so an unreachable goal ends honestly via
//     finish_gate_exhausted and the objective disposition recorded at run
//     completion still shows goal_unmet;
//   - delegated specialists never carry the gate (the root owns the goal);
//   - budget/operator stops are engine-side and unaffected.
//
// The verdict is deliberately deterministic: objective.Evaluate is the same
// pure function the web-side panel uses, fed with the same signal shapes
// (findings via the reporting store, coverage via the plan counters that
// PlanDisposition persists into scan.json). No extra LLM call judges the goal.

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/objective"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

// hookGoalFinishGate refuses voluntary finish while required, engine-decidable
// objective criteria are unmet. Registered AFTER hookFinishGatekeeper and
// hookLedgerFinishGate: methodology/coverage/ledger precision reasons win the
// BlockReason when several gates block at once, and the goal reason surfaces
// once those pass — the model then sees exactly which acceptance lines are
// still open.
func hookGoalFinishGate(state *ScanState, _ map[string]string) HookResult {
	if state == nil || state.DelegatedAgent || state.Objective == nil || len(state.Objective.Criteria) == 0 {
		return HookResult{}
	}
	maxRejections := state.MaxFinishRejections
	if maxRejections <= 0 {
		maxRejections = 15
	}
	if state.FinishAttempts > maxRejections {
		return HookResult{} // deadlock valve: finish_gate_exhausted records the gap honestly
	}

	open := goalGateOpenCriteria(state)
	if len(open) == 0 {
		return HookResult{}
	}
	var b strings.Builder
	b.WriteString("⛔ GOAL OBLIGATION — the operator's objective is not yet achieved; finish is refused.\n")
	b.WriteString("Open acceptance criteria:\n")
	b.WriteString(strings.Join(open, "\n"))
	b.WriteString("\nExtend the plan toward these criteria and produce evidence through 'report_vulnerability' with real verification. Do not attempt to finish again until each criterion above is satisfied by accepted, verified evidence (or is impossible in this environment — then say so explicitly in your work notes instead of stretching the evidence).")
	return HookResult{
		Block:       true,
		BlockReason: "goal not achieved: " + strings.Join(open, "; "),
		Nudge:       b.String(),
	}
}

// goalGateOpenCriteria evaluates the objective against the live signal and
// returns one line per required, engine-decidable criterion that is not met.
// Shared by the finish gate (hookGoalFinishGate) and the auto-finalize
// deferral (deferFinalizeForGoalObligation) so both sides of the closing
// boundary use the identical deterministic verdict.
func goalGateOpenCriteria(state *ScanState) []string {
	if state == nil || state.Objective == nil || len(state.Objective.Criteria) == 0 {
		return nil
	}
	report := objective.Evaluate(objectiveSpecFromAgent(state.Objective), objectiveSignalForState(state))
	var open []string
	for i, c := range state.Objective.Criteria {
		if !c.Required || c.VerifyBy == objective.VerifyManual {
			continue
		}
		// Defensive: the evaluator returns one status per criterion today, but
		// never block on a length mismatch — a missing status can't prove the
		// goal, so it would already be caught upstream as undetermined.
		if i >= len(report.Criteria) {
			break
		}
		if report.Criteria[i].State == objective.StateMet {
			continue
		}
		open = append(open, fmt.Sprintf("- %s (%s): %s", criterionLabel(c), criterionKindDetail(c), report.Criteria[i].Reason))
	}
	return open
}

// goalAchievedAutoStop actively closes the run when every required,
// engine-decidable criterion is met — the complement of hookGoalFinishGate
// (which refuses finish while one is open; R-goal). It fires only when a
// structured objective with at least one required non-manual criterion
// exists, so scans without an objective behave exactly as before. The
// terminal path reuses emitPlanCompleteFinish so the event is
// indistinguishable from a graceful completion (findings never lost), and
// the returned stop reason records goal_achieved in scan.json. Once-only via
// goalAchievedEmitted: BeforeTurn runs every turn.
func (a *Agent) goalAchievedAutoStop() bool {
	state := a.state
	if state == nil || state.DelegatedAgent || state.Objective == nil || len(state.Objective.Criteria) == 0 {
		return false
	}
	required := false
	for _, c := range state.Objective.Criteria {
		if c.Required && c.VerifyBy != objective.VerifyManual {
			required = true
			break
		}
	}
	if !required || a.goalAchievedEmitted.Load() {
		return false
	}
	if len(goalGateOpenCriteria(state)) != 0 {
		return false
	}
	if !a.goalAchievedEmitted.CompareAndSwap(false, true) {
		return false
	}
	findings := 0
	if a.scanCtx != nil {
		findings = len(reporting.GetVulnerabilitiesForContext(a.scanCtx.ID))
	}
	// Same flags a model-issued finish sets: StopCheck suppresses the ADK's
	// next paid model call, and the loop unwinds through the normal
	// completion path.
	a.finishAccepted.Store(true)
	a.scanFinished.Store(true)
	a.emit(Event{Type: "message", Content: "🎯 Objective achieved — all required acceptance criteria are satisfied by accepted, verified evidence. Closing the run (stop reason: goal_achieved).", TotalTokens: a.syncBudgetTokens()})
	a.emitPlanCompleteFinish(findings, false)
	return true
}

// goalObligationLimit bounds how many times the plan-complete tail may be
// converted into a goal obligation. After it, the finalize proceeds and the
// objective disposition recorded at completion shows goal_unmet — the honest
// terminal for a goal unreachable in this environment.
const goalObligationLimit = 3

// deferFinalizeForGoalObligation converts one firing of the plan-exhausted
// finding-idle tail into a goal obligation instead of a terminal stop: the
// open criteria are queued for delivery (hookGoalObligation) and the idle
// tail restarts so the model has room to extend the plan. Returns true when
// the finalize was deferred (caller keeps looping); false once the deferral
// budget is spent (caller finalizes).
func (a *Agent) deferFinalizeForGoalObligation(open []string) bool {
	if a.state.GoalObligationCount >= goalObligationLimit {
		return false
	}
	a.state.GoalObligationCount++
	a.state.GoalObligation = strings.Join(open, "; ")
	// Deliberate re-nudge: clear the delivered marker so the obligation text
	// reaches the model again after each tail refire.
	a.state.GoalObligationDelivered = ""
	a.state.FindingIdleIterations = 0
	a.state.FindingIdleSince = time.Time{}
	log.Printf("[goal-gate] plan complete but goal unmet (%d/%d deferrals): %s",
		a.state.GoalObligationCount, goalObligationLimit, a.state.GoalObligation)
	return true
}

// hookGoalObligation delivers the deferred goal obligation as an
// iteration-start directive, registered after hookPlanner — whose plan brief
// goes quiet exactly when the plan is exhausted, the moment the obligation
// must speak. Delivery dedupe mirrors LastPlanBrief: once per distinct
// content; the deferral clears the marker when the tail refires.
func hookGoalObligation(state *ScanState, _ map[string]string) HookResult {
	if state == nil || state.GoalObligation == "" || state.GoalObligation == state.GoalObligationDelivered {
		return HookResult{}
	}
	content := "GOAL OBLIGATION — the plan is complete but the operator's objective is not achieved yet.\n" +
		"Open acceptance criteria:\n" + state.GoalObligation + "\n" +
		"Extend the plan (build_plan/update_plan) toward these criteria and produce verified evidence through 'report_vulnerability'. When a criterion is impossible in this environment, say so explicitly instead of stretching the evidence."
	return HookResult{Directives: []Directive{{
		Priority:  DirectivePriorityPlanner,
		Category:  "goal",
		DedupeKey: "goal-obligation",
		Content:   content,
		OnDelivered: func(s *ScanState) {
			s.GoalObligationDelivered = s.GoalObligation
		},
	}}}
}

// objectiveSpecFromAgent mirrors the engine-side criteria into the evaluator's
// spec (same mapping as platform objectives, minus the platform import).
func objectiveSpecFromAgent(o *ObjectiveSpec) *objective.Spec {
	spec := &objective.Spec{Statement: o.Statement}
	for _, c := range o.Criteria {
		spec.Criteria = append(spec.Criteria, objective.SpecCriterion{
			ID:         c.ID,
			Kind:       c.Kind,
			VulnClass:  c.VulnClass,
			Capability: c.Capability,
			VerifyBy:   c.VerifyBy,
			Required:   c.Required,
		})
	}
	return spec
}

// objectiveSignalForState collects the evaluator's signal from live scan
// state: accepted findings from the reporting store (same shape the web
// projects into scan.json), coverage from the plan counters (same semantics
// as PlanDisposition → PlanTasksTotal/PlanTasksCompleted). Title/CWE carry
// the matching tokens, exactly as on the web side.
func objectiveSignalForState(state *ScanState) objective.Signal {
	var sig objective.Signal
	for _, v := range reporting.GetVulnerabilitiesForContext(state.ScanContextID) {
		sig.Findings = append(sig.Findings, objective.FindingSignal{
			ID:           v.ID,
			Title:        v.Title,
			Severity:     v.Severity,
			CWE:          v.CWE,
			Tag:          objectiveTagOf(v.Tags),
			ExploitProof: v.ExploitationProof,
		})
	}
	if state.Plan != nil {
		_, _, completed, _ := state.Plan.Counts()
		sig.CoveragePlanned = len(state.Plan.Tasks)
		sig.CoverageCompleted = completed
	}
	return sig
}

// objectiveTagOf picks the verification dimension tag of an accepted finding
// via the shared helper (web twin until R-goal sank it into internal/objective).
func objectiveTagOf(tags []string) string {
	return objective.VerificationTagOf(tags)
}

func criterionLabel(c ObjectiveCriterion) string {
	if c.ID != "" {
		return c.ID
	}
	return "?"
}

// criterionKindDetail renders the human-meaningful part of a criterion.
func criterionKindDetail(c ObjectiveCriterion) string {
	switch c.Kind {
	case objective.KindFindingClass:
		return "finding_class: " + c.VulnClass
	case objective.KindCapability:
		return "capability: " + c.Capability
	case objective.KindCoverage:
		return "coverage"
	default:
		return c.Kind
	}
}

// forceContinuationFinishValve bounds how many times the model may attempt
// finish after the operator re-opened a finished run (REDESIGN D11) before
// an honest finish is allowed.
const forceContinuationFinishValve = 10

// hookForceContinuationFinishGate refuses model-initiated finish while the
// operator's 1:1 re-open (SetForceContinuation) is active. The restored
// checkpoint looks "done", so the model's rational first move is finish —
// but the re-open means there is new, re-prioritized work per the updated
// instruction. Registered right after the gatekeeper (so FinishAttempts is
// already counted) and ahead of the ledger/goal gates; stands down after
// forceContinuationFinishValve so a genuinely exhausted run still closes.
func hookForceContinuationFinishGate(state *ScanState, _ map[string]string) HookResult {
	if state == nil || state.DelegatedAgent || !state.ForceContinuation {
		return HookResult{}
	}
	state.ForceContinuationFinishAttempts++
	if state.ForceContinuationFinishAttempts > forceContinuationFinishValve {
		return HookResult{}
	}
	return HookResult{
		Block:       true,
		BlockReason: "⛔ RUN RE-OPENED BY THE OPERATOR — this engagement continues. The updated instruction sets the current priorities: sweep the remaining medium 300-point challenges (a-07, a-09, a-12, a-14, a-17) with up to 3 containers active, submit flags the moment you have them, and keep the shelved c-03/a-03 for last. Do not call finish unless the platform starts returning invalid_state.",
	}
}
