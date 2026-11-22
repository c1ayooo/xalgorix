package runstate

// PG round-trip integration test for the R6.0 importer: a fixture data dir
// goes in, the imported rows decode back to the source content. Skipped
// unless XALGORIX_TEST_DATABASE_URL is set (same gate as the infoitem
// postgres integration tests; CI gains a PG service with R6.1).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// Compile-time pins: the runstate store is the production persistence
// backend for the ledger, the state-file documents and the intelligence
// snapshots (main registers all three at startup).
var _ scanctx.LedgerBackend = (*Store)(nil)
var _ scanctx.StateFilesBackend = (*Store)(nil)
var _ intelligence.Backend = (*Store)(nil)

func integrationStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	store, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open runstate store: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestImportDataDirRoundTrip(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()

	// Re-arm the one-shot guard: the fixture scan id is stable, and the
	// watermark row persists on a shared test database (deleting it re-arms
	// the import — the documented mechanism).
	if _, err := store.pool.Exec(ctx, `DELETE FROM run_state_watermarks WHERE scan_id = $1`, "scan-old"); err != nil {
		t.Fatalf("re-arm import: %v", err)
	}

	// scanDirs skips dot-trees, so build the fixture under a data dir whose
	// scan dir name is addressable.
	scanDir := writeFixtures(t)
	dataDir := filepath.Dir(filepath.Dir(filepath.Dir(scanDir)))

	stats, err := store.ImportDataDir(ctx, dataDir)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stats.Scans != 1 || stats.Events != 2 || stats.JournalRows != 1 || stats.Ledgers != 1 || stats.StateFiles != 1 {
		t.Fatalf("stats = %+v", stats)
	}

	// Second pass skips (one-shot watermark guard).
	stats2, err := store.ImportDataDir(ctx, dataDir)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if stats2.Scans != 0 || stats2.Skipped != 1 {
		t.Fatalf("re-import stats = %+v, want skipped", stats2)
	}

	// Events round-trip: rows decode back to the source lines in order.
	var gotType string
	var gotPayload []byte
	if err := store.pool.QueryRow(ctx,
		`SELECT type, payload FROM run_events WHERE scan_id = $1 AND seq = 1`,
		"scan-old",
	).Scan(&gotType, &gotPayload); err != nil {
		t.Fatalf("read back event: %v", err)
	}
	if gotType != "message" || !strings.Contains(string(gotPayload), "old turn") {
		t.Fatalf("event round-trip = %s / %s", gotType, gotPayload)
	}

	// Journal round-trip: the record column is opaque TEXT (migration 0013);
	// the payload decodes wholesale into domainjournal.Record.
	var rec domainjournal.Record
	var jrSeq uint64
	var jrPayload []byte
	if err := store.pool.QueryRow(ctx,
		`SELECT seq, record FROM run_journal WHERE scan_id = $1`,
		"scan-old",
	).Scan(&jrSeq, &jrPayload); err != nil {
		t.Fatalf("read back journal: %v", err)
	}
	if rec, err = decodeJournalRecord(jrPayload); err != nil {
		t.Fatalf("decode journal payload: %v", err)
	}
	if jrSeq != 1 || rec.Operation != domainjournal.OpClaimHypothesis || rec.HypothesisID != "H-1" {
		t.Fatalf("journal round-trip = seq %d op %s hyp %s", jrSeq, rec.Operation, rec.HypothesisID)
	}

	// Ledger snapshot + state files.
	var ledgerSeq int64
	if err := store.pool.QueryRow(ctx,
		`SELECT seq FROM run_ledger_snapshots WHERE scan_id = $1`, "scan-old",
	).Scan(&ledgerSeq); err != nil {
		t.Fatalf("read back ledger: %v", err)
	}
	if ledgerSeq != 7 {
		t.Fatalf("ledger seq = %d, want 7", ledgerSeq)
	}
	var intel []byte
	if err := store.pool.QueryRow(ctx,
		`SELECT data FROM run_state_files WHERE scan_id = $1 AND kind = $2`,
		"scan-old", KindIntelligence,
	).Scan(&intel); err != nil {
		t.Fatalf("read back intelligence: %v", err)
	}
	if !strings.Contains(string(intel), `"items":[]`) {
		t.Fatalf("intelligence round-trip = %s", intel)
	}

	// Watermark landed as applied.
	imported, err := store.Imported(ctx, "scan-old")
	if err != nil || !imported {
		t.Fatalf("imported = %v err = %v", imported, err)
	}
}
