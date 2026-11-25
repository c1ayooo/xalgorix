package scanctx

import (
	"testing"
	"time"
)

func TestProgressSnapshotAnalysisArtifactIsNotStalled(t *testing.T) {
	p := NewProgressStore()
	p.SetProfile("a", ProfileBinary)
	p.MarkSignal("a", SignalArtifact)
	snap := p.SnapshotFor("a", time.Now().Add(-45*time.Minute), time.Now())
	if snap.Status != ProgressAnalysisProgress {
		t.Fatalf("status = %s reason=%s, want analysis_progress", snap.Status, snap.Reason)
	}
	if snap.Signals[string(SignalArtifact)].Weight != 1 {
		t.Fatalf("binary artifact weight = %+v", snap.Signals)
	}
	if snap.LastSignal.IsZero() {
		t.Fatal("typed signal did not advance LastSignal")
	}
}

func TestProgressSnapshotBeliefAndCoverageCountAsProgress(t *testing.T) {
	for _, signal := range []ProgressSignal{SignalBelief, SignalEvidence, SignalCoverage, SignalWorkflow, SignalFinding} {
		p := NewProgressStore()
		p.MarkSignal("a", signal)
		if last := p.LastSignalAt("a"); last.IsZero() {
			t.Fatalf("%s: LastSignalAt is zero", signal)
		}
		snap := p.SnapshotFor("a", time.Now().Add(-20*time.Minute), time.Now())
		if snap.Status == ProgressStalled {
			t.Fatalf("%s: status stalled despite fresh typed progress: %+v", signal, snap)
		}
	}
}

func TestProgressSnapshotEnvironmentBlocked(t *testing.T) {
	p := NewProgressStore()
	for i := 0; i < 3; i++ {
		p.MarkFailure("a", "terminal_execute", "dial tcp: connection refused")
	}
	snap := p.SnapshotFor("a", time.Now().Add(-time.Minute), time.Now())
	if snap.Status != ProgressEnvironmentBlocked || snap.ConsecutiveFailures != 3 || snap.LastFailureClass != "unreachable" {
		t.Fatalf("snapshot = %+v", snap)
	}
	// A successful result clears the blocked classification.
	p.ClearFailures("a")
	snap = p.SnapshotFor("a", time.Now().Add(-time.Minute), time.Now())
	if snap.Status == ProgressEnvironmentBlocked || snap.ConsecutiveFailures != 0 {
		t.Fatalf("after clear = %+v", snap)
	}
}

func TestProgressSnapshotStalledAfterWindow(t *testing.T) {
	p := NewProgressStore()
	p.SetProfile("a", ProfileWeb)
	snap := p.SnapshotFor("a", time.Now().Add(-45*time.Minute), time.Now())
	if snap.Status != ProgressStalled || snap.StallAfter != 20*time.Minute {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestProgressSnapshotActiveTool(t *testing.T) {
	p := NewProgressStore()
	p.BeginTool("a", "terminal_execute")
	snap := p.SnapshotFor("a", time.Now().Add(-time.Hour), time.Now())
	if snap.Status != ProgressActiveTool || snap.ActiveTool != "terminal_execute" {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestMarkAnalysisDedupesSameReference(t *testing.T) {
	p := NewProgressStore()
	p.MarkAnalysis("a", "read_info_item|E-1")
	p.MarkAnalysis("a", "read_info_item|E-1")
	snap := p.SnapshotFor("a", time.Now().Add(-time.Minute), time.Now())
	if got := snap.Signals[string(SignalAnalysis)].Count; got != 1 {
		t.Fatalf("analysis count = %d, want 1 for the same reference", got)
	}
	p.MarkAnalysis("a", "read_info_item|E-2")
	snap = p.SnapshotFor("a", time.Now().Add(-time.Minute), time.Now())
	if got := snap.Signals[string(SignalAnalysis)].Count; got != 2 {
		t.Fatalf("analysis count = %d, want 2 for a new reference", got)
	}
}

func TestProfileWindow(t *testing.T) {
	cases := map[ProgressProfile]time.Duration{
		ProfileDefault:    20 * time.Minute,
		ProfileWeb:        20 * time.Minute,
		ProfileProtocol:   20 * time.Minute,
		ProfileBinary:     30 * time.Minute,
		ProfileMultistage: 25 * time.Minute,
	}
	for profile, want := range cases {
		if got := WindowForProfile(profile); got != want {
			t.Fatalf("%s window = %s, want %s", profile, got, want)
		}
	}
}
