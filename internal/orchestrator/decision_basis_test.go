package orchestrator

import (
	"context"
	"fmt"
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

func TestDecisionBasisAcceptsGraphNodeIDsAndCriteria(t *testing.T) {
	store := graph.NewMemoryStore()
	sctx := scanctx.New("basis-graph", t.TempDir())
	t.Cleanup(sctx.Close)
	node, err := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: "basis-graph", Kind: graph.KindFact, UnitKey: "f-1",
		State: graph.StateConfirmed, Payload: `{"summary":"live route observed"}`,
	})
	if err != nil || node == nil {
		t.Fatalf("seed node: %v", err)
	}
	p := &Planner{
		cfg: PlannerConfig{
			ScanID: "basis-graph", Repo: store, ScanCtx: sctx,
			ObjectiveCriteria: []PlannerCriterion{{ID: "C1"}},
		},
		ctx: context.Background(),
	}
	if bad := p.invalidDecisionBasis([]string{"C1", "GOAL", fmt.Sprint(node.ID)}); bad != "" {
		t.Fatalf("criterion/GOAL/graph node must be valid, rejected %q", bad)
	}
	if bad := p.invalidDecisionBasis([]string{"999999"}); bad == "" {
		t.Fatal("a nonexistent graph id must be rejected")
	}
}
