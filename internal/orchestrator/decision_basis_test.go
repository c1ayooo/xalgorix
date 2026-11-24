package orchestrator

import (
	"context"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestIntentDecisionBasisIsValidatedAndStored(t *testing.T) {
	store := graph.NewMemoryStore()
	sctx := scanctx.New("basis-test", t.TempDir())
	t.Cleanup(sctx.Close)
	h := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "Orders are enumerable across users", VulnClass: "idor", Endpoint: "/api/orders",
		Preconditions: []string{"a low-privilege session"}, Origin: "coordinator", Status: scanctx.HypothesisQueued,
	})
	p := &Planner{
		cfg: PlannerConfig{ScanID: "basis-test", Repo: store, ScanCtx: sctx, Target: "10.0.0.5:80"},
		ctx: context.Background(),
	}
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		Title: "Verify order-level authorization", VulnClass: "idor",
		Target: "10.0.0.5:80", Endpoint: "/api/orders", Phase: 8,
		DecisionBasis: plannerStrings{h.ID},
	}}})
	if added != 1 {
		t.Fatalf("valid decision basis must admit the intent, added=%d", added)
	}
	nodes, err := store.ListNodes(context.Background(), "basis-test", graph.KindIntent)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("intent nodes = %d err=%v", len(nodes), err)
	}
	refs, _ := nodes[0].PayloadJSON()["decision_basis_refs"].([]any)
	if len(refs) != 1 || refs[0] != h.ID {
		t.Fatalf("decision basis not stored in the intent payload: %#v", nodes[0].PayloadJSON()["decision_basis_refs"])
	}
}

func TestIntentUnknownDecisionBasisIsRejected(t *testing.T) {
	store := graph.NewMemoryStore()
	sctx := scanctx.New("basis-bad", t.TempDir())
	t.Cleanup(sctx.Close)
	p := &Planner{
		cfg: PlannerConfig{ScanID: "basis-bad", Repo: store, ScanCtx: sctx, Target: "10.0.0.5:80"},
		ctx: context.Background(),
	}
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		Title: "Verify order-level authorization", VulnClass: "idor",
		Target: "10.0.0.5:80", Endpoint: "/api/orders", Phase: 8,
		DecisionBasis: plannerStrings{"H-404"},
	}}})
	if added != 0 {
		t.Fatalf("an invented decision basis must be rejected, added=%d", added)
	}
}
