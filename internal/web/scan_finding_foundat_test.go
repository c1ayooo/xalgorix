package web

// scan_finding_foundat_test.go — pins the found_at backfill that keeps the
// project findings page honest about a long-running assessment: persisted
// findings written before the field existed must be stamped from the durable
// event journal's report_vulnerability broadcasts, and a row that already has
// a value must never be overwritten.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackfillFindingFoundAtFromEventJournal(t *testing.T) {
	dir := t.TempDir()
	journal := strings.Join([]string{
		`{"type":"tool_result","tool_name":"report_vulnerability","timestamp":"2026-11-24T12:39:56+08:00","vulns":[{"id":"XALG-1","title":"old without found_at"}]}`,
		`{"type":"tool_result","tool_name":"report_vulnerability","timestamp":"2026-11-24T12:45:00+08:00","vulns":[{"id":"XALG-2","title":"journaling found_at","found_at":"2026-11-24T12:44:30+08:00"}]}`,
		`{"type":"message","content":"no vulns here","timestamp":"2026-11-24T12:46:00+08:00"}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, scanEventJournalFile), []byte(journal), 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	rec := &ScanRecord{
		ID: "scan-1",
		Vulns: []VulnSummary{
			{ID: "XALG-1", Title: "old without found_at"},
			{ID: "XALG-2", Title: "journaling found_at"},
			{ID: "XALG-3", Title: "absent from the journal"},
			{ID: "XALG-4", Title: "already stamped", FoundAt: "2026-11-24T12:40:00+08:00"},
		},
	}

	if stamped := backfillFindingFoundAt(rec, dir); stamped != 2 {
		t.Fatalf("backfill stamped %d findings, want 2", stamped)
	}
	if got, want := rec.Vulns[0].FoundAt, "2026-11-24T12:39:56+08:00"; got != want {
		t.Fatalf("XALG-1 found_at = %q, want the event timestamp %q", got, want)
	}
	if got, want := rec.Vulns[1].FoundAt, "2026-11-24T12:44:30+08:00"; got != want {
		t.Fatalf("XALG-2 found_at = %q, want the payload value %q (it wins over the event time)", got, want)
	}
	if got := rec.Vulns[2].FoundAt; got != "" {
		t.Fatalf("XALG-3 found_at = %q, want empty (no journal evidence)", got)
	}
	if got, want := rec.Vulns[3].FoundAt, "2026-11-24T12:40:00+08:00"; got != want {
		t.Fatalf("XALG-4 found_at = %q, want the existing value %q", got, want)
	}
}

func TestBackfillFindingFoundAtSkipsJournalWhenNothingMissing(t *testing.T) {
	// A fully stamped record must not read the journal at all: the missing-id
	// set is empty, so even a directory without a journal stays a no-op.
	rec := &ScanRecord{
		ID:    "scan-2",
		Vulns: []VulnSummary{{ID: "XALG-1", FoundAt: "2026-11-24T12:39:56+08:00"}},
	}
	if stamped := backfillFindingFoundAt(rec, t.TempDir()); stamped != 0 {
		t.Fatalf("backfill stamped %d findings, want 0", stamped)
	}
}
