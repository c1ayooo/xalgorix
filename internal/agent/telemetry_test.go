package agent

import "testing"

func TestTelemetryStatsReportsNarrationLossAndUncooperativeTimeouts(t *testing.T) {
	a := &Agent{}
	a.droppedTelemetry.Add(3)
	a.uncooperativeTimeouts.Add(2)
	got := a.TelemetryStats()
	want := "telemetry: dropped_narration=3 uncooperative_timeouts=2\n"
	if got != want {
		t.Fatalf("TelemetryStats = %q, want %q", got, want)
	}
	if a.DroppedNarration() != 3 || a.UncooperativeTimeouts() != 2 {
		t.Fatalf("counters exposed incorrectly: %d/%d", a.DroppedNarration(), a.UncooperativeTimeouts())
	}
}
