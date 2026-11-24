package agent

// orchestrator_prompt_test.go — the system prompt must match the executable
// surface. A control-plane lane runs with HTTP + notes only; reusing the
// offensive prompt made the model call terminal_execute/report_vulnerability,
// which the registry does not contain (a fatal unknown-tool error before the
// engine's handler, a wasted turn after it).

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

func surfaceTestRegistry(names ...string) *tools.Registry {
	reg := tools.NewRegistry()
	for _, name := range names {
		reg.Register(&tools.Tool{
			Name: name, Description: name,
			Execute: func(map[string]string) (tools.Result, error) {
				return tools.Result{Success: true}, nil
			},
		})
	}
	return reg
}

func TestOrchestratorSurfacePromptMatchesInventory(t *testing.T) {
	agent := &Agent{
		cfg:         &config.Config{},
		registry:    surfaceTestRegistry("http_request", "add_note", "read_notes"),
		toolSurface: SurfaceOrchestrator,
	}
	prompt := agent.buildSystemPrompt(
		[]string{"tsecbench.example"},
		"List challenges, then start container f2-05 and record its address.",
		scanctx.RequestRatePolicy{MaxRPS: 1, Source: "test"},
	)

	for _, want := range []string{
		"HARD TOOL SURFACE",
		"- http_request",
		"- add_note",
		"- read_notes",
		"List challenges, then start container f2-05",
		"tsecbench.example",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("orchestrator prompt missing %q", want)
		}
	}
	for _, banned := range []string{
		"## YOUR HACKER MINDSET",             // the offensive prompt must not be reused
		"Use ONLY standard pentesting tools", // names terminal_execute/browser_action as required
		"report every proven finding with report_vulnerability",
		"Do NOT call spawn_agent",
	} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("orchestrator prompt must not contain %q", banned)
		}
	}
	// terminal_execute may only appear once, as a forbidden-tool example.
	if strings.Count(prompt, "terminal_execute") > 1 {
		t.Fatalf("terminal_execute is still advertised as an instruction: %s", prompt)
	}
}

func TestWorkerSurfacePromptListsRestrictedTools(t *testing.T) {
	agent := &Agent{
		cfg:         &config.Config{},
		registry:    surfaceTestRegistry("http_request", "terminal_execute"),
		toolSurface: SurfaceWorker,
	}
	prompt := agent.buildSystemPrompt(
		[]string{"https://example.test"},
		"Work the claimed lane.",
		scanctx.RequestRatePolicy{MaxRPS: 2, Source: "test"},
	)
	if !strings.Contains(prompt, "## TOOL SURFACE — HARD") {
		t.Fatal("restricted surface section missing")
	}
	for _, want := range []string{"- http_request", "- terminal_execute"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("surface section missing %q", want)
		}
	}
	// Execution lanes keep the full assessment prompt.
	if !strings.Contains(prompt, "## YOUR HACKER MINDSET") {
		t.Fatal("worker surface should keep the assessment prompt")
	}
}
