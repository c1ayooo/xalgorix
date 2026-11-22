package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// ledgerTestScan mirrors auditTestScan: a registered scan record so
// resolveScanDirByID finds the directory.
func ledgerTestScan(t *testing.T, s *Server, id string) string {
	t.Helper()
	dir := filepath.Join(s.dataDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := &ScanRecord{
		ID:        id,
		Target:    "https://example.com",
		Status:    "running",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.saveScanRecordTo(rec, dir)
	return dir
}

func ledgerGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	s.handleScanLedger(rr, req)
	return rr
}

// The projection must serve the on-disk ledger authority: hypotheses,
// evidence (with nature), and zero-filled status counts (charter §5/§6.3 —
// read-only projection of the state authority, not an event replay).
func TestScanLedgerEndpoint_ProjectionsHypotheses(t *testing.T) {
	s := newTestServer(t, nil)
	id := "ledger-proj-1"
	dir := ledgerTestScan(t, s, id)

	store := scanctx.NewLedgerStore()
	store.SetPersistPath(dir)
	now := time.Now().UTC()
	h1 := store.Upsert(scanctx.Hypothesis{
		Title:     "SQLi on login",
		VulnClass: "sqli",
		Target:    "https://example.com",
		Endpoint:  "https://example.com/login",
		Parameter: "username",
		Status:    scanctx.HypothesisQueued,
		CreatedAt: now,
		UpdatedAt: now,
	})
	h2 := store.Upsert(scanctx.Hypothesis{
		Title:     "IDOR on invoice",
		VulnClass: "idor",
		Target:    "https://example.com",
		Endpoint:  "https://example.com/invoice/1",
		Status:    scanctx.HypothesisQueued,
		CreatedAt: now,
		UpdatedAt: now,
	})
	h3 := store.Upsert(scanctx.Hypothesis{
		Title:     "Reflected XSS",
		VulnClass: "xss",
		Target:    "https://example.com",
		Endpoint:  "https://example.com/search",
		Parameter: "q",
		Status:    scanctx.HypothesisQueued,
		CreatedAt: now,
		UpdatedAt: now,
	})
	// H-2 is being worked right now; H-3 settled as proven with evidence.
	store.SetStatus(h2.ID, scanctx.HypothesisTesting, "probe id param")
	store.SetStatus(h3.ID, scanctx.HypothesisProven, "")
	store.AddEvidence(h3.ID, scanctx.Evidence{
		Kind:       "exploit",
		Summary:    "payload reflected without encoding",
		Nature:     scanctx.EvidenceObserved,
		AgentID:    "spec-1",
		Source:     "verify_xss",
		Confidence: 0.9,
		CreatedAt:  now,
	})
	if store.Len() != 3 {
		t.Fatalf("fixture store len = %d, want 3", store.Len())
	}

	rr := ledgerGet(t, s, "/api/scans/"+id+"/ledger")
	if rr.Code != http.StatusOK {
		t.Fatalf("ledger code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view scanLedgerView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.LedgerPresent || view.LedgerDamaged || view.Incomplete {
		t.Fatalf("flags = present:%v damaged:%v incomplete:%v", view.LedgerPresent, view.LedgerDamaged, view.Incomplete)
	}
	if len(view.Hypotheses) != 3 {
		t.Fatalf("hypotheses = %d, want 3", len(view.Hypotheses))
	}
	// Display order: active (testing) → queued → settled (proven).
	if view.Hypotheses[0].ID != h2.ID || view.Hypotheses[1].ID != h1.ID || view.Hypotheses[2].ID != h3.ID {
		t.Fatalf("order = %s,%s,%s want %s,%s,%s (active first)",
			view.Hypotheses[0].ID, view.Hypotheses[1].ID, view.Hypotheses[2].ID, h2.ID, h1.ID, h3.ID)
	}
	if view.Counts["testing"] != 1 || view.Counts["queued"] != 1 || view.Counts["proven"] != 1 {
		t.Fatalf("counts = %+v, want testing:1 queued:1 proven:1", view.Counts)
	}
	// Every known bucket is zero-filled so the UI never infers absent keys.
	for _, st := range []string{"queued", "testing", "proven", "rejected", "blocked", "exhausted"} {
		if _, ok := view.Counts[st]; !ok {
			t.Fatalf("counts missing zero-filled bucket %q: %+v", st, view.Counts)
		}
	}
	// Evidence projection keeps nature and engine attribution.
	ev := view.Hypotheses[2].Evidence
	if len(ev) != 1 || ev[0].Nature != scanctx.EvidenceObserved || ev[0].Source != "verify_xss" {
		t.Fatalf("proven evidence = %+v, want observed verify_xss item", ev)
	}
}

// A missing ledger snapshot is honest absence, never an empty-but-complete
// answer (same discipline as the audit endpoint's missing journal).
func TestScanLedgerEndpoint_MissingLedgerIsIncomplete(t *testing.T) {
	s := newTestServer(t, nil)
	id := "ledger-absent-1"
	ledgerTestScan(t, s, id)

	rr := ledgerGet(t, s, "/api/scans/"+id+"/ledger")
	if rr.Code != http.StatusOK {
		t.Fatalf("ledger code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view scanLedgerView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.LedgerPresent || !view.Incomplete || view.Note == "" {
		t.Fatalf("absent ledger must be incomplete with note, got %+v", view)
	}
	if len(view.Hypotheses) != 0 {
		t.Fatalf("absent ledger must carry no hypotheses, got %d", len(view.Hypotheses))
	}
	if rr := ledgerGet(t, s, "/api/scans/does-not-exist/ledger"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown scan code = %d, want 404", rr.Code)
	}
}

// A snapshot that does not parse must be reported damaged — folding corruption
// into "zero hypotheses" would fabricate a clean ledger (charter §5.2).
func TestScanLedgerEndpoint_DamagedLedgerIsFlagged(t *testing.T) {
	s := newTestServer(t, nil)
	id := "ledger-damaged-1"
	dir := ledgerTestScan(t, s, id)
	if err := os.WriteFile(filepath.Join(dir, "ledger.json"), []byte(`{"seq":3,"hypotheses":[{"id":"H-1"`), 0o600); err != nil {
		t.Fatal(err)
	}

	rr := ledgerGet(t, s, "/api/scans/"+id+"/ledger")
	if rr.Code != http.StatusOK {
		t.Fatalf("ledger code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view scanLedgerView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.LedgerPresent || !view.LedgerDamaged || !view.Incomplete || view.Note == "" {
		t.Fatalf("damaged ledger flags = present:%v damaged:%v incomplete:%v note:%q",
			view.LedgerPresent, view.LedgerDamaged, view.Incomplete, view.Note)
	}
	if len(view.Hypotheses) != 0 {
		t.Fatalf("damaged ledger must not project partial hypotheses, got %d", len(view.Hypotheses))
	}
}

// An empty-but-valid snapshot reads as a present, clean, empty ledger (a run
// that started and claimed nothing yet is a real state, not damage).
func TestScanLedgerEndpoint_EmptySnapshotIsClean(t *testing.T) {
	s := newTestServer(t, nil)
	id := "ledger-empty-1"
	dir := ledgerTestScan(t, s, id)
	if err := os.WriteFile(filepath.Join(dir, "ledger.json"), []byte(`{"seq":0,"ev_seq":0,"order":[],"hypotheses":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rr := ledgerGet(t, s, "/api/scans/"+id+"/ledger")
	if rr.Code != http.StatusOK {
		t.Fatalf("ledger code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view scanLedgerView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.LedgerPresent || view.LedgerDamaged || view.Incomplete {
		t.Fatalf("empty snapshot must read clean, got %+v", view)
	}
}
