package agent

// decision_tools.go — human decision actions on the authoritative plan
// (PLANNER_OBSERVATION_AND_CORRECTION §2.4/§5.6). They are decisions, not
// execution: they change what is worked on next, never what a lane is allowed
// to do (lane tool surfaces, scope and the report gate still apply).
//
// steer_work / kill_work already live in worker_control_tools.go and
// worker_pool.go. This file adds the two remaining decision actions that were
// only reachable from the main chat: an exported retire wrapper (the decision
// inbox is not a chat turn) and intent reprioritization.

import (
	"fmt"
	"log"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

const (
	// intentPriorityMin/Max bound the operator-settable claim priority. The
	// graph orders claims by priority DESC, so a larger number is claimed
	// first; the phase-derived defaults stay inside 780..990.
	intentPriorityMin = 0
	intentPriorityMax = 100000
)

// RetireIntentForAPI is the exported decision entry for the operator decision
// inbox. It is the same transition the main-chat retire_intent tool applies:
// actor=human, only open → deleted (a CAS, so a concurrent planner/worker
// change returns a conflict instead of silently overwriting newer state).
// Running/paused work must be killed or settled by the pool first.
func (a *Agent) RetireIntentForAPI(unitID, reason string) error {
	_, err := a.mainChatRetireIntent(map[string]string{
		"unit_id": unitID,
		"reason":  reason,
	})
	return err
}

// ReprioritizeIntent rewrites one intent's claim priority without touching its
// lifecycle state or payload. The unit is addressed by its PlanTask unit key.
// The existing payload is preserved verbatim, so target/notes/objective fields
// survive the rewrite; only the priority column changes (Repository.UpdateNode
// updates payload + priority and explicitly leaves state alone).
//
// actor=human is part of the emitted audit event and of the caller-visible
// result.
func (a *Agent) ReprioritizeIntent(unitID string, priority int, reason string) error {
	unitID = strings.TrimSpace(unitID)
	if unitID == "" {
		return fmt.Errorf("unit_id is required")
	}
	if priority < intentPriorityMin || priority > intentPriorityMax {
		return fmt.Errorf("priority %d is out of range [%d, %d]", priority, intentPriorityMin, intentPriorityMax)
	}
	repo, scanID, err := a.mainChatGraph()
	if err != nil {
		return err
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	tasks, err := repo.PlanTasks(ctx, scanID)
	if err != nil {
		return err
	}
	var target *graph.PlanTask
	for i := range tasks {
		if strings.TrimSpace(tasks[i].TaskID) == unitID {
			target = &tasks[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no intent %q in the plan", unitID)
	}
	node, err := repo.GetNode(ctx, scanID, target.NodeID)
	if err != nil {
		return err
	}
	payload := strings.TrimSpace(node.Payload)
	if payload == "" {
		payload = "{}"
	}
	if err := repo.UpdateNode(ctx, scanID, target.NodeID, payload, priority); err != nil {
		return err
	}
	a.recordDecision(domainjournal.Decision{
		Action:          "reprioritize_intent",
		IntentID:        unitID,
		Candidates:      []domainjournal.DecisionCandidate{{ID: unitID, Kind: "intent", Rank: 1, Selected: true}},
		HardConstraints: []string{"priority_range=pass"},
		Selected:        unitID,
		Reason:          strings.TrimSpace(reason),
		InfoUse:         domainjournal.DecisionInfoUseNone,
		NoInfoReason:    domainjournal.DecisionNoInfoOperator,
	}, "human")
	meta := map[string]any{
		"actor":    "human",
		"action":   "reprioritize_intent",
		"unit_id":  unitID,
		"priority": priority,
	}
	if r := strings.TrimSpace(reason); r != "" {
		meta["reason"] = r
	}
	a.emit(Event{Type: "plan_updated", Content: "human reprioritize: " + unitID, Meta: meta})
	return nil
}

// recordDecision appends one structured decision to the scan's audit journal.
// It is best-effort for the caller's primary action (the plan transition has
// already committed), but a write failure is logged instead of silently
// claiming the decision was traced.
func (a *Agent) recordDecision(d domainjournal.Decision, actor string) {
	if a == nil || a.scanCtx == nil || a.scanCtx.Ledger == nil {
		return
	}
	if strings.TrimSpace(d.Actor) == "" {
		d.Actor = strings.TrimSpace(actor)
	}
	if strings.TrimSpace(d.InfoUse) == "" {
		d.InfoUse = domainjournal.DecisionInfoUseNone
	}
	if strings.TrimSpace(d.NoInfoReason) == "" {
		d.NoInfoReason = domainjournal.DecisionNoInfoOperator
	}
	if _, err := a.scanCtx.Ledger.AppendDecision(d, scanctx.AuditActorForAgent(actor)); err != nil {
		log.Printf("[agent] record decision %s failed: %v", d.Action, err)
	}
}
