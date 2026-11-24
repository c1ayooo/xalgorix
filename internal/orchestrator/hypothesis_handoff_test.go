package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func handoffFixture(t *testing.T) *scanctx.ScanContext {
	t.Helper()
	sctx := scanctx.New("handoff-"+t.Name(), "")
	formal := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "GET /api/orders?id= as user returns another user's order", VulnClass: "idor",
		Endpoint: "/api/orders", Preconditions: []string{"a low-privilege session"},
		Origin: "coordinator", Confidence: 0.7, Status: scanctx.HypothesisQueued,
		NextAction: "swap order_id to another user's order and compare",
	})
	seed := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "Test idor on /api/legacy-orders", VulnClass: "idor", Endpoint: "/api/legacy-orders",
		Origin: "auto-plan-surface", Confidence: 0.5, Status: scanctx.HypothesisQueued,
	})
	if formal.ID == "" || seed.ID == "" || formal.ID == seed.ID {
		t.Fatalf("fixture ids: formal=%q seed=%q", formal.ID, seed.ID)
	}
	return sctx
}

func TestWorkerBeliefHandoffSurfacesFormalOnly(t *testing.T) {
	sctx := handoffFixture(t)
	block := workerBeliefHandoff(sctx, "idor", "/api/orders", "10.0.0.5:80")
	if block == "" {
		t.Fatal("matching formal hypothesis must be handed to the worker")
	}
	if !strings.Contains(block, "H-1") || !strings.Contains(block, "claim_next_hypothesis") {
		t.Fatalf("handoff missing claim path:\n%s", block)
	}
	if strings.Contains(block, "auto-plan-surface") {
		t.Fatalf("coverage seeds must never be handed off:\n%s", block)
	}
	if strings.Contains(block, "H-2") {
		t.Fatalf("seed row ID must not appear in the handoff:\n%s", block)
	}
	if got := workerBeliefHandoff(sctx, "ssrf", "/fetch", "10.0.0.5:80"); got != "" {
		t.Fatalf("non-matching intent must not receive a handoff:\n%s", got)
	}
}

func TestPlannerLedgerListFormalAndSeeds(t *testing.T) {
	sctx := handoffFixture(t)
	p := &Planner{
		cfg: PlannerConfig{ScanID: sctx.ID, ScanCtx: sctx, Repo: graph.NewMemoryStore()},
		ctx: context.Background(),
	}
	sched := p.ledgerList("schedulable", "10")
	if !strings.Contains(sched, "H-1") || !strings.Contains(sched, "claim") {
		t.Fatalf("schedulable listing missing the formal claim:\n%s", sched)
	}
	all := p.ledgerList("all", "10")
	if !strings.Contains(all, "coverage seed") || !strings.Contains(all, "NOT claimable") {
		t.Fatalf("all listing must mark coverage seeds as not claimable:\n%s", all)
	}
}
