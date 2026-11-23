package agent

// plan_claim_test.go — pins the plan-task claim protocol: deterministic order
// (phase, then insertion), exclusivity (successive claims take successive
// tasks), and lease expiry returning an active task to pending.

import (
	"testing"
	"time"
)

func claimTestPlan() *Plan {
	p := NewPlan()
	p.add(&Task{ID: "t6", Title: "XSS", Phase: 6, VulnClass: "xss", Status: TaskPending})
	p.add(&Task{ID: "t2", Title: "Recon", Phase: 2, Status: TaskPending})
	p.add(&Task{ID: "t9", Title: "Upload", Phase: 9, VulnClass: "upload", Status: TaskPending})
	return p
}

func TestClaimNextDeterministicOrder(t *testing.T) {
	p := claimTestPlan()
	now := time.Now().UTC()
	first := p.ClaimNext("w1", time.Minute, now)
	if first == nil || first.ID != "t2" {
		t.Fatalf("first claim = %+v, want t2 (lowest phase)", first)
	}
	second := p.ClaimNext("w2", time.Minute, now)
	if second == nil || second.ID != "t6" {
		t.Fatalf("second claim = %+v, want t6", second)
	}
	if first.Owner != "w1" || second.Owner != "w2" || first.Status != TaskActive {
		t.Fatalf("claim bookkeeping wrong: %+v / %+v", first, second)
	}
}

func TestClaimNextExclusiveAndExpires(t *testing.T) {
	p := NewPlan()
	p.add(&Task{ID: "only", Title: "SQLi", Phase: 6, VulnClass: "sqli", Status: TaskPending})
	now := time.Now().UTC()
	got := p.ClaimNext("w1", 10*time.Millisecond, now)
	if got == nil || got.ID != "only" {
		t.Fatalf("claim failed: %+v", got)
	}
	if again := p.ClaimNext("w2", time.Minute, now); again != nil {
		t.Fatalf("same task claimed twice: %+v", again)
	}
	// Lease expiry: reclaim returns it to pending, then w2 can claim it.
	later := now.Add(20 * time.Millisecond)
	got2 := p.ClaimNext("w2", time.Minute, later)
	if got2 == nil || got2.ID != "only" || got2.Owner != "w2" {
		t.Fatalf("expired lease not reclaimed for w2: %+v", got2)
	}
}

func TestTaskSubject(t *testing.T) {
	if s := taskSubject(&Task{Phase: 6, VulnClass: "SQLi"}); s != "phase:6:sqli" {
		t.Fatalf("class subject = %q", s)
	}
	if s := taskSubject(&Task{Endpoint: "HTTP://10.0.0.9:8080/x", VulnClass: "sqli"}); s != "endpoint:http://10.0.0.9:8080/x" {
		t.Fatalf("endpoint subject = %q", s)
	}
}
