package terminal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func enableJournalLedger(t *testing.T) (string, string) {
	t.Helper()
	ctxID := "term-obs-" + t.Name()
	sc := scanctx.New(ctxID, "")
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(ctxID) })
	sc.Ledger.SetScanID("scan-" + t.Name())
	dir := t.TempDir()
	if err := sc.Ledger.EnableJournal(dir); err != nil {
		t.Fatalf("enable journal: %v", err)
	}
	return ctxID, dir
}

func readJournal(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read journal: %v", err)
			}
			return string(data)
		}
	}
	return ""
}

func TestObserveSensitiveOutputCapturesServedEnvFile(t *testing.T) {
	ctxID, dir := enableJournalLedger(t)
	output := "200  /                      len=96\n200  /.env                  len=118\n200  /backup.sql            len=123\n"
	observeSensitiveOutput(ctxID, "for p in ...; do curl -o /dev/null -w '%{http_code}'; done", output)

	journal := readJournal(t, dir)
	if !strings.Contains(journal, "tool_observation") {
		t.Fatalf("expected a tool_observation record, journal=%s", journal)
	}
	if !strings.Contains(journal, "sensitive_artifact_served") {
		t.Fatalf("expected sensitive_artifact_served purpose, journal=%s", journal)
	}
	if !strings.Contains(journal, "env_file") {
		t.Fatalf("expected env_file pattern label, journal=%s", journal)
	}
	// The subject must be the matched line, not the first line of output: a
	// reader of the item has to see what was actually served.
	if !strings.Contains(journal, `"identifier":"200  /.env`) {
		t.Fatalf("expected the matched line as subject, journal=%s", journal)
	}
}

func TestObserveSensitiveOutputMasksCredentials(t *testing.T) {
	ctxID, dir := enableJournalLedger(t)
	const key = "AKIAFAKEFAKEFAKEFAKE"
	output := "AWS_ACCESS_KEY_ID=" + key + "\nDB_PASSWORD=sup3rs3cret\n"
	observeSensitiveOutput(ctxID, "cat .env", output)

	journal := readJournal(t, dir)
	if journal == "" {
		t.Fatal("expected an observation record")
	}
	if strings.Contains(journal, key) {
		t.Fatalf("raw access key leaked into the journal: %s", journal)
	}
	if strings.Contains(journal, "sup3rs3cret") {
		t.Fatalf("raw password leaked into the journal: %s", journal)
	}
	if !strings.Contains(journal, "credential_material_observed") && !strings.Contains(journal, "credential_assignment") {
		t.Fatalf("expected a credential observation, journal=%s", journal)
	}
}

func TestObserveSensitiveOutputIgnoresBenignOutput(t *testing.T) {
	ctxID, dir := enableJournalLedger(t)
	observeSensitiveOutput(ctxID, "curl -I http://127.0.0.1:8901/", "HTTP/1.0 200 OK\r\nServer: SimpleHTTP/0.6\r\n")
	if journal := readJournal(t, dir); strings.Contains(journal, "tool_observation") {
		t.Fatalf("benign output must not create observations: %s", journal)
	}
}
