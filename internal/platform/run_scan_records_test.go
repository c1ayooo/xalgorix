package platform

// Unit tests for the R6.1 "full runs state sweep" query API (REDESIGN D12:
// the runs table takes over scan.json). The FileStore is the no-DSN/test
// implementation; its snapshot round-trip pins the shared contract (keyset
// cursor, scan_id order, verbatim detail). The PG implementation's SQL
// behavior is pinned by the DSN-gated integration tests
// (postgres_integration_test.go gains the same assertions under
// XALGORIX_TEST_DATABASE_URL).

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreScanRunRecordsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Empty store sweeps empty — the file-mode web tests' fallback posture.
	rows, next, err := store.ScanRunRecords(ctx, "", 10)
	if err != nil {
		t.Fatalf("empty sweep: %v", err)
	}
	if len(rows) != 0 || next != "" {
		t.Fatalf("empty sweep = %d rows / next %q, want 0 / \"\"", len(rows), next)
	}

	updatedAt := time.Date(2026, 5, 1, 18, 0, 0, 0, time.UTC)
	for _, rec := range []RunScanRecord{
		{ScanID: "scan-b", InstanceID: "inst-1", ScanDir: "t.example/2026-05-01/scan-b", Detail: json.RawMessage(`{"id":"scan-b","status":"finished"}`), UpdatedAt: updatedAt},
		{ScanID: "scan-a", InstanceID: "inst-1", ScanDir: "t.example/2026-05-01/scan-a", Detail: json.RawMessage(`{"id":"scan-a","status":"running"}`)},
		{ScanID: "scan-c", InstanceID: "", ScanDir: "", Detail: json.RawMessage(`{"id":"scan-c"}`), UpdatedAt: updatedAt.Add(time.Hour)},
	} {
		if err := store.PutRunScanRecord(ctx, rec); err != nil {
			t.Fatalf("put %s: %v", rec.ScanID, err)
		}
	}

	// Keyset order: scan_id ascending regardless of write order.
	rows, _, err = store.ScanRunRecords(ctx, "", 10)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("sweep = %d rows, want 3", len(rows))
	}
	for i, want := range []string{"scan-a", "scan-b", "scan-c"} {
		if rows[i].ScanID != want {
			t.Fatalf("row %d = %s, want %s", i, rows[i].ScanID, want)
		}
	}
	if rows[0].InstanceID != "inst-1" || rows[0].ScanDir != "t.example/2026-05-01/scan-a" {
		t.Fatalf("row 0 identity = %+v", rows[0])
	}
	var detail map[string]any
	if err := json.Unmarshal(rows[0].Detail, &detail); err != nil {
		t.Fatalf("detail decode: %v", err)
	}
	if detail["status"] != "running" {
		t.Fatalf("detail status = %v, want running (verbatim)", detail["status"])
	}

	// Cursor pagination: first page feeds the next call until exhaustion.
	page1, next, err := store.ScanRunRecords(ctx, "", 2)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 2 || next != "scan-b" {
		t.Fatalf("page 1 = %d rows / next %q, want 2 / scan-b", len(page1), next)
	}
	page2, next, err := store.ScanRunRecords(ctx, next, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ScanID != "scan-c" || next != "" {
		t.Fatalf("page 2 = %+v / next %q, want scan-c / \"\"", page2, next)
	}

	// Upsert replaces the row (the safe point's idempotent save).
	if err := store.PutRunScanRecord(ctx, RunScanRecord{
		ScanID:     "scan-b",
		InstanceID: "inst-1",
		ScanDir:    "t.example/2026-05-01/scan-b",
		Detail:     json.RawMessage(`{"id":"scan-b","status":"stopped"}`),
		UpdatedAt:  updatedAt.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows, _, _ = store.ScanRunRecords(ctx, "", 10)
	if len(rows) != 3 {
		t.Fatalf("post-upsert sweep = %d rows, want 3", len(rows))
	}
	var got map[string]any
	if err := json.Unmarshal(rows[1].Detail, &got); err != nil {
		t.Fatalf("post-upsert detail decode: %v", err)
	}
	if got["status"] != "stopped" {
		t.Fatalf("post-upsert status = %v, want stopped", got["status"])
	}

	// Missing scan_id is a validation error, not a silent write.
	if err := store.PutRunScanRecord(ctx, RunScanRecord{Detail: json.RawMessage(`{}`)}); err == nil {
		t.Fatalf("put without scan_id must fail")
	}

	// The snapshot round-trips rows across reopen (persistence, not just memory).
	reopened, err := NewFileStore(filepath.Dir(filepath.Dir(store.path)))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rows, _, err = reopened.ScanRunRecords(ctx, "", 10)
	if err != nil {
		t.Fatalf("reopened sweep: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("reopened sweep = %d rows, want 3", len(rows))
	}
	// The file snapshot is an indented JSON document, so the embedded raw
	// payload is re-indented (semantically verbatim, not byte-verbatim — the
	// PG TEXT column is the byte-fidelity home).
	if err := json.Unmarshal(rows[1].Detail, &got); err != nil {
		t.Fatalf("reopened detail decode: %v", err)
	}
	if got["status"] != "stopped" {
		t.Fatalf("reopened status = %v, want stopped", got["status"])
	}
}
