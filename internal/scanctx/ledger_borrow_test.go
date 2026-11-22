package scanctx

import "testing"

// TestClaimNextUnassignedBorrowsUnownedWork pins the idle-worker borrow path:
// a lane whose own classes are exhausted may take another lane's UNTAKEN work,
// which keeps a live worker busy instead of idling while the surface still has
// unclaimed hypotheses.
func TestClaimNextUnassignedBorrowsUnownedWork(t *testing.T) {
	ls := NewLedgerStore()
	owned := ls.Upsert(Hypothesis{VulnClass: "sqli", Endpoint: "/search"})
	if !ls.MarkAssigned(owned.ID, "sub_lane_a") {
		t.Fatal("mark assigned failed")
	}
	free := ls.Upsert(Hypothesis{VulnClass: "xss", Endpoint: "/comment"})

	borrowed, ok, err := ls.ClaimNextUnassignedAs("sub_lane_b", modelActor("sub_lane_b"))
	if err != nil {
		t.Fatalf("borrow claim: %v", err)
	}
	if !ok {
		t.Fatal("expected to claim unowned work")
	}
	if borrowed.ID != free.ID {
		t.Fatalf("borrowed %q, want unowned %q (never steal another lane's owner)", borrowed.ID, free.ID)
	}
	if borrowed.Status != HypothesisTesting || borrowed.AssignedTo != "sub_lane_b" {
		t.Fatalf("borrow did not take ownership: %#v", borrowed)
	}

	// The only remaining queued item belongs to lane A: borrowing must refuse.
	if got, ok, _ := ls.ClaimNextUnassignedAs("sub_lane_b", modelActor("sub_lane_b")); ok {
		t.Fatalf("stole another lane's owned work: %#v", got)
	}
	// Its owner can still claim it through the same path.
	if got, ok, _ := ls.ClaimNextUnassignedAs("sub_lane_a", modelActor("sub_lane_a")); !ok || got.ID != owned.ID {
		t.Fatalf("owner could not claim own work: ok=%v got=%#v", ok, got)
	}
}
