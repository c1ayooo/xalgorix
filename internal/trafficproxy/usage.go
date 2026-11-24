package trafficproxy

// usage.go — aggregated egress-proxy usage per run.
//
// Kept even when traffic recording (MITM) is OFF: the platform still routes a
// run through its pinned egress, and operators need to know which run left
// through which proxy. Only masked labels and counters are stored — never
// packets or credentials. Rows are accumulated in memory and flushed to the
// configured sink (PostgreSQL) on an interval, on manager swaps and at Close.

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// EgressUsageSink persists aggregated usage. platform.Store implements it.
type EgressUsageSink interface {
	RecordEgressUsage(ctx context.Context, scanID, instanceID, label string, connections, requests int64) error
}

type egressUsageRow struct {
	scanID      string
	instanceID  string
	label       string
	connections int64
	requests    int64
	firstAt     time.Time
	lastAt      time.Time
}

var (
	usageMu   sync.Mutex
	usageRows = make(map[string]*egressUsageRow)
	usageSink atomic.Value // EgressUsageSink
)

// SetEgressUsageSink installs the persistence sink (nil disables flushing).
func SetEgressUsageSink(sink EgressUsageSink) {
	if sink == nil {
		usageSink.Store(egressUsageSinkBox{})
		return
	}
	usageSink.Store(egressUsageSinkBox{sink: sink})
}

type egressUsageSinkBox struct{ sink EgressUsageSink }

func (b egressUsageSinkBox) RecordEgressUsage(ctx context.Context, scanID, instanceID, label string, connections, requests int64) error {
	if b.sink == nil {
		return nil
	}
	return b.sink.RecordEgressUsage(ctx, scanID, instanceID, label, connections, requests)
}

// NoteEgressUsage accumulates one observation in memory. scanID is required;
// a missing label falls back to the run's non-consuming egress label.
func NoteEgressUsage(scanID, instanceID, label string, connections, requests int64) {
	if scanID == "" {
		return
	}
	if label == "" {
		label = "unknown"
	}
	now := time.Now().UTC()
	key := scanID + "\x00" + label
	usageMu.Lock()
	row := usageRows[key]
	if row == nil {
		row = &egressUsageRow{scanID: scanID, instanceID: instanceID, label: label, firstAt: now}
		usageRows[key] = row
	}
	row.connections += connections
	row.requests += requests
	row.lastAt = now
	if instanceID != "" {
		row.instanceID = instanceID
	}
	usageMu.Unlock()
}

// FlushEgressUsage writes the accumulated rows to the sink and clears them.
// A failed row is re-queued so a transient PG outage does not lose the audit.
func FlushEgressUsage(ctx context.Context) error {
	sink, _ := usageSink.Load().(egressUsageSinkBox)
	if sink.sink == nil {
		usageMu.Lock()
		usageRows = make(map[string]*egressUsageRow) // nothing to flush to
		usageMu.Unlock()
		return nil
	}
	usageMu.Lock()
	rows := usageRows
	usageRows = make(map[string]*egressUsageRow)
	usageMu.Unlock()

	var firstErr error
	for key, row := range rows {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := sink.RecordEgressUsage(writeCtx, row.scanID, row.instanceID, row.label, row.connections, row.requests)
		cancel()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			usageMu.Lock()
			if existing := usageRows[key]; existing != nil {
				existing.connections += row.connections
				existing.requests += row.requests
				if row.firstAt.Before(existing.firstAt) {
					existing.firstAt = row.firstAt
				}
				if row.lastAt.After(existing.lastAt) {
					existing.lastAt = row.lastAt
				}
			} else {
				usageRows[key] = row
			}
			usageMu.Unlock()
		}
	}
	return firstErr
}

// RunEgressUsageFlusher flushes every interval until ctx is cancelled.
func RunEgressUsageFlusher(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = FlushEgressUsage(context.Background())
			return
		case <-ticker.C:
			if err := FlushEgressUsage(ctx); err != nil {
				log.Printf("[trafficproxy] egress usage flush failed: %v", err)
			}
		}
	}
}
