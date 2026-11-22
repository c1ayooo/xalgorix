package agent

import (
	"strings"
	"testing"
)

// TestClaimNextHypothesis_SpecialistBorrowsUnownedWork pins the idle-worker
// borrow path end-to-end: a specialist whose own classes are exhausted claims
// another lane's UNOWNED work instead of idling, and the output says so.
// The coordinator's strict class-filter contract is pinned separately by
// TestClaimNextHypothesis_NothingClaimable.
func TestClaimNextHypothesis_SpecialistBorrowsUnownedWork(t *testing.T) {
	ag := newClaimAgent(t)
	ag.delegatedAgentID = "sub_lane_b"
	l := ag.scanCtx.Ledger
	borrowID := seedQueued(l, "xss", "/comment", 0.7)

	res, err := ag.claimNextHypothesisTool(map[string]string{"vuln_class": "sqli"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("borrow claim failed: %s", res.Error)
	}
	got, _ := res.Metadata["hypothesis_id"].(string)
	if got != borrowID {
		t.Fatalf("claimed %q, want borrowed %q", got, borrowID)
	}
	if !strings.Contains(res.Output, "Borrowed unclaimed work from another lane") {
		t.Fatalf("borrow provenance missing from output:\n%s", res.Output)
	}
	h, ok := l.Get(borrowID)
	if !ok || h.AssignedTo != "sub_lane_b" {
		t.Fatalf("borrow did not take ownership: %#v", h)
	}
}
