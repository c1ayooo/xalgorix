package trafficproxy

import (
	"context"
	"testing"
	"time"
)

func TestConfigureForwardOnlyEnablesRoutingWithoutRecording(t *testing.T) {
	t.Cleanup(Reset)
	if err := ConfigureForwardOnly("127.0.0.1:0", ""); err != nil {
		t.Fatalf("ConfigureForwardOnly: %v", err)
	}
	if !Enabled() {
		t.Fatal("forward-only router must report Enabled so consumers route through it")
	}
	if MITMEnabled() {
		t.Fatal("forward-only router must not intercept TLS")
	}
	m := defaultManager.Load()
	if m == nil {
		t.Fatal("no manager")
	}
	got := make(chan TrafficObservation, 1)
	m.sink.Store(NewBufferedSink(func(o TrafficObservation) { got <- o }, 4))
	m.observe(&TrafficObservation{ScanCtxID: "run-1"})
	select {
	case <-got:
		t.Fatal("forward-only mode enqueued an observation (recording must stay off)")
	case <-time.After(80 * time.Millisecond):
	}
}

type captureUsageSink struct {
	rows [][5]any
}

func (c *captureUsageSink) RecordEgressUsage(_ context.Context, scanID, instanceID, label string, connections, requests int64) error {
	c.rows = append(c.rows, [5]any{scanID, instanceID, label, connections, requests})
	return nil
}

func TestEgressUsageAggregatesAndFlushes(t *testing.T) {
	SetEgressUsageSink(nil)
	_ = FlushEgressUsage(context.Background()) // clear any residue
	t.Cleanup(func() { SetEgressUsageSink(nil); _ = FlushEgressUsage(context.Background()) })

	NoteEgressUsage("scan-1", "inst-1", "#2", 1, 2)
	NoteEgressUsage("scan-1", "inst-1", "#2", 2, 3)
	NoteEgressUsage("scan-1", "inst-1", "direct", 0, 1)

	sink := &captureUsageSink{}
	SetEgressUsageSink(sink)
	if err := FlushEgressUsage(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(sink.rows) != 2 {
		t.Fatalf("rows = %d, want 2 (%v)", len(sink.rows), sink.rows)
	}
	for _, row := range sink.rows {
		if row[0] != "scan-1" {
			t.Fatalf("scan id = %v", row[0])
		}
		if row[2] == "#2" && (row[3] != int64(3) || row[4] != int64(5)) {
			t.Fatalf("aggregated counts = %v/%v, want 3/5", row[3], row[4])
		}
	}
	// A second flush after clearing emits nothing.
	if err := FlushEgressUsage(context.Background()); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if len(sink.rows) != 2 {
		t.Fatalf("second flush duplicated rows: %v", sink.rows)
	}
}
