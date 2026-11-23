package web

// scan_units_test.go — B4 projection: ledger hypotheses surface as belief
// units with owner/lease, expired claims are flagged, and the endpoint
// degrades honestly when no plan was persisted.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanUnitsProjectsHypotheses(t *testing.T) {
	s := newTestServer(t, nil)
	slug := "units-record"
	writeScanRecordForTest(t, s, "units.example", slug, &ScanRecord{
		ID: slug, InstanceID: slug, Target: "https://units.example", Status: "running",
	})
	dir := filepath.Join(s.dataDir, "units.example", "2026-01-01", slug)
	past := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	ledger := `{"hypotheses":[
		{"id":"H-1","vuln_class":"sqli","endpoint":"/login","status":"testing","assigned_to":"worker-1","lease_until":"` + future + `"},
		{"id":"H-2","vuln_class":"xss","endpoint":"/search","status":"testing","assigned_to":"worker-2","lease_until":"` + past + `"},
		{"id":"H-3","vuln_class":"idor","endpoint":"/api/items","status":"queued"}
	]}`
	if err := os.WriteFile(filepath.Join(dir, ledgerFileName), []byte(ledger), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/scans/"+slug+"/units", nil)
	s.handleScanUnits(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var view scanUnitsView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(view.Units) != 3 {
		t.Fatalf("units = %d, want 3: %#v", len(view.Units), view.Units)
	}
	if view.Counters["hypotheses_testing"] != 2 || view.Counters["hypotheses_queued"] != 1 {
		t.Fatalf("counters wrong: %#v", view.Counters)
	}
	if view.Counters["hypotheses_expired"] != 1 {
		t.Fatalf("expired lease not flagged: %#v", view.Counters)
	}
	expired := 0
	for _, u := range view.Units {
		if u.Expired {
			expired++
			if u.ID != "H-2" {
				t.Fatalf("wrong unit flagged expired: %+v", u)
			}
		}
	}
	if expired != 1 {
		t.Fatalf("expired units = %d", expired)
	}
}
