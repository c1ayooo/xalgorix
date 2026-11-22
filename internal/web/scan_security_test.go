package web

// scan_security_test.go — pins the /api/scans/{id}/security extraction:
// guard refusals are categorized from journal markers, plain events stay
// out, counters zero-fill the full category set, and chronological order
// is preserved within the newest-first cap.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanSecurityBlocksExtraction(t *testing.T) {
	s := newTestServer(t, nil)
	slug := "sec-probe-record"
	writeScanRecordForTest(t, s, "sec.example", slug, &ScanRecord{
		ID: slug, InstanceID: slug, Target: "https://sec.example", Status: "running",
	})
	dir := filepath.Join(s.dataDir, "sec.example", "2026-01-01", slug)
	journal := strings.Join([]string{
		`{"type":"message","content":"recon underway","timestamp":"2026-11-22T19:00:00+08:00"}`,
		`{"type":"tool_result","tool_name":"python_action","output":"⛔ OUT-OF-SCOPE TARGET BLOCKED — \"1.2.7.0.0.0.1\" points at the operator's machine. Refusing to probe localhost.","timestamp":"2026-11-22T19:01:00+08:00"}`,
		`{"type":"tool_result","tool_name":"terminal_execute","output":"ok normal result","timestamp":"2026-11-22T19:02:00+08:00"}`,
		`{"type":"message","content":"⛔ GOAL OBLIGATION — the operator's objective is not yet achieved; finish is refused.\nOpen acceptance criteria:\n- C1","timestamp":"2026-11-22T19:03:00+08:00"}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, scanEventJournalFile), []byte(journal), 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/scans/"+slug+"/security", nil)
	s.handleScanSecurity(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("security endpoint code=%d body=%s", rr.Code, rr.Body.String())
	}
	var view scanSecurityView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(view.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (refusals only, plain events excluded): %#v", len(view.Blocks), view.Blocks)
	}
	if view.Blocks[0].Category != "scopeguard" || view.Blocks[0].Tool != "python_action" {
		t.Fatalf("first block wrong: %#v", view.Blocks[0])
	}
	if !strings.Contains(view.Blocks[0].Reason, "1.2.7.0.0.0.1") {
		t.Fatalf("scopeguard reason lost the variant host: %q", view.Blocks[0].Reason)
	}
	if view.Blocks[1].Category != "goal" {
		t.Fatalf("second block category = %q, want goal", view.Blocks[1].Category)
	}
	if view.Counters["scopeguard"] != 1 || view.Counters["goal"] != 1 {
		t.Fatalf("counters wrong: %#v", view.Counters)
	}
	if view.Counters["other"] != 0 {
		t.Fatalf("other counter should zero-fill at 0, got %d", view.Counters["other"])
	}
}

func TestScanSecurityEmptyJournal(t *testing.T) {
	s := newTestServer(t, nil)
	slug := "sec-empty-record"
	writeScanRecordForTest(t, s, "sec.example", slug, &ScanRecord{
		ID: slug, InstanceID: slug, Target: "https://sec.example", Status: "running",
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/scans/"+slug+"/security", nil)
	s.handleScanSecurity(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	var view scanSecurityView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(view.Blocks) != 0 || view.Truncated {
		t.Fatalf("empty journal must yield empty non-truncated view: %#v", view)
	}
}
