package agent

import "testing"

// TestBudgetFairShareOnlyWithMultipleAgents pins the boundary: single-agent
// scans keep the exact configured cap (byte-identical behavior), while two live
// agents each get a floor left for the other so one lane cannot eat the shared
// tool-call budget.
func TestBudgetFairShareOnlyWithMultipleAgents(t *testing.T) {
	single := newScanBudget()
	single.enter()
	defer single.leave()
	if got := single.ceiling(100); got != 100 {
		t.Fatalf("single agent ceiling = %d, want 100", got)
	}
	if allowed := single.reserveToolCalls(100, 100); allowed != 100 {
		t.Fatalf("single agent reserved %d, want 100", allowed)
	}

	shared := newScanBudget()
	shared.enter() // agent A
	shared.enter() // agent B
	defer shared.leave()
	defer shared.leave()
	// cap 100, 2 live agents → floor 33, each may reach 67.
	if got := shared.ceiling(100); got != 67 {
		t.Fatalf("two-agent ceiling = %d, want 67", got)
	}
	if allowed := shared.reserveToolCalls(80, 100); allowed != 67 {
		t.Fatalf("first agent reserved %d, want 67 (floor kept for the other)", allowed)
	}
	if allowed := shared.reserveToolCalls(80, 100); allowed != 0 {
		t.Fatalf("second reservation = %d, want 0 (agent A hit its share)", allowed)
	}

	// Unlimited caps stay unlimited regardless of sharing.
	if allowed := shared.reserveToolCalls(5, 0); allowed != 5 {
		t.Fatalf("unlimited reservation = %d, want 5", allowed)
	}
}

// TestBudgetIterationFairShare keeps the iteration cap bounded the same way.
func TestBudgetIterationFairShare(t *testing.T) {
	b := newScanBudget()
	b.enter()
	b.enter()
	defer b.leave()
	defer b.leave()
	// cap 9, 2 agents → floor 3, ceiling 6.
	granted := 0
	for i := 0; i < 12; i++ {
		if b.reserveIteration(9) {
			granted++
		}
	}
	if granted != 6 {
		t.Fatalf("granted %d iterations, want 6 (share ceiling)", granted)
	}
}
