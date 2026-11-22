package web

// PG round-trip integration test for the R6.1 runs-table takeover (REDESIGN
// D12): the REAL production pairing — runstate.Store registered as the scan
// record + events backend, platform.Store sweeping the same database — driven
// through the web layer's save → summaries → projection path. Skipped unless
// XALGORIX_TEST_DATABASE_URL is set (the scan_event_journal_pg_integration
// precedent).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
)

func TestScanStateTakeoverPGEndToEnd(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	// The platform store construction applies the shared migrations (0016
	// included) before any backend writes. Close ordering: t.Cleanup runs
	// LIFO — the pool closes are registered first so they run last, after the
	// cleanups that still query through them.
	platformStore, err := platform.Open(ctx, platform.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("platform store: %v", err)
	}
	if concrete, ok := platformStore.(*platform.Store); ok {
		t.Cleanup(concrete.Close)
	}
	recordStore, err := runstate.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("runstate store: %v", err)
	}
	t.Cleanup(recordStore.Close)

	s := newTestServer(t, nil)
	s.SetPlatformStore(platformStore)
	SetScanStateBackend(recordStore)
	t.Cleanup(func() { SetScanStateBackend(nil) })
	SetEventStateBackend(recordStore)
	t.Cleanup(func() { SetEventStateBackend(nil) })

	dir := filepath.Join(s.dataDir, "pg.example", "2026-05-01", "scan-pg-e2e")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Full cleanup so repeat runs on the shared test database are
	// deterministic: record row, event rows, journal rows, watermarks.
	t.Cleanup(func() {
		_ = recordStore.DeleteScanRecord(ctx, "scan-pg-e2e")
		_ = recordStore.DeleteEventsByScan(ctx, "scan-pg-e2e")
		_ = recordStore.DeleteJournalByScan(ctx, "scan-pg-e2e")
		_, _ = recordStore.DeleteWatermarksByScan(ctx, "scan-pg-e2e")
	})

	rec := takeoverTestRecord("scan-pg-e2e", "inst-pg-e2e")
	s.saveScanRecordTo(rec, dir)

	// No state files; the durable row is the authority.
	if _, err := os.Stat(filepath.Join(dir, "scan.json")); !os.IsNotExist(err) {
		t.Fatalf("scan.json written with the backend registered (err=%v)", err)
	}

	// The shared test database may carry rows from other tests' fixtures;
	// assert on THIS record's row, never on the whole set.
	entries := s.findAllScanSummaries()
	var entry *scanEntry
	for i := range entries {
		if entries[i].rec.ID == "scan-pg-e2e" {
			entry = &entries[i]
			break
		}
	}
	if entry == nil || entry.dir != dir {
		t.Fatalf("PG summary entry missing: %+v", entries)
	}
	got := entry.rec
	if got.ID != "scan-pg-e2e" || got.InstanceID != "inst-pg-e2e" || got.Status != "running" ||
		got.PlanTasksTotal != 4 || got.PlanTasksCompleted != 2 || got.TotalTokens != 123 ||
		len(got.Vulns) != 1 {
		t.Fatalf("PG summary record drifted: %+v", got)
	}

	// The live event journal feeds the full read (production pairing: the
	// same store serves both seams).
	if err := appendScanEventJournal(dir, WSEvent{Type: "message", Content: "pg-one", Timestamp: "2026-05-01T18:01:00Z"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	full, ok := loadScanRecordFromDir(dir)
	if !ok || len(full.Events) != 1 || full.Events[0].Content != "pg-one" {
		t.Fatalf("PG full read = %+v ok=%v", full, ok)
	}
	detail, ok := s.loadScanRecordForDetail(dir, detailEventTail)
	if !ok || detail.ID != "scan-pg-e2e" || len(detail.Events) != 1 || detail.EventsTotal != 1 {
		t.Fatalf("PG detail read = %+v ok=%v", detail, ok)
	}

	// Projection over the PG-backed set.
	idx := s.scanSummaryIndexFor(entries)
	run := platform.Run{InstanceID: "inst-pg-e2e"}
	idx.projectRun(&run, nil)
	if run.Status != "running" || run.VulnCount != 1 || run.TotalTokens != 123 {
		t.Fatalf("PG projected run = %+v", run)
	}
	if bindDir, bindRec := s.scanDirRecordForRun("inst-pg-e2e", ""); bindDir != dir || bindRec == nil || bindRec.ID != "scan-pg-e2e" {
		t.Fatalf("PG scanDirRecordForRun = %s / %+v", bindDir, bindRec)
	}

	// Deletion removes the durable row.
	s.deleteScanRecordState(filepath.Base(dir), rec.ID)
	for _, e := range s.findAllScanSummaries() {
		if e.rec.ID == "scan-pg-e2e" {
			t.Fatalf("post-delete PG summaries still carry the record: %+v", e)
		}
	}
}
