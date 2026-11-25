package agent

// worker_pool.go — the root agent's non-tool API for the R7 worker pool.
//
// The main agent's tool surface no longer includes spawn_agent. The
// orchestrator claims an authoritative intent and asks the root to dispatch a
// worker through the same graph machinery (concurrency, queue, checkpoint,
// lifecycle events, /agents projection).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// Kill reason codes (PLANNER_OBSERVATION_AND_CORRECTION §4.1). A kill is a
// decision with a typed cause, validated by the engine rather than trusted from
// the model's prose.
const (
	KillReasonHuman           = "human"
	KillReasonStagnant        = "stagnant"
	KillReasonRepeatedFailure = "repeated_failure"
	KillReasonLaneViolation   = "lane_violation"
	KillReasonScopeViolation  = "scope_violation"
	KillReasonDuplicate       = "duplicate"
	KillReasonSuperseded      = "superseded"
	// KillReasonBudgetExhausted is an engine cause (worker timebox), never a
	// planner/human decision. It exists so a budget kill is not mislabeled as
	// "human" in the worker table.
	KillReasonBudgetExhausted = "budget_exhausted"
	// KillReasonLeaseLost is an engine cause: the intent lease expired or was
	// reclaimed. The old worker must stop before it produces side effects a new
	// owner cannot see.
	KillReasonLeaseLost = "lease_lost"
)

// ErrStagnantKillRefused is returned when the engine rejects a stagnant kill
// because the worker produced information-layer progress inside the guard
// window. Callers must treat this as "do not kill", not as a generic failure:
// falling back to a plain (human-labeled) cancel is the round-5 bug.
var ErrStagnantKillRefused = errors.New("stagnant kill refused: recent information-layer progress")

// KillProgressWindow is the information-layer recency window a stagnant kill
// must respect: a worker that produced an increment inside it may not be
// killed as stagnant. The orchestrator reuses it as the retry cooldown after a
// refusal so the two guards cannot drift.
const KillProgressWindow = 2 * time.Minute

var validKillReasonCodes = map[string]bool{
	KillReasonHuman:           true,
	KillReasonStagnant:        true,
	KillReasonRepeatedFailure: true,
	KillReasonLaneViolation:   true,
	KillReasonScopeViolation:  true,
	KillReasonDuplicate:       true,
	KillReasonSuperseded:      true,
	KillReasonBudgetExhausted: true,
	KillReasonLeaseLost:       true,
}

func normalizeKillReasonCode(code string) (string, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return KillReasonHuman, nil
	}
	if !validKillReasonCodes[code] {
		return "", fmt.Errorf("unknown reason_code %q (allowed: human, stagnant, repeated_failure, lane_violation, scope_violation, duplicate, superseded)", code)
	}
	return code, nil
}

// DispatchWorkerTask enqueues one worker run on the root's graph. The caller
// has already claimed the intent; the task text carries it to the worker.
func (a *Agent) DispatchWorkerTask(name, task string, targets []string, unitID string) (string, error) {
	return a.DispatchWorkerTaskWithSurface(name, task, targets, unitID, "")
}

// DispatchWorkerTaskWithSurface selects the child's role surface (for example
// the control-plane API orchestration lane).
func (a *Agent) DispatchWorkerTaskWithSurface(name, task string, targets []string, unitID, surface string) (string, error) {
	if a == nil || a.agentGraph == nil {
		return "", fmt.Errorf("worker pool graph unavailable")
	}
	// IG6d: seed the worker with the shared run state (hints, plan, open/dead
	// beliefs, gated findings, coverage, notes) so a fresh lane does not
	// rediscover dead ends. Bounded to 8 KiB and read-only.
	if seed := a.workerSeedContext(unitID); seed != "" {
		task = strings.TrimRight(task, "\n") + "\n\n" + seed
	}
	agentID, err := a.agentGraph.DispatchWithSurface(name, task, targets, unitID, surface)
	if err != nil {
		return "", err
	}
	// Bind the authoritative unit to the delegated agent id so kill validation
	// can translate the main agent's unit address into the progress-ledger key
	// (the hook records progress under the delegated agent id).
	if a.scanCtx != nil && a.scanCtx.Progress != nil {
		a.scanCtx.Progress.BindUnit(unitID, agentID)
	}
	return agentID, nil
}

// WaitWorkerResult blocks until the dispatched worker settles (or ctx ends).
func (a *Agent) WaitWorkerResult(ctx context.Context, agentID string) (summary, status, errText string) {
	if a == nil || a.agentGraph == nil {
		return "", "missing", "worker pool graph unavailable"
	}
	return a.agentGraph.DispatchResult(ctx, agentID)
}

// WorkerPoolSize returns the configured pool concurrency (0 = legacy graph
// default). The orchestrator uses it to size its goroutines.
func (a *Agent) WorkerPoolSize() int {
	if a == nil {
		return 0
	}
	return int(a.workerPoolSize.Load())
}

// SetWorkerConcurrency applies a live worker-count change (settings hot
// resize). It updates the graph's admission limit so a grow actually runs the
// new slots instead of queueing them; a shrink leaves running workers to finish
// and simply stops admitting more until active falls below the new limit.
func (a *Agent) SetWorkerConcurrency(n int) error {
	if a == nil {
		return fmt.Errorf("agent unavailable")
	}
	if n < 1 {
		n = 1
	}
	if n > 10 {
		n = 10
	}
	a.workerPoolSize.Store(int64(n))
	if a.agentGraph != nil {
		a.agentGraph.SetMaxConcurrent(n)
	}
	return nil
}

