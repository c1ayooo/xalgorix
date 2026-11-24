package agent

// tool_surface.go — executable tool surfaces (R7/M2).
//
// The charter's roles are code-enforced, not prompt-only:
//   - main    : observe + steer; no attack execution, no reporting
//   - planner : read the graph + emit/retire intents + steer/kill; no execution
//   - worker  : execute one claimed intent + write evidence/findings; no global plan/delegation
//   - legacy  : the pre-R7 full surface (rollback only)
//
// A surface is a name predicate installed on tools.Registry before MCP/tool
// plugins can register, so unknown/plugin tools are default-denied for
// main/planner and default-allowed for workers.

import "strings"

// ToolSurface selects the executable tool set for one agent role.
type ToolSurface string

const (
	// SurfaceLegacyRoot is the historical full surface. The zero value means
	// "legacy" so existing call sites and tests keep their behavior.
	SurfaceLegacyRoot ToolSurface = ""
	SurfaceMain       ToolSurface = "main"
	SurfacePlanner    ToolSurface = "planner"
	SurfaceWorker     ToolSurface = "worker"
	// SurfaceOrchestrator is the control-plane API lane: HTTP calls + notes
	// only. It must never receive terminal/browser/scan tools.
	SurfaceOrchestrator ToolSurface = "orchestrator"
	// SurfaceAnalysis is the notes/analysis lane (kind=task): read notes,
	// ledger, info items and research tools, plus note writing. It has no
	// terminal, HTTP, browser or scan tools, so a "task" intent cannot drift
	// into attacking a container its target never named.
	SurfaceAnalysis ToolSurface = "analysis"
)

// mainTools is the observation/control allowlist. Prefixes read_/list_/search_
// cover the read-only catalog; the explicit names are the control tools.
var mainTools = map[string]bool{
	"graph_overview":      true,
	"read_plan":           true,
	"add_intent":          true,
	"add_note":            true,
	"steer_work":          true,
	"kill_work":           true,
	"retire_intent":       true,
	"reprioritize_intent": true,
	"finish":              true,
}

// plannerTools is the planning allowlist.
var plannerTools = map[string]bool{
	"graph_overview": true,
	"read_plan":      true,
	"build_plan":     true,
	"update_plan":    true,
	"add_intent":     true,
	"retire_intent":  true,
	"steer_work":     true,
	"kill_work":      true,
	"add_note":       true,
}

// workerDenied is the worker denylist: everything else is executable.
var workerDenied = map[string]bool{
	"create_agent": true,
	"spawn_agent":  true,
	"check_agent":  true,
	"wait_agent":   true,
	// Global plan/control belongs to the planner/main, not a lane.
	"build_plan":          true,
	"add_intent":          true,
	"retire_intent":       true,
	"reprioritize_intent": true,
	"steer_work":          true,
	"kill_work":           true,
	"claim_next_task":     true,
	"graph_overview":      true,
	"finish":              true,
}

// orchestratorTools is the control-plane API lane: HTTP + notes only.
var orchestratorTools = map[string]bool{
	"http_request":     true,
	"read_notes":       true,
	"add_note":         true,
	"read_ledger":      true,
	"read_info_item":   true,
	"record_deviation": true,
	"read_tool_output": true,
	"read_skill":       true,
	"list_skills":      true,
	// Deterministic decision aids: pure text/byte inputs, no network or
	// execution, so they are safe on the control-plane lane too.
	"detect_honeypot":      true,
	"assess_nuclei_hit":    true,
	"parse_poc_template":   true,
	"diff_responses":       true,
	"solve_slider_captcha": true,
}

// analysisTools is the notes/research lane. No execution: terminal, python,
// browser, HTTP and scan tools are all absent, so this lane cannot reach a
// container even when its symbolic task target is misread as scope.
var analysisTools = map[string]bool{
	"read_notes":              true,
	"add_note":                true,
	"read_ledger":             true,
	"read_info_item":          true,
	"read_tool_output":        true,
	"read_skill":              true,
	"list_skills":             true,
	"search_skills":           true,
	"web_search":              true,
	"cve_search":              true,
	"exploit_search":          true,
	"code_search":             true,
	"record_hypothesis":       true,
	"add_hypothesis_evidence": true,
	"update_hypothesis":       true,
	"record_deviation":        true,
	// Deterministic decision aids (no terminal/HTTP/browser access).
	"detect_honeypot":      true,
	"assess_nuclei_hit":    true,
	"parse_poc_template":   true,
	"diff_responses":       true,
	"solve_slider_captcha": true,
}

// Allows reports whether the surface permits a tool name.
func (s ToolSurface) Allows(name string) bool {
	name = strings.TrimSpace(name)
	switch s {
	case SurfaceMain:
		return hasToolPrefix(name, "read_", "list_", "search_") || mainTools[name]
	case SurfacePlanner:
		return hasToolPrefix(name, "read_", "list_", "search_") || plannerTools[name]
	case SurfaceWorker:
		return !workerDenied[name]
	case SurfaceOrchestrator:
		return orchestratorTools[name]
	case SurfaceAnalysis:
		return analysisTools[name]
	case SurfaceLegacyRoot:
		return true
	default:
		// Unknown surfaces are treated as legacy rather than silently
		// widening or narrowing the historical surface.
		return true
	}
}

func hasToolPrefix(name string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// String is stable for logs/tests.
func (s ToolSurface) String() string {
	if s == SurfaceLegacyRoot {
		return "legacy"
	}
	return string(s)
}
