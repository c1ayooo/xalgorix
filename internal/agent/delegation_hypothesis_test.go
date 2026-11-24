package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

func delegationAgent(t *testing.T) (*Agent, *scanctx.ScanContext) {
	t.Helper()
	ctx, state := newTestCtxState(t)
	state.DelegationEnabled = true
	a := &Agent{
		state:    state,
		scanCtx:  ctx,
		registry: tools.NewRegistry(),
	}
	return a, ctx
}

func TestFormalSchedulableExcludesCoverageSeeds(t *testing.T) {
	a, ctx := delegationAgent(t)
	ctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "Test idor on /api/orders", VulnClass: "idor", Endpoint: "/api/orders",
		Origin: "auto-plan-surface", Status: scanctx.HypothesisQueued,
	})
	if got := a.formalSchedulableHypotheses(); len(got) != 0 {
		t.Fatalf("coverage seeds must not drive delegation: %+v", got)
	}
	ctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "GET /api/orders?id= as user returns another user's order", VulnClass: "idor",
		Endpoint: "/api/orders", Preconditions: []string{"a low-privilege session"},
		Origin: "coordinator", Status: scanctx.HypothesisQueued,
	})
	if got := a.formalSchedulableHypotheses(); len(got) != 1 {
		t.Fatalf("formal hypothesis must drive delegation: %+v", got)
	}
}

func TestHypothesisDrivenProfiles(t *testing.T) {
	a, ctx := delegationAgent(t)
	ctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "The id parameter reaches the SQL query", VulnClass: "sqli", Endpoint: "/api/orders",
		Preconditions: []string{"a low-privilege session"}, Origin: "coordinator", Status: scanctx.HypothesisQueued,
	})
	hyps := a.formalSchedulableHypotheses()
	roles := map[string]bool{}
	for _, p := range a.hypothesisDrivenProfiles(hyps) {
		roles[p.Role] = true
	}
	if !roles["injection-serverside"] {
		t.Fatalf("injection-serverside must launch for a SQLi belief: %v", roles)
	}
	if roles["client-source"] || roles["business-logic"] || roles["authz-logic"] {
		t.Fatalf("lanes without a belief must not launch: %v", roles)
	}
	if got := a.hypothesisDrivenProfiles(nil); len(got) != 0 {
		t.Fatalf("no hypotheses must mean no testing lanes, got %d", len(got))
	}
}

func TestSpecialistLaneBriefCarriesBeliefContract(t *testing.T) {
	a, ctx := delegationAgent(t)
	h := ctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "GET /api/orders?id= as user returns another user's order", VulnClass: "idor",
		Endpoint: "/api/orders", Preconditions: []string{"a low-privilege session"},
		Baseline: "user B gets 403", Falsification: "user B always gets 403",
		Origin: "coordinator", Status: scanctx.HypothesisQueued,
	})
	ctx.Ledger.AddEvidence(h.ID, scanctx.Evidence{Kind: "baseline", Nature: scanctx.EvidenceObserved, Summary: "control"})
	seed := ctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "Test idor on /api/legacy", VulnClass: "idor", Endpoint: "/api/legacy",
		Origin: "auto-plan-surface", Status: scanctx.HypothesisQueued,
	})
	profile := specialistProfile{
		Role: "authz-logic", VulnClasses: []string{"idor"},
		EvidenceContract: "role differential", StoppingRule: "exhaust the lane",
	}
	brief, ids := a.specialistLaneBrief(profile)
	for _, want := range []string{h.ID, "claim:", "baseline:", "falsify:", "evidence:", "Write-back contract", "add_hypothesis_evidence", "info_item_id/intent_id"} {
		if !strings.Contains(brief, want) {
			t.Errorf("lane brief missing %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, seed.ID) {
		t.Fatalf("coverage seed must not be assigned to a lane:\n%s", brief)
	}
	if len(ids) != 1 || ids[0] != h.ID {
		t.Fatalf("lane hypothesis ids = %v, want [%s]", ids, h.ID)
	}
}

func TestProfileReadinessReportsMissingTools(t *testing.T) {
	a, _ := delegationAgent(t)
	if ready, missing := a.profileReadiness(specialistProfile{Role: "binary", RequiredTools: []string{"rizin"}}); ready || len(missing) != 1 {
		t.Fatalf("missing tool must block the lane: ready=%v missing=%v", ready, missing)
	}
	if ready, _ := a.profileReadiness(specialistProfile{Role: "normal"}); !ready {
		t.Fatal("a lane with no declared tools is ready")
	}
}
