package scanctx

import (
	"testing"
)

func seedHypothesis(l *LedgerStore, origin, class, endpoint string, pre []string) Hypothesis {
	return l.Upsert(Hypothesis{
		Title: "claim about " + class, VulnClass: class, Endpoint: endpoint,
		Preconditions: pre, Origin: origin, Confidence: 0.5, Status: HypothesisQueued,
	})
}

func TestCoverageSeedsAreNotClaimable(t *testing.T) {
	l := NewLedgerStore()
	for _, origin := range []string{"auto", "auto-plan", "auto-plan-surface", "auto-surface-param", "engine", "recon"} {
		h := seedHypothesis(l, origin, "sqli", "/"+origin, nil)
		if h.ClaimEligible() {
			t.Errorf("origin %q must not be claim-eligible", origin)
		}
	}
	if got := l.Schedulable(10); len(got) != 0 {
		t.Fatalf("coverage seeds must not enter the schedulable pool, got %d", len(got))
	}
	if _, claimed, err := l.ClaimNextAs("", "worker", AuditActorForAgent("worker")); err != nil || claimed {
		t.Fatalf("coverage seeds must not be claimable: claimed=%v err=%v", claimed, err)
	}
	if l.Len() != 6 {
		t.Fatalf("seeds must remain in the ledger as audit history, len=%d", l.Len())
	}
}

func TestFormalHypothesesAreClaimable(t *testing.T) {
	l := NewLedgerStore()
	model := seedHypothesis(l, "coordinator", "sqli", "/api/users", []string{"unknown"})
	legacy := seedHypothesis(l, "", "xss", "/search", []string{"authenticated session"})
	source := l.Upsert(Hypothesis{
		Title: "Source sink reaches db.query", VulnClass: "sqli", Endpoint: "app.py:42",
		DataFlow: "req.args.id -> db.query", Origin: "source-sink", Status: HypothesisQueued,
	})
	for _, h := range []Hypothesis{model, legacy, source} {
		if !h.ClaimEligible() {
			t.Errorf("%s (%s) must be claim-eligible", h.ID, h.Origin)
		}
	}
	if got := l.Schedulable(10); len(got) != 3 {
		t.Fatalf("schedulable = %d, want 3", len(got))
	}
	got, claimed, err := l.ClaimNextAs("", "worker", AuditActorForAgent("worker"))
	if err != nil || !claimed {
		t.Fatalf("claim failed: %v %v", claimed, err)
	}
	if !got.ClaimEligible() {
		t.Fatalf("claimed an ineligible row: %+v", got)
	}
}

func TestModelRecordingPromotesSeed(t *testing.T) {
	l := NewLedgerStore()
	seed := seedHypothesis(l, "auto-plan-surface", "idor", "/api/orders", nil)
	if seed.ClaimEligible() {
		t.Fatal("seed must start ineligible")
	}
	upgraded := l.Upsert(Hypothesis{
		Title:     "GET /api/orders?id= as user returns another user's order",
		VulnClass: "idor", Endpoint: "/api/orders",
		Preconditions: []string{"a low-privilege session"},
		Origin:        "coordinator", Confidence: 0.7, Status: HypothesisQueued,
	})
	if upgraded.ID != seed.ID {
		t.Fatalf("same dedup key must refine the seed, got %s vs %s", upgraded.ID, seed.ID)
	}
	if !upgraded.ClaimEligible() {
		t.Fatalf("promoted hypothesis must be claim-eligible: %+v", upgraded)
	}
	stored, _ := l.Get(seed.ID)
	if stored.Origin != "coordinator" || stored.Title == "" || len(stored.Preconditions) == 0 {
		t.Fatalf("promotion did not persist the formal fields: %+v", stored)
	}
}

func TestCompetingHypothesesAndRejection(t *testing.T) {
	l := NewLedgerStore()
	idor := seedHypothesis(l, "coordinator", "idor", "/api/orders", []string{"a low-privilege session"})
	xss := seedHypothesis(l, "coordinator", "xss", "/api/orders", []string{"a rendered page"})
	if got := l.Schedulable(10); len(got) != 2 {
		t.Fatalf("competing hypotheses must both stay schedulable, got %d", len(got))
	}
	if err := l.SetStatusAs(idor.ID, HypothesisRejected, "control returned 403", AuditActorForAgent("coordinator")); err != nil {
		t.Fatal(err)
	}
	got := l.Schedulable(10)
	if len(got) != 1 || got[0].ID != xss.ID {
		t.Fatalf("a rejected hypothesis must leave the pool; got %+v", got)
	}
	claimed, ok, err := l.ClaimNextAs("", "worker", AuditActorForAgent("worker"))
	if err != nil || !ok || claimed.ID != xss.ID {
		t.Fatalf("claim after rejection = %+v ok=%v err=%v", claimed, ok, err)
	}
}