// SteerWorker injects a course correction into the active worker for one unit.
func (a *Agent) SteerWorker(unitID, msg string) error {
	if a == nil || a.agentGraph == nil {
		return fmt.Errorf("worker pool graph unavailable")
	}
	return a.agentGraph.SteerUnit(unitID, msg)
}

// KillWorker cancels the active worker for one unit. It is the human-default
// entry point; use KillWorkerWithReason when a typed cause is available.
func (a *Agent) KillWorker(unitID, reason string) error {
	return a.KillWorkerWithReason(unitID, KillReasonHuman, reason)
}

// WaitWorkerExit blocks until the worker bound to unitID has actually exited
// (its done channel closed). It is the exit-confirmation half of a kill: the
// orchestrator may not settle the intent while the worker is still winding down.
func (a *Agent) WaitWorkerExit(ctx context.Context, unitID string) error {
	if a == nil || a.agentGraph == nil {
		return fmt.Errorf("worker pool graph unavailable")
	}
	return a.agentGraph.WaitUnitExit(ctx, strings.TrimSpace(unitID))
}

// KillWorkerWithReason cancels the active worker for one unit after validating
// the typed reason code. An empty code defaults to human.
//
// Engine validation (§4.1): a stagnant kill is rejected while the progress
// ledger shows an information-layer increment inside the two-minute window.
// Other codes are decision causes (lane/scope violations, duplicate work,
// superseded direction) that do not depend on the progress clock.
func (a *Agent) KillWorkerWithReason(unitID, reasonCode, reason string) error {
	if a == nil || a.agentGraph == nil {
		return fmt.Errorf("worker pool graph unavailable")
	}
	code, err := normalizeKillReasonCode(reasonCode)
	if err != nil {
		return err
	}
	unitID = strings.TrimSpace(unitID)
	if unitID == "" {
		return fmt.Errorf("unit_id is required")
	}
	if code == KillReasonStagnant {
		if agentID, ok := a.workerAgentID(unitID); ok {
			if name, _, active := a.scanCtx.Progress.ActiveTool(agentID); active {
				deadline, _ := a.scanCtx.Progress.ActiveToolDeadline(agentID)
				if deadline.IsZero() || time.Now().Before(deadline) {
					return fmt.Errorf("%w: tool %s is still running; wait for its result", ErrStagnantKillRefused, name)
				}
			}
		}
		_, lastProgress, ok := a.workerProgress(unitID)
		if ok && !lastProgress.IsZero() {
			if ago := time.Since(lastProgress); ago < KillProgressWindow {
				return fmt.Errorf("%w: %s produced progress %s ago (window %s); steer it and wait for a real stall", ErrStagnantKillRefused, unitID, ago.Round(time.Second), KillProgressWindow)
			}
		}
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "reason_code=" + code
	} else {
		reason = code + ": " + reason
	}
	if err := a.agentGraph.KillUnit(unitID, reason); err != nil {
		return err
	}
	actor := "controller"
	noInfo := domainjournal.DecisionNoInfoEnginePolicy
	if code == KillReasonHuman {
		actor = "human"
		noInfo = domainjournal.DecisionNoInfoOperator
	}
	a.recordDecision(domainjournal.Decision{
		Action:          "kill_work",
		IntentID:        unitID,
		Candidates:      []domainjournal.DecisionCandidate{{ID: unitID, Kind: "intent", Rank: 1, Selected: true}},
		HardConstraints: []string{"reason_code=pass", "progress_window=pass"},
		Selected:        unitID,
		Reason:          reason,
		InfoUse:         "none",
		NoInfoReason:    noInfo,
	}, actor)
	return nil
}

// workerAgentID resolves the delegated agent id for a unit without requiring a
// progress timestamp. It is used by the active-tool guard, which must refuse a
// stagnant kill even when the tool has not returned a result yet.
func (a *Agent) workerAgentID(unitID string) (string, bool) {
	if a == nil || a.scanCtx == nil || a.scanCtx.Progress == nil {
		return "", false
	}
	unitID = strings.TrimSpace(unitID)
	if unitID == "" {
		return "", false
	}
	if agentID, ok := a.scanCtx.Progress.UnitAgent(unitID); ok && agentID != "" {
		return agentID, true
	}
	return unitID, true
}

// workerProgress resolves the progress projection for a unit. The dispatch path
// binds unit → delegated agent id; a caller that passes an agent id directly
// still resolves through the fallback lookup.
func (a *Agent) workerProgress(unitID string) (lastTouch, lastProgress time.Time, ok bool) {
	if a.scanCtx == nil || a.scanCtx.Progress == nil {
		return time.Time{}, time.Time{}, false
	}
	progress := a.scanCtx.Progress
	if agentID, bound := progress.UnitAgent(unitID); bound {
		if touch, last, found := progress.AgentTimes(agentID); found {
			return touch, last, true
		}
	}
	return progress.AgentTimes(unitID)
}

// RegisterWorkerSteer lets the agent runner bind a child's message sink.
func (a *Agent) RegisterWorkerSteer(agentID string, fn func(string) error) {
	if a != nil && a.agentGraph != nil {
		a.agentGraph.RegisterSteer(agentID, fn)
	}
}

// WorkerPoolRunning reports the graph's live worker count, for the finish gate
// and panel projections.
func (a *Agent) WorkerPoolRunning() int {
	if a == nil || a.agentGraph == nil {
		return 0
	}
	return a.agentGraph.RunningCount()
}

// WorkerPoolUncollected reports results not yet observed. The pool consumes
// every result itself, so this is normally zero on the main-agent path.
func (a *Agent) WorkerPoolUncollected() int {
	if a == nil || a.agentGraph == nil {
		return 0
	}
	return a.agentGraph.UncollectedCount()
}
