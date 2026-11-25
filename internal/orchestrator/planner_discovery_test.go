package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// R1: the planner may propose discovery, but apply() admits it only when the
// shared policy authorizes it against the live surface.

func discoveryPlanner(t *testing.T, cfg PlannerConfig, endpoints []string) *Planner {
	t.Helper()
	store := graph.NewMemoryStore()
	sctx := scanctx.New("r1-"+cfg.ScanID, "")
	for i, ep := range endpoints {
		sctx.Ledger.Upsert(scanctx.Hypothesis{
			ID: "H-" + string(rune('A'+i)), VulnClass: "recon", Endpoint: ep,
			Status: scanctx.HypothesisQueued,
		})
	}
	cfg.Repo = store
	if cfg.ScanCtx == nil {
		cfg.ScanCtx = sctx
	}
	return &Planner{cfg: cfg, ctx: context.Background()}
}

func TestPlannerRejectsContentDiscoveryUnderQuiet(t *testing.T) {
	p := discoveryPlanner(t, PlannerConfig{
		ScanID: "quiet", NoiseProfile: "quiet", Target: "10.0.0.5:80",
	}, nil)
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		Title: "Bounded content discovery — wordlist pass", Target: "10.0.0.5:80",
		Endpoint: "/", Phase: 3,
	}}})
	if added != 0 {
		t.Fatalf("quiet must reject a wordlist proposal, added=%d", added)
	}
}

func TestPlannerAdmitsContentDiscoveryWhenInventoryThin(t *testing.T) {
	p := discoveryPlanner(t, PlannerConfig{
		ScanID: "thin", NoiseProfile: "balanced", Target: "10.0.0.5:80",
	}, []string{"/login", "/"})
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		Title: "Bounded content discovery — gap-driven wordlist pass", Target: "10.0.0.5:80",
		Endpoint: "/", Phase: 3,
	}}})
	if added != 1 {
		t.Fatalf("thin balanced inventory must admit one bounded pass, added=%d", added)
	}
}

func TestPlannerRejectsContentDiscoveryWithMatureInventory(t *testing.T) {
	endpoints := []string{"/", "/a", "/b", "/c", "/d", "/e", "/f", "/g"}
	p := discoveryPlanner(t, PlannerConfig{
		ScanID: "mature", NoiseProfile: "balanced", Target: "10.0.0.5:80",
	}, endpoints)
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		Title: "Directory & file discovery (ffuf/gobuster)", Target: "10.0.0.5:80",
		Endpoint: "/", Phase: 3,
	}}})
	if added != 0 {
		t.Fatalf("mature inventory must reject generic enumeration, added=%d", added)
	}
}

func TestPlannerRejectsContentDiscoveryWithAPISpec(t *testing.T) {
	p := discoveryPlanner(t, PlannerConfig{
		ScanID: "apispec", NoiseProfile: "balanced", Target: "10.0.0.5:80",
	}, []string{"/openapi.json"})
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		Title: "内容发现：目录枚举", Target: "10.0.0.5:80", Endpoint: "/", Phase: 3,
	}}})
	if added != 0 {
		t.Fatalf("an API description must reject path enumeration, added=%d", added)
	}
}

func TestPlannerPortGate(t *testing.T) {
	cases := []struct {
		name  string
		cfg   PlannerConfig
		in    plannerIntent
		want  int
		label string
	}{
		{
			name: "URL target never authorizes a sweep",
			cfg: PlannerConfig{ScanID: "p-url", Target: "https://app.example.com", NoiseProfile: "balanced",
				Deep: true, RequestBudgetConfigured: true, UserInstruction: "run nmap"},
			in:   plannerIntent{Title: "Full TCP port + service discovery", Target: "https://app.example.com", Phase: 1},
			want: 0, label: "URL",
		},
		{
			name: "host:port without budget rejected",
			cfg: PlannerConfig{ScanID: "p-nobudget", Target: "10.0.0.5:80", NoiseProfile: "balanced",
				Deep: true, RequestBudgetConfigured: false, UserInstruction: "run nmap"},
			in:   plannerIntent{Title: "Full TCP port + service discovery", Target: "10.0.0.5:80", Phase: 1},
			want: 0, label: "no budget",
		},
		{
			name: "host:port shallow rejected",
			cfg: PlannerConfig{ScanID: "p-shallow", Target: "10.0.0.5:80", NoiseProfile: "balanced",
				Deep: false, RequestBudgetConfigured: true, UserInstruction: "run nmap"},
			in:   plannerIntent{Title: "Full TCP port + service discovery", Target: "10.0.0.5:80", Phase: 1},
			want: 0, label: "shallow",
		},
		{
			name: "authorized deep run admits",
			cfg: PlannerConfig{ScanID: "p-ok", Target: "10.0.0.5:80", NoiseProfile: "balanced",
				Deep: true, RequestBudgetConfigured: true, UserInstruction: "run nmap"},
			in:   plannerIntent{Title: "Full TCP port + service discovery", Target: "10.0.0.5:80", Phase: 1},
			want: 1, label: "authorized",
		},
		{
			name: "quiet blocks unless operator asked",
			cfg: PlannerConfig{ScanID: "p-quiet", Target: "10.0.0.5:80", NoiseProfile: "quiet",
				Deep: true, RequestBudgetConfigured: true},
			in:   plannerIntent{Title: "Port scan", Target: "10.0.0.5:80", Phase: 1},
			want: 0, label: "quiet",
		},
	}
	for _, tc := range cases {
		p := discoveryPlanner(t, tc.cfg, nil)
		added, _ := p.apply(plannerOutput{Intents: []plannerIntent{tc.in}})
		if added != tc.want {
			t.Errorf("%s (%s): added=%d want %d", tc.name, tc.label, added, tc.want)
		}
	}
}

