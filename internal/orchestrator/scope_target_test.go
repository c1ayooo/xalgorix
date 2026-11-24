package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
)

func scopeNode(payload string) *graph.Node {
	return &graph.Node{Payload: payload}
}

func TestTargetsForIntentScope(t *testing.T) {
	p := &Pool{cfg: Config{
		ScopeTargets:    []string{"acme.test", "*.wild.test", "10.0.1.0/24"},
		ScopeDeny:       []string{"deny.acme.test"},
		ScopeAllowPorts: []int{443},
	}}

	allowed := []struct {
		target string
		want   string
	}{
		{"app.acme.test", "app.acme.test"},
		{"a.wild.test", "a.wild.test"},
		{"10.0.1.55", "10.0.1.55"},
		{"https://app.acme.test:443/x", "app.acme.test:443"},
	}
	for _, c := range allowed {
		node := scopeNode(`{"target_kind":"scope","target":"` + c.target + `"}`)
		got := p.targetsForIntent(node)
		if len(got) != 1 || got[0] != c.want {
			t.Fatalf("scope target %q => %v, want [%s]", c.target, got, c.want)
		}
	}

	refused := []string{
		"evil.test",
		"https://app.acme.test:8080/",
		"https://deny.acme.test/",
		"wild.test", // wildcard excludes the apex
	}
	for _, target := range refused {
		node := scopeNode(`{"target_kind":"scope","target":"` + target + `"}`)
		if got := p.targetsForIntent(node); len(got) != 0 {
			t.Fatalf("scope target %q must be refused, got %v", target, got)
		}
	}

	// No enforced scope: the kind is never authorized (benchmark unchanged).
	noScope := &Pool{}
	node := scopeNode(`{"target_kind":"scope","target":"app.acme.test"}`)
	if got := noScope.targetsForIntent(node); len(got) != 0 {
		t.Fatalf("scope intent without configured scope must be refused, got %v", got)
	}
}

func TestSurfaceForIntentScopeIsWorker(t *testing.T) {
	p := &Pool{}
	node := scopeNode(`{"target_kind":"scope","target":"app.acme.test"}`)
	if got := p.surfaceForIntent(node); got != "worker" {
		t.Fatalf("scope surface = %q, want worker", got)
	}
}

func TestPlannerApplyScopeRequiresAuthorization(t *testing.T) {
	store := graph.NewMemoryStore()
	ctx := context.Background()
	scanID := "planner-scope"
	p := &Planner{cfg: PlannerConfig{
		ScanID: scanID, Repo: store,
		ScopeTargets: []string{"acme.test", "10.0.1.0/24"},
	}, ctx: ctx}

	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{
		{Title: "Scope discovery", Phase: 2, Target: "app.acme.test", TargetKind: "scope"},
		{Title: "Out of scope", Phase: 2, Target: "evil.test", TargetKind: "scope"},
		{Title: "CIDR sweep", Phase: 2, Target: "10.0.1.7", TargetKind: "scope"},
	}})
	if added != 2 {
		t.Fatalf("added = %d, want 2 (out-of-scope intent must be skipped)", added)
	}
	tasks, _ := store.PlanTasks(ctx, scanID)
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v", tasks)
	}
	for _, task := range tasks {
		if task.Target == "evil.test" {
			t.Fatalf("out-of-scope intent was accepted: %+v", task)
		}
	}
}

func TestPlannerApplyScopeNeedsConfiguredScope(t *testing.T) {
	store := graph.NewMemoryStore()
	p := &Planner{cfg: PlannerConfig{ScanID: "planner-scope-none", Repo: store}, ctx: context.Background()}
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{
		{Title: "Scope discovery", Phase: 2, Target: "app.acme.test", TargetKind: "scope"},
	}})
	if added != 0 {
		t.Fatalf("scope intent without configured scope must be skipped, added = %d", added)
	}
}

func TestPlannerAuthorizedScopeOverviewAndPrompt(t *testing.T) {
	store := graph.NewMemoryStore()
	p := &Planner{cfg: PlannerConfig{
		ScanID: "planner-scope-overview", Repo: store,
		ScopeTargets:    []string{"acme.test", "10.0.1.0/24"},
		ScopeDeny:       []string{"deny.acme.test"},
		ScopeAllowPorts: []int{443},
	}, ctx: context.Background()}
	overview := p.overview()
	for _, want := range []string{"AUTHORIZED SCOPE", "acme.test", "10.0.1.0/24", "deny.acme.test", "443", "target_kind=scope"} {
		if !strings.Contains(overview, want) {
			t.Fatalf("overview missing %q:\n%s", want, overview)
		}
	}

	plain := &Planner{cfg: PlannerConfig{ScanID: "planner-scope-overview-none", Repo: store}, ctx: context.Background()}
	if strings.Contains(plain.overview(), "AUTHORIZED SCOPE") {
		t.Fatal("overview must not render an AUTHORIZED SCOPE section without a configured scope")
	}
}

func TestPlannerPromptScopeRulesOnlyForEngagement(t *testing.T) {
	if !strings.Contains(plannerPromptFor(nil, true), "target_kind=scope") {
		t.Fatal("engagement prompt must mention target_kind=scope when a scope is configured")
	}
	if strings.Contains(plannerPromptFor(nil), "target_kind=scope") {
		t.Fatal("prompt without a configured scope must be unchanged")
	}
	bench := plannerPromptFor([]string{"bench.local"}, true)
	if bench != plannerSystemPrompt+plannerReasoningRules+plannerBenchmarkRules {
		t.Fatal("benchmark prompt must be byte-for-byte unchanged apart from the always-on decision discipline")
	}
	if strings.Contains(bench, "Engagement scope rules") {
		t.Fatal("benchmark prompt must not include engagement scope rules")
	}
}
