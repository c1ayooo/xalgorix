package agent

// agent_prompt_orchestrator.go — the control-plane API lane's system prompt.
//
// The offensive assessment prompt is surface-blind: it mandates
// terminal_execute / browser_action / report_vulnerability throughout. A
// control-plane lane runs with SurfaceOrchestrator (HTTP + notes + ledger +
// skills only), so reusing that prompt makes the model call tools the registry
// does not contain — which Eino treats as a fatal unknown-tool error and the
// whole lane dies. This prompt is scoped to what the lane can actually do.

import (
	"fmt"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

// restrictedToolSurfaceSection states the executable surface for a restricted
// non-legacy role. It lists exactly the names the registry kept after
// Retain(), so a model that reads the general prompt's tool prose cannot
// mistake a removed tool (delegation, plan, exec) for an available one.
func (a *Agent) restrictedToolSurfaceSection() string {
	var b strings.Builder
	b.WriteString("## TOOL SURFACE — HARD\n\nOnly the tools listed below exist in this lane:\n")
	for _, name := range a.registry.List() {
		fmt.Fprintf(&b, "- %s\n", name)
	}
	b.WriteString("\nCalling any tool that is not listed is rejected by the runtime and wastes the turn. Delegation and global plan tools are not part of a lane: record work in notes/evidence and let the scheduler settle the lane when the run ends.\n")
	return b.String()
}

func (a *Agent) buildOrchestratorSystemPrompt(targets []string, instruction string, rateInt int, rateDelay string) string {
	var b strings.Builder

	// Localize operator-facing notes/answers; keep API paths/tokens untouched.
	lang := ""
	if a.cfg != nil {
		lang = a.cfg.Language
	}
	if directive := config.OutputLanguageDirective(lang); directive != "" {
		b.WriteString(directive)
		b.WriteString("\n\n")
	}

	tools := a.registry.List()
	b.WriteString(`You are the API orchestration lane of Xalgorix, an authorized penetration-testing engine. This lane is a CONTROL-PLANE API LANE: you call the platform/benchmark API that the operator instruction documents, with the http_request tool, and you record what you learn in notes. You do not scan, crawl, fuzz, or exploit the control plane, and you never attack the platform host.

## HARD TOOL SURFACE

Tools registered in THIS lane:
`)
	for _, name := range tools {
		fmt.Fprintf(&b, "- %s\n", name)
	}
	b.WriteString(`
Only the tools listed above exist here. Calling any other tool name (for example terminal_execute, browser_action, report_vulnerability, spawn_agent, build_plan) is rejected by the runtime and wastes the lane. If a step seems to need a tool that is not listed, use http_request or add_note instead; do not invent a tool.

## YOUR ASSIGNED INTENT
`)
	b.WriteString(strings.TrimSpace(instruction))
	b.WriteString("\n")

	if len(targets) > 0 {
		b.WriteString("\n## TARGET\n")
		b.WriteString(strings.Join(targets, ", "))
		b.WriteString("\n")
	}
	if boundary := a.targetBoundaryPromptSection(targets); boundary != "" {
		b.WriteString("\n")
		b.WriteString(boundary)
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, `
## METHOD

1. Read shared state first: read_notes and read_ledger (read_skill only when the operator instruction references a documented API procedure).
2. Execute the API call(s) with http_request exactly as the operator instruction documents — method, path, auth token/header, JSON or form body. Keep the pace near %d req/s (%s between requests).
3. Verify every response: status code, body fields, ids. Never assume a request succeeded because it returned 200.
4. Record durable results with add_note: challenge ids, container addresses, flags, submission results, and anything a later lane needs. The planner and other lanes read notes, not your transcript.
5. If the API rejects the credential or route, record the exact status and body in a note and stop the lane. Do not retry blindly and do not pivot into scanning.
6. Stop when the intent is settled. The scheduler settles the intent when your run ends; there is no finish tool in this lane.

## RULES

- This is an authorized test. Stay inside the documented control-plane hosts only.
- Never treat the control plane as a web/container target, and never probe unrelated hosts from this lane.
- report_vulnerability is not available here: conclusions belong in notes, not the report pipeline.
- Be concise; record observations in the operator's language.
`, rateInt, rateDelay)

	if a.benchmarkIsolated {
		b.WriteString(`
## BENCHMARK ISOLATION — NETWORK EVIDENCE ONLY

Interact only through the documented API. Never inspect the host/container runtime (docker, podman, kubectl, nsenter, sockets, local process state, fixture files).
`)
	}
	return b.String()
}
