package runstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// writeFixtures builds a minimal pre-R6 scan directory tree and returns the
// scan directory (the data root is three levels up: <data>/<target>/<date>/).
func writeFixtures(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "resume.example", "2026-05-01", "scan-old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("scan.json", `{"id":"scan-old","instance_id":"inst-old","status":"finished","target":"resume.example","started_at":"2026-05-01T18:00:00Z","finished_at":"2026-05-01T19:00:00Z","current_phase":3,"plan_tasks_total":10,"plan_tasks_completed":4}`)
	write("events.jsonl", strings.Join([]string{
		`{"type":"message","content":"old turn","timestamp":"2026-05-01T18:05:00Z"}`,
		`not-json-on-purpose`,
		`{"type":"tool_call","tool_name":"http_request","content":"GET /admin","timestamp":"2026-05-01T18:06:00Z"}`,
	}, "\n")+"\n")
	write("ledger.json", `{"seq":7,"order":["H-1"],"hypotheses":[{"id":"H-1","title":"t","status":"queued"}]}`)
	write("intelligence.json", `{"version":1,"seq":1,"items":[]}`)
	write(domainjournal.FileName, `{"scan_id":"scan-old","seq":1,"at":"2026-05-01T18:05:30Z","actor":{"kind":"engine"},"operation":"claim_hypothesis","hypothesis_id":"H-1","summary":"claim"}`+"\n")

	// Noise that must be skipped by the walker.
	if err := os.MkdirAll(filepath.Join(filepath.Dir(dir), ".dispatches"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), ".dispatches", "inst-old.json"), []byte(`{"instance_id":"inst-old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScanDirsSkipsDotAndUnderscoreTrees(t *testing.T) {
	dataDir := t.TempDir()
	scanDir := filepath.Join(dataDir, "t.example", "2026-01-01", "scan-a")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scanDir, "scan.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, noise := range []string{".dispatches", "_platform"} {
		if err := os.MkdirAll(filepath.Join(dataDir, noise), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dirs, err := scanDirs(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || filepath.Base(dirs[0]) != "scan-a" {
		t.Fatalf("scanDirs = %v, want only scan-a", dirs)
	}
}

func TestParseEventsSkipsDamagedLines(t *testing.T) {
	dir := writeFixtures(t)
	rows, err := parseEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("events = %d rows, want 2 (damaged line skipped, order preserved)", len(rows))
	}
	// seq = 1-based durable line index of the KEPT rows in order.
	for i, row := range rows {
		if row.Seq != uint64(i+1) {
			t.Fatalf("row %d seq = %d, want %d", i, row.Seq, i+1)
		}
	}
	if rows[0].Type != "message" || rows[1].Type != "tool_call" {
		t.Fatalf("event types = %s/%s", rows[0].Type, rows[1].Type)
	}
	if rows[0].OccurredAt.IsZero() {
		t.Fatalf("occurred_at not parsed from timestamp: %v", rows[0])
	}
}

func TestParseScanStateTypedFields(t *testing.T) {
	dir := writeFixtures(t)
	detail, err := parseScanState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if detail.InstanceID != "inst-old" || detail.LegacyScanID != "scan-old" {
		t.Fatalf("identity = %+v", detail)
	}
	if detail.Status != "finished" || detail.CurrentPhase != 3 || detail.PlanTotal != 10 || detail.PlanCompleted != 4 {
		t.Fatalf("typed fields = %+v", detail)
	}
	if detail.StartedAt == nil || detail.StartedAt.Year() != 2026 || detail.FinishedAt == nil {
		t.Fatalf("timestamps = %+v", detail)
	}
	if len(detail.Raw) == 0 || !strings.Contains(string(detail.Raw), "resume.example") {
		t.Fatalf("raw payload lost: %d bytes", len(detail.Raw))
	}
}

func TestParseLedgerAndJournal(t *testing.T) {
	dir := writeFixtures(t)
	raw, seq, ok, err := parseLedger(dir)
	if err != nil || !ok {
		t.Fatalf("ledger ok=%v err=%v", ok, err)
	}
	if seq != 7 || !strings.Contains(string(raw), `"H-1"`) {
		t.Fatalf("ledger seq=%d raw=%s", seq, raw)
	}
	rows, err := parseJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Seq != 1 {
		t.Fatalf("journal rows = %+v", rows)
	}
	if !strings.Contains(string(rows[0].Payload), "claim_hypothesis") {
		t.Fatalf("journal payload lost: %s", rows[0].Payload)
	}
}
