package runstate

// DSN-gated integration test for the scanctx.LedgerBackend implementation
// (R6.1): snapshot save/load round-trip and the D11 instance continuity read.

import (
	"context"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// Compile-time pin: the runstate store is the production scanctx ledger
// backend (main registers it at startup).
var _ scanctx.LedgerBackend = (*Store)(nil)

func TestLedgerBackendSaveLoadContinuity(t *testing.T) {
	store := integrationStore(t)

	snapshot := []byte(`{"seq":5,"ev_seq":2,"order":["H-1"],"hypotheses":[{"id":"H-1","dedup_key":"k|/a","title":"carried","status":"testing"}]}`)

	if err := store.SaveLedgerSnapshot("scan-cont-a", "inst-cont", 5, snapshot); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, seq, ok, err := store.LoadLedgerSnapshot("scan-cont-a")
	if err != nil || !ok {
		t.Fatalf("load own snapshot: ok=%v err=%v", ok, err)
	}
	if seq != 5 || string(data) != string(snapshot) {
		t.Fatalf("snapshot round-trip mismatch: seq=%d data=%s", seq, data)
	}

	// Continuity: a NEW scan id of the same instance inherits the previous
	// session's snapshot; excluding itself never returns its own row.
	data2, _, source, ok2, err := store.LoadLatestLedgerSnapshotForInstance("inst-cont", "scan-cont-b")
	if err != nil || !ok2 {
		t.Fatalf("continuity load: ok=%v err=%v", ok2, err)
	}
	if source != "scan-cont-a" || string(data2) != string(snapshot) {
		t.Fatalf("continuity snapshot from %s: %s", source, data2)
	}
	if _, _, _, ok3, _ := store.LoadLatestLedgerSnapshotForInstance("inst-cont", "scan-cont-a"); ok3 {
		t.Fatalf("exclude-self returned a row")
	}

	// The failure path (failed watermark on save error) is covered by the
	// scanctx fake-backend test (save error surfaces to the mutation caller);
	// here we pin the watermark row itself is writable for the ledger kind.
	if err := store.Watermark(context.Background(), "scan-wm", "ledger", "inst-cont", 5, StatusApplied, ""); err != nil {
		t.Fatalf("watermark write: %v", err)
	}
}
