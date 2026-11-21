package scanctx

import (
	"math"
	"testing"
)

// Boundary hardening for the VOI ranking weight (charter §7.2): the weight is
// clamped to [0, MaxVOIWeight] for every non-finite or out-of-range input.
func TestVOIWeightRejectsNonFiniteValues(t *testing.T) {
	ls := NewLedgerStore()
	for _, w := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0.30 + 1e-9} {
		ls.SetVOIWeight(w)
		if got := ls.VOIWeight(); got != 0 && got != MaxVOIWeight {
			t.Fatalf("SetVOIWeight(%v) stored %v, want 0 or %v", w, got, MaxVOIWeight)
		}
	}
	ls.SetVOIWeight(math.Nextafter(MaxVOIWeight, 0))
	if got := ls.VOIWeight(); got <= 0 || got > MaxVOIWeight {
		t.Fatalf("weight just below the cap = %v, want (0, %v]", got, MaxVOIWeight)
	}
}

// A NaN/Inf VOI estimate must not poison the score: it is clamped into [0,1]
// on write, and the audit record carries the effective value.
func TestSetVOIAsClampsNonFiniteEstimates(t *testing.T) {
	ls := NewLedgerStore()
	h := ls.Upsert(Hypothesis{VulnClass: "ssrf", Endpoint: "/fetch"})
	for _, voi := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 2} {
		if err := ls.SetVOIAs(h.ID, voi, true, AuditActorForAgent("agent_1")); err != nil {
			t.Fatalf("SetVOIAs(%v): %v", voi, err)
		}
		got, ok := ls.Get(h.ID)
		if !ok {
			t.Fatal("hypothesis vanished")
		}
		if math.IsNaN(got.VOI) || math.IsInf(got.VOI, 0) || got.VOI < 0 || got.VOI > 1 || !got.VOIEstimated {
			t.Fatalf("SetVOIAs(%v) stored voi=%v estimated=%v, want clamped [0,1] with estimated=true", voi, got.VOI, got.VOIEstimated)
		}
	}
}

// Marking an estimate unestimated must zero the VOI so stale estimates cannot
// keep steering the ranking after an operator/model retraction.
func TestSetVOIAsUnestimatedZeroesScore(t *testing.T) {
	ls := NewLedgerStore()
	h := ls.Upsert(Hypothesis{VulnClass: "sqli", Endpoint: "/items"})
	if err := ls.SetVOIAs(h.ID, 0.9, true, AuditActorForAgent("agent_1")); err != nil {
		t.Fatal(err)
	}
	if err := ls.SetVOIAs(h.ID, 0, false, AuditActorForAgent("agent_1")); err != nil {
		t.Fatal(err)
	}
	got, _ := ls.Get(h.ID)
	if got.VOI != 0 || got.VOIEstimated {
		t.Fatalf("unestimated VOI = %v/%v, want 0/false", got.VOI, got.VOIEstimated)
	}
}
