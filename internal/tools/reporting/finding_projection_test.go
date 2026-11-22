package reporting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// journalRecords reads the domain journal a scan wrote, in order.
func journalRecords(t *testing.T, dir string) []map[string]any {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read journal dir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read journal: %v", err)
		}
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("parse journal line: %v", err)
			}
			out = append(out, rec)
		}
		return out
	}
	t.Fatal("no journal file found")
	return nil
}

func newJournalLedger(t *testing.T) (string, *scanctx.ScanContext, string) {
	t.Helper()
	ctxID := "report-proj-" + t.Name()
	sc := scanctx.New(ctxID, "")
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(ctxID) })
	sc.Ledger.SetScanID("scan-" + t.Name())
	dir := t.TempDir()
	if err := sc.Ledger.EnableJournal(dir); err != nil {
		t.Fatalf("enable journal: %v", err)
	}
	return ctxID, sc, dir
}

func TestProjectFindingInfoItemWritesObservedRecord(t *testing.T) {
	ctxID, _, dir := newJournalLedger(t)
	vuln := Vulnerability{
		ID:          "XALG-9",
		Title:       "Unauthenticated .env exposure",
		Severity:    "high",
		CVSS:        7.5,
		Target:      "http://127.0.0.1:8901",
		Endpoint:    "http://127.0.0.1:8901/.env",
		Description: "The web root serves .env with credentials.",
		Verified:    true,
	}
	projectFindingInfoItem(ctxID, vuln)

	recs := journalRecords(t, dir)
	var found map[string]any
	for _, r := range recs {
		if r["operation"] == "finding_recorded" {
			found = r
		}
	}
	if found == nil {
		t.Fatalf("no finding_recorded record in journal: %+v", recs)
	}
	item, ok := found["info_item"].(map[string]any)
	if !ok {
		t.Fatalf("finding_recorded carries no info_item: %+v", found)
	}
	if item["nature"] != "observed" {
		t.Fatalf("expected nature=observed, got %v", item["nature"])
	}
	coords := item["coordinates"].(map[string]any)
	if coords["purpose"] != "finding" {
		t.Fatalf("expected purpose=finding, got %v", coords["purpose"])
	}
	subject := coords["subject"].(map[string]any)
	if subject["identifier"] != vuln.Endpoint {
		t.Fatalf("expected subject %q, got %v", vuln.Endpoint, subject["identifier"])
	}
}

func TestEnsureFindingLedgerEvidenceLinksQueuedHypothesis(t *testing.T) {
	ctxID, sc, _ := newJournalLedger(t)
	// A hypothesis seeded during recon is still queued when the finding lands.
	h := sc.Ledger.Upsert(scanctx.Hypothesis{
		VulnClass: "file_disclosure",
		Endpoint:  "http://127.0.0.1:8901/.env",
		Status:    scanctx.HypothesisQueued,
	})
	vuln := Vulnerability{
		ID:       "XALG-10",
		Title:    "env exposure",
		Target:   "http://127.0.0.1:8901",
		Endpoint: "http://127.0.0.1:8901/.env",
	}
	if note := ensureFindingLedgerEvidence(ctxID, vuln, "lfi"); note == "" {
		t.Fatal("expected a link note")
	}
	got, ok := sc.Ledger.Get(h.ID)
	if !ok {
		t.Fatalf("hypothesis %s disappeared", h.ID)
	}
	if got.Status != scanctx.HypothesisProven {
		t.Fatalf("expected proven, got %q", got.Status)
	}
	if len(got.Evidence) == 0 || got.Evidence[0].FindingID != "XALG-10" {
		t.Fatalf("expected finding_ref evidence, got %+v", got.Evidence)
	}
}

func TestEnsureFindingLedgerEvidenceCreatesHypothesisWhenNoneMatches(t *testing.T) {
	ctxID, sc, _ := newJournalLedger(t)
	before := sc.Ledger.Len()
	vuln := Vulnerability{
		ID:       "XALG-11",
		Title:    "exposed backup",
		Target:   "http://127.0.0.1:8901",
		Endpoint: "http://127.0.0.1:8901/backup.sql",
	}
	if note := ensureFindingLedgerEvidence(ctxID, vuln, "lfi"); note == "" {
		t.Fatal("expected a creation note")
	}
	if sc.Ledger.Len() != before+1 {
		t.Fatalf("expected one created hypothesis, len %d -> %d", before, sc.Ledger.Len())
	}
	if sc.Ledger.EvidenceCount() == 0 {
		t.Fatal("expected evidence after projection")
	}
}
