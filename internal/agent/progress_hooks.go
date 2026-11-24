package agent

// progress_hooks.go — feeds the scan's progress ledger projection from the
// central post-tool-result path (hooks.Fire(OnToolResult)).
//
// The projection answers one question deterministically (docs/PLANNER_
// OBSERVATION_AND_CORRECTION.md §3): did this worker produce an information-
// layer increment, and when? Touch is recorded for every tool result; progress
// is recorded only for tools that MUTATE the information layer (notes, ledger
// hypotheses/evidence, findings, assets, intelligence). Read/list/search tools
// never count. The store is per-scan, in-memory only — it is an observation
// projection, not a state authority.

import (
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// progressMutatingTools lists tools whose successful execution is an
// information-layer increment. Keep this list to mutations; adding a
// read/search tool here would let a busy read loop masquerade as progress.
var progressMutatingTools = map[string]bool{
	// Notes (the model-facing information mirror).
	"add_note": true,
	// Ledger hypotheses and evidence.
	"record_hypothesis":       true,
	"add_hypothesis_evidence": true,
	"update_hypothesis":       true,
	// Findings pass the report gate, but an accepted finding is the strongest
	// information-layer increment a run can produce.
	"report_vulnerability": true,
	"submit_verdict":       true,
	// Asset graph and intelligence projections.
	"record_asset":        true,
	"record_intelligence": true,
	"ingest_har":          true,
	// Deviation/audit projections that change the ledger's state.
	"record_deviation": true,
	// Deterministic access-control hypothesis generation.
	"authz_matrix": true,
}

// hookProgressToolStart marks a tool as actively executing so the stall clock
// does not reclaim a worker whose legitimate long tool has not returned yet.
func hookProgressToolStart(state *ScanState, args map[string]string) HookResult {
	if state == nil || strings.TrimSpace(state.ScanContextID) == "" {
		return HookResult{}
	}
	sc := scanctx.Get(state.ScanContextID)
	if sc == nil || sc.Progress == nil {
		return HookResult{}
	}
	agentID := progressAgentID(state)
	sc.Progress.BeginTool(agentID, args["tool_name"])
	if budget := toolActiveBudget(args["tool_name"]); budget > 0 {
		sc.Progress.SetToolDeadline(agentID, time.Now().Add(budget))
	}
	return HookResult{}
}

// toolActiveBudget is the tool's own maximum execution time. A tool running
// past this deadline is no longer a legitimate long task; reclaim/kill may act
// on it. Unknown tools get a conservative default so a wedged subprocess still
// reaches a decision before the worker's total budget.
func toolActiveBudget(name string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "terminate", "terminal_execute", "terminal":
		return 60 * time.Minute
	case "python_action", "python":
		return 30 * time.Minute
	case "browser_action", "page_agent", "pageagent":
		return 10 * time.Minute
	case "http_request", "send_request":
		return 5 * time.Minute
	default:
		return 15 * time.Minute
	}
}

// hookProgressLedger updates the per-scan progress projection. It never changes
// hook behavior elsewhere: it always returns an empty HookResult.
func hookProgressLedger(state *ScanState, args map[string]string) HookResult {
	if state == nil || strings.TrimSpace(state.ScanContextID) == "" {
		return HookResult{}
	}
	sc := scanctx.Get(state.ScanContextID)
	if sc == nil || sc.Progress == nil {
		return HookResult{}
	}
	agentID := progressAgentID(state)
	sc.Progress.EndTool(agentID)
	sc.Progress.Touch(agentID)
	// A failed call changed nothing: touch keeps the liveness clock honest but
	// the information layer did not move.
	if strings.TrimSpace(args["error"]) != "" {
		return HookResult{}
	}
	tool := strings.TrimSpace(args["tool_name"])
	if !progressMutatingTools[tool] {
		return HookResult{}
	}
	// A write that stores the same value again is a successful tool call but not
	// an information-layer increment (round-5: identical note writes refreshed
	// the clock and starved real supervision).
	if tool == "add_note" && strings.EqualFold(strings.TrimSpace(args["state_changed"]), "false") {
		return HookResult{}
	}
	sc.Progress.MarkProgress(agentID)
	return HookResult{}
}

func progressAgentID(state *ScanState) string {
	if state == nil {
		return "root"
	}
	agentID := strings.TrimSpace(state.DelegatedAgentID)
	if agentID == "" {
		return "root"
	}
	return agentID
}