func TestPlannerStillAdmitsNormalTestingIntent(t *testing.T) {
	p := discoveryPlanner(t, PlannerConfig{
		ScanID: "normal", NoiseProfile: "quiet", Target: "10.0.0.5:80",
	}, []string{"/guestbook"})
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		Title: "SSTI verification on /guestbook", VulnClass: "ssti",
		Target: "10.0.0.5:80", Endpoint: "/guestbook", Phase: 6,
	}}})
	if added != 1 {
		t.Fatalf("a normal testing intent must never be gated by discovery policy, added=%d", added)
	}
}

func TestEmptyPlanOnEmptyFrontierDoesNotDrain(t *testing.T) {
	store := graph.NewMemoryStore()
	p := &Planner{cfg: PlannerConfig{ScanID: "floor", Repo: store}, ctx: context.Background()}
	p.noteEmptyPlan()
	if p.planRounds.Load() != 0 {
		t.Fatal("an empty plan on an empty frontier must keep retrying, not count as a drained plan")
	}
	// With work in the frontier, an empty plan is a legal decision.
	if _, err := p.cfg.Repo.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: "floor", Kind: graph.KindIntent, UnitKey: "i-1", State: graph.StateOpen,
	}); err != nil {
		t.Fatal(err)
	}
	p.noteEmptyPlan()
	if p.planRounds.Load() != 1 {
		t.Fatal("an empty plan with open work is a valid decision")
	}
}

// TestPlannerControlPlaneAPIIntentBypassesDiscovery pins the round-7 failure:
// a control-plane reconcile intent ("GET /openapi/v1/challenges") was rejected
// as a port sweep because its notes quoted the operator's "no port scanning"
// rule. The API lane is orchestration, not target recon.
func TestPlannerControlPlaneAPIIntentBypassesDiscovery(t *testing.T) {
	p := discoveryPlanner(t, PlannerConfig{
		ScanID: "cp-api", NoiseProfile: "balanced", Target: "https://bench.example.com",
		ControlPlaneHosts: []string{"bench.example.com"},
	}, nil)
	in := plannerIntent{
		Title:      "列题：GET /openapi/v1/challenges（BENCHMARK_TOKEN 鉴权）",
		Target:     "https://bench.example.com",
		TargetKind: "control_plane_api",
		Endpoint:   "/openapi/v1/challenges",
		Notes:      "平台域名只用于 API 调用；禁止端口扫描、目录爆破与爬虫。",
		Phase:      1,
	}
	if ok, reason := p.admitDiscovery(in, in.Target, nil); !ok {
		t.Fatalf("control-plane API read must be admitted, reason=%q", reason)
	}
}

// TestPlannerIgnoresDiscoveryWordsQuotedInNotes proves classification uses the
// declared action (title + vuln_class), not notes that quote prohibitions.
func TestPlannerIgnoresDiscoveryWordsQuotedInNotes(t *testing.T) {
	p := discoveryPlanner(t, PlannerConfig{
		ScanID: "notes-quote", NoiseProfile: "balanced", Target: "https://app.example.com",
	}, nil)
	in := plannerIntent{
		Title:    "列题：GET /openapi/v1/challenges",
		Target:   "https://app.example.com",
		Endpoint: "/openapi/v1/challenges",
		Notes:    "操作者要求：禁止端口扫描与目录爆破，只允许 API 调用。",
		Phase:    1,
	}
	if ok, reason := p.admitDiscovery(in, in.Target, nil); !ok {
		t.Fatalf("an intent whose notes quote a prohibition must not be gated as discovery, reason=%q", reason)
	}
}

// TestPlannerSettleGap pins the settle-triggered cadence: routine wakes are
// spaced by plannerMinModelGap, while the first round and urgent triggers do
// not wait.
func TestPlannerSettleGap(t *testing.T) {
	now := time.Now()
	if got := plannerSettleGap(time.Time{}, now); got != 0 {
		t.Fatalf("first round must not wait: %v", got)
	}
	if got := plannerSettleGap(now.Add(-30*time.Second), now); got != time.Minute {
		t.Fatalf("30s after a round, wait = %v, want 60s", got)
	}
	if got := plannerSettleGap(now.Add(-2*time.Minute), now); got != 0 {
		t.Fatalf("stale round must not wait: %v", got)
	}
}
