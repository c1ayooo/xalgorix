package scanctx

// ledger_lease_test.go — pins the deterministic claim protocol's lease:
// claim sets a future lease, activity renews it, expiry reclaims the
// hypothesis to queued with the owner cleared, and a reclaimed unit is
// immediately claimable again.

import (
	"testing"
	"time"
)

func leaseTestStore(t *testing.T) *LedgerStore {
	t.Helper()
	ls := NewLedgerStore()
	ls.SetLeaseTTL(50 * time.Millisecond)
	if _, err := ls.UpsertAs(Hypothesis{
		Title: "SQLi on /login", VulnClass: "sqli", Endpoint: "/login",
	}, engineActor("test")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return ls
}

func TestClaimSetsLease(t *testing.T) {
	ls := leaseTestStore(t)
	h, ok, err := ls.ClaimNextAs("", "worker-1", engineActor("test"))
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if h.LeaseUntil.IsZero() || !h.LeaseUntil.After(time.Now()) {
		t.Fatalf("claim lease not in the future: %v", h.LeaseUntil)
	}
	if h.AssignedTo != "worker-1" || h.Status != HypothesisTesting {
		t.Fatalf("claim state wrong: %+v", h)
	}
}

func TestExpiredLeaseReclaimed(t *testing.T) {
	ls := leaseTestStore(t)
	if _, ok, _ := ls.ClaimNextAs("", "worker-1", engineActor("test")); !ok {
		t.Fatal("seed claim failed")
	}
	time.Sleep(60 * time.Millisecond)
	// Any claim call runs the deterministic reclaim first; there is nothing
	// else queued, so the reclaimed unit is claimed again — by worker-2.
	h, ok, err := ls.ClaimNextAs("", "worker-2", engineActor("test"))
	if err != nil || !ok {
		t.Fatalf("reclaim+reclaim: ok=%v err=%v", ok, err)
	}
	if h.AssignedTo != "worker-2" {
		t.Fatalf("expired lease was not reclaimed: owner=%q", h.AssignedTo)
	}
	got, found := ls.Get(h.ID)
	if !found || got.Status != HypothesisTesting || got.AssignedTo != "worker-2" {
		t.Fatalf("reclaimed-hypothesis state wrong: %+v", got)
	}
}

func TestActivityRenewsLease(t *testing.T) {
	ls := leaseTestStore(t)
	h, ok, _ := ls.ClaimNextAs("", "worker-1", engineActor("test"))
	if !ok {
		t.Fatal("claim failed")
	}
	first := h.LeaseUntil
	time.Sleep(60 * time.Millisecond)
	if err := ls.RecordAttemptAs(h.ID, engineActor("test")); err != nil {
		t.Fatalf("record attempt: %v", err)
	}
	got, _ := ls.Get(h.ID)
	if !got.LeaseUntil.After(first) {
		t.Fatalf("activity did not renew the lease: %v -> %v", first, got.LeaseUntil)
	}
	if got.Status != HypothesisTesting {
		t.Fatalf("record attempt changed status: %s", got.Status)
	}
}
