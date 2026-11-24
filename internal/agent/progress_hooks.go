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
	agentID := strings.TrimSpace(state.DelegatedAgentID)
	if agentID == "" {
		agentID = "root"
	}
	sc.Progress.Touch(agentID)
	// A failed call changed nothing: touch keeps the liveness clock honest but
	// the information layer did not move.
	if strings.TrimSpace(args["error"]) != "" {
		return HookResult{}
	}
	if progressMutatingTools[strings.TrimSpace(args["tool_name"])] {
		sc.Progress.MarkProgress(agentID)
	}
	return HookResult{}
}
