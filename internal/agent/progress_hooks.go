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
	"regexp"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// progressToolSignals maps a successful mutating tool to the typed increments
// it produces. Keep this list to mutations; read/search tools are handled by
// progressAnalysisTools and never count as information-layer writes.
var progressToolSignals = map[string][]scanctx.ProgressSignal{
	"add_note":                {scanctx.SignalInfo},
	"record_hypothesis":       {scanctx.SignalBelief},
	"update_hypothesis":       {scanctx.SignalBelief},
	"add_hypothesis_evidence": {scanctx.SignalEvidence},
	"report_vulnerability":    {scanctx.SignalFinding},
	"submit_verdict":          {scanctx.SignalFinding},
	"record_asset":            {scanctx.SignalAsset},
	"record_intelligence":     {scanctx.SignalObservation},
	"ingest_har":              {scanctx.SignalObservation, scanctx.SignalCoverage},
	"record_deviation":        {scanctx.SignalWorkflow},
	"authz_matrix":            {scanctx.SignalBelief, scanctx.SignalCoverage},
	"complete_work":           {scanctx.SignalWorkflow},
}

// progressAnalysisTools are read/search tools over existing information. A
// distinct read is analysis progress even when no new InfoItem is written; the
// same reference repeatedly is only touch (MarkAnalysis dedupes).
var progressAnalysisTools = map[string]bool{
	"read_tool_output": true,
	"read_info_item":   true,
	"search_info":      true,
	"read_ledger":      true,
	"read_notes":       true,
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
	sc.Progress.ObserveToolAction(agentID)
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
	tool := strings.TrimSpace(args["tool_name"])
	// Phase-1 observation only: record how many actions happened since the
	// last information-layer signal and how often the same canonical
	// (tool,target,outcome) repeats. Nothing here changes agent behavior.
	sc.Progress.ObserveToolOutcome(agentID, spinSignature(tool, args))
	// A failed call changed nothing: touch keeps the liveness clock honest, and
	// the failure is classified so the snapshot can distinguish a blocked
	// environment from a worker that stopped working.
	if strings.TrimSpace(args["error"]) != "" {
		sc.Progress.MarkFailure(agentID, tool, args["error"])
		return HookResult{}
	}
	sc.Progress.ClearFailures(agentID)

	if progressAnalysisTools[strings.ToLower(tool)] {
		key := strings.ToLower(tool) + "|" + analysisReference(args)
		sc.Progress.MarkAnalysis(agentID, key)
		if strings.EqualFold(tool, "read_tool_output") {
			sc.Progress.MarkSignal(agentID, scanctx.SignalArtifact)
		}
		return HookResult{}
	}

	signals := progressToolSignals[tool]
	if len(signals) == 0 {
		return HookResult{}
	}
	// A write that stores the same value again is a successful tool call but not
	// an information-layer increment (round-5: identical note writes refreshed
	// the clock and starved real supervision).
	if tool == "add_note" && strings.EqualFold(strings.TrimSpace(args["state_changed"]), "false") {
		return HookResult{}
	}
	sc.Progress.MarkMutation(agentID, signals...)
	return HookResult{}
}

// analysisReference identifies the object a read touched. It keeps the same
// reference from refreshing the clock repeatedly while allowing a new ID/URL
// to count as new analysis progress.
func analysisReference(args map[string]string) string {
	for _, key := range []string{"id", "url", "target", "path", "file", "name"} {
		if value := strings.TrimSpace(args[key]); value != "" {
			return value
		}
	}
	return strings.TrimSpace(args["command"])
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

// spinSignature canonicalises one tool result for the Phase-1 spin
// observation. It must collapse retries of the same dead action: same tool,
// same host:port, same outcome class.
func spinSignature(tool string, args map[string]string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	return tool + "|" + spinTarget(args) + "|" + spinOutcome(args["error"], args["output"])
}

var spinHostPortRE = regexp.MustCompile(`(?i)(?:https?://)?(\[[0-9a-f:]+\]|[a-z0-9._-]+):(\d{1,5})`)

// spinTarget extracts the most specific target an action names.
func spinTarget(args map[string]string) string {
	for _, key := range []string{"url", "target", "endpoint", "host"} {
		if raw := strings.TrimSpace(args[key]); raw != "" {
			return normalizeSpinTarget(raw)
		}
	}
	if cmd := strings.TrimSpace(args["command"]); cmd != "" {
		if m := spinHostPortRE.FindStringSubmatch(cmd); len(m) == 3 {
			return strings.ToLower(m[1] + ":" + m[2])
		}
	}
	return "?"
}

func normalizeSpinTarget(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" {
		return "?"
	}
	return raw
}

var spinHTTPStatusRE = regexp.MustCompile(`HTTP/\d\.\d\s+(\d{3})`)

// spinOutcome collapses a result to a stable class.
func spinOutcome(errText, output string) string {
	if strings.TrimSpace(errText) != "" {
		return "error"
	}
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "connection refused"):
		return "refused"
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"):
		return "timeout"
	case strings.Contains(lower, "platform submission guard"):
		return "gate"
	}
	if m := spinHTTPStatusRE.FindStringSubmatch(output); len(m) == 2 {
		return m[1]
	}
	if strings.TrimSpace(output) == "" {
		return "empty"
	}
	return "ok"
}
