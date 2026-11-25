package scanctx

import (
	"testing"
	"time"
)

func TestSpinObservationCountsActionsSinceSignal(t *testing.T) {
	p := NewProgressStore()
	started := time.Now().Add(-time.Minute)
	for i := 0; i < 4; i++ {
		p.ObserveToolAction("worker-1")
		p.ObserveToolOutcome("worker-1", "curl|10.0.0.9:8443|refused")
	}
	snap := p.SpinSnapshotFor("worker-1", started, time.Now())
	if snap.Signals != 0 {
		t.Fatalf("signals = %d, want 0", snap.Signals)
	}
	if snap.ActionsSinceProgress != 4 {
		t.Fatalf("actions = %d, want 4", snap.ActionsSinceProgress)
	}
	if snap.MaxRepeat != 4 {
		t.Fatalf("max repeat = %d, want 4", snap.MaxRepeat)
	}
	if snap.LastSignature != "curl|10.0.0.9:8443|refused" {
		t.Fatalf("last signature = %q", snap.LastSignature)
	}

	// A real information-layer mutation starts a new epoch and clears the
	// action counter; the repeated-signature history stays visible.
	p.MarkMutation("worker-1", SignalEvidence)
	snap = p.SpinSnapshotFor("worker-1", started, time.Now())
	if snap.Signals != 1 {
		t.Fatalf("signals after mutation = %d, want 1", snap.Signals)
	}
	if snap.ActionsSinceProgress != 0 {
		t.Fatalf("actions after signal = %d, want 0", snap.ActionsSinceProgress)
	}
	if snap.MaxRepeat != 4 {
		t.Fatalf("max repeat history lost: %d", snap.MaxRepeat)
	}
}

func TestSpinObservationIgnoresEmptyAgent(t *testing.T) {
	p := NewProgressStore()
	p.ObserveToolAction("")
	p.ObserveToolOutcome("", "x")
	snap := p.SpinSnapshotFor("", time.Now(), time.Now())
	if snap.ActionsSinceProgress != 0 || snap.Signals != 0 {
		t.Fatalf("empty agent recorded: %+v", snap)
	}
}
