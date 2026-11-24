package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestPlannerToolAddIntentAndRetire(t *testing.T) {
	store := graph.NewMemoryStore()
	scanID := "planner-tool-write"
	p := &Planner{cfg: PlannerConfig{ScanID: scanID, Repo: store}, ctx: context.Background()}

	out, added, retired := p.execPlannerTool(llm.ToolCall{
		Name: "add_intent",
		Args: map[string]string{
			"task_id": "test-sqli-tool", "title": "Test SQLi", "phase": "6",
			"vuln_class": "sqli", "endpoint": "/api/users", "target": "10.0.1.5:8080",
			"target_kind": "container", "timebox_s": "300",
		},
	}, 0)
	if added != 1 || retired != 0 || !strings.Contains(out, "intent added") {
		t.Fatalf("add: out=%q added=%d retired=%d", out, added, retired)
	}
	tasks, _ := store.PlanTasks(context.Background(), scanID)
	if len(tasks) != 1 || tasks[0].TaskID != "test-sqli-tool" {
		t.Fatalf("tasks = %+v", tasks)
	}

	// A second identical add is a dedupe no-op.
	out, added, _ = p.execPlannerTool(llm.ToolCall{
		Name: "add_intent",
		Args: map[string]string{
			"title": "Test SQLi", "phase": "6", "vuln_class": "sqli",
			"endpoint": "/api/users", "target": "10.0.1.5:8080", "target_kind": "container",
		},
	}, added)
	if added != 0 || !strings.Contains(out, "rejected") {
		t.Fatalf("duplicate add: out=%q added=%d", out, added)
	}

	out, _, retired = p.execPlannerTool(llm.ToolCall{
		Name: "retire_intent", Args: map[string]string{"task_id": "test-sqli-tool"},
	}, 0)
	if retired != 1 || !strings.Contains(out, "intent retired") {
		t.Fatalf("retire: out=%q retired=%d", out, retired)
	}
}

func TestPlannerToolCapsRoundWrites(t *testing.T) {
	store := graph.NewMemoryStore()
	p := &Planner{cfg: PlannerConfig{ScanID: "planner-tool-cap", Repo: store}, ctx: context.Background()}
	out, added, _ := p.execPlannerTool(llm.ToolCall{
		Name: "add_intent",
		Args: map[string]string{"title": "one", "target": "10.0.0.1:80", "target_kind": "web"},
	}, 4)
	if added != 0 || !strings.Contains(out, "maximum of 4") {
		t.Fatalf("cap: out=%q added=%d", out, added)
	}
}

func TestPlannerToolReadInfoItem(t *testing.T) {
	sctx := scanctx.New("planner-tool-read", "")
	sctx.Ledger.Upsert(scanctx.Hypothesis{
		ID: "H-1", VulnClass: "sqli", Endpoint: "/login", Status: scanctx.HypothesisQueued,
		NextAction: "confirm error-based injection",
	})
	sctx.Ledger.AddEvidence("H-1", scanctx.Evidence{Summary: "syntax error observed"})
	all := sctx.Ledger.All()
	evID := all[0].Evidence[0].ID
	p := &Planner{cfg: PlannerConfig{ScanID: "planner-tool-read", ScanCtx: sctx}, ctx: context.Background()}

	if out := p.execPlannerToolName("read_info_item", map[string]string{"id": "H-1"}); !strings.Contains(out, "belief H-1") {
		t.Fatalf("belief read = %q", out)
	}
	if out := p.execPlannerToolName("read_info_item", map[string]string{"id": evID}); !strings.Contains(out, "syntax error observed") {
		t.Fatalf("evidence read = %q", out)
	}
	if out := p.execPlannerToolName("read_info_item", map[string]string{"id": "nope"}); !strings.Contains(out, "not found") {
		t.Fatalf("missing read = %q", out)
	}
}

func (p *Planner) execPlannerToolName(name string, args map[string]string) string {
	out, _, _ := p.execPlannerTool(llm.ToolCall{Name: name, Args: args}, 0)
	return out
}

func TestPlannerToolsSteerAndKill(t *testing.T) {
	var steered, killed string
	p := &Planner{cfg: PlannerConfig{
		ScanID: "planner-tools-control", Repo: graph.NewMemoryStore(),
		Steer: func(unitID, message string) error { steered = unitID + ":" + message; return nil },
		Kill:  func(unitID, reasonCode, reason string) error { killed = unitID + ":" + reasonCode; return nil },
	}, ctx: context.Background()}

	if out := p.execPlannerToolName("steer_work", map[string]string{
		"unit_id": "u-1", "message": "focus on authz",
	}); !strings.Contains(out, "steered") {
		t.Fatalf("steer_work = %q", out)
	}
	if steered != "u-1:focus on authz" {
		t.Fatalf("steer callback = %q", steered)
	}
	if p.roundActions.Load() != 1 {
		t.Fatalf("roundActions = %d, want 1 after a steer", p.roundActions.Load())
	}

	if out := p.execPlannerToolName("kill_work", map[string]string{
		"unit_id": "u-1", "reason_code": "stagnant", "reason": "no progress",
	}); !strings.Contains(out, "killed") {
		t.Fatalf("kill_work = %q", out)
	}
	if killed != "u-1:stagnant" {
		t.Fatalf("kill callback = %q", killed)
	}
	if p.roundActions.Load() != 2 {
		t.Fatalf("roundActions = %d, want 2 after a kill", p.roundActions.Load())
	}

	// Unknown reason codes are rejected before the callback runs.
	killed = ""
	out := p.execPlannerToolName("kill_work", map[string]string{"unit_id": "u-1", "reason_code": "bogus"})
	if killed != "" || !strings.Contains(out, "reason_code") {
		t.Fatalf("invalid reason_code: out=%q killed=%q", out, killed)
	}
	// Missing parameters are rejected.
	if out := p.execPlannerToolName("steer_work", map[string]string{"unit_id": "u-1"}); !strings.Contains(out, "required") {
		t.Fatalf("missing message = %q", out)
	}
}

func TestPlannerToolsSteerKillUnavailableWithoutCallbacks(t *testing.T) {
	p := &Planner{cfg: PlannerConfig{ScanID: "planner-tools-none"}, ctx: context.Background()}
	if out := p.execPlannerToolName("steer_work", map[string]string{"unit_id": "u", "message": "m"}); !strings.Contains(out, "unavailable") {
		t.Fatalf("steer without callback = %q", out)
	}
	if out := p.execPlannerToolName("kill_work", map[string]string{"unit_id": "u", "reason_code": "stagnant"}); !strings.Contains(out, "unavailable") {
		t.Fatalf("kill without callback = %q", out)
	}
}
