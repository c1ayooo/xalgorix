package orchestrator

import (
	"context"
	"testing"

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
