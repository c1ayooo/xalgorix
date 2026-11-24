package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// trafficTestScan registers a scan and seeds its traffic archive with the
// exchanges the MITM proxy would have written (unredacted wire packets).
func trafficTestScan(t *testing.T, s *Server, id string, exchanges []scanctx.TrafficExchange) string {
	t.Helper()
	dir := auditTestScan(t, s, id)
	archive := scanctx.NewTrafficArchive(dir)
	for i := range exchanges {
		ex := exchanges[i]
		if _, err := archive.Append(&ex); err != nil {
			t.Fatalf("seed exchange %d: %v", i, err)
		}
	}
	return dir
}

func trafficGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleScanTraffic(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// The endpoint is a pure projection of the per-scan traffic archive: filters
// by endpoint substring + method, caps the newest window, and distinguishes
// an empty archive (present=false) from an empty match (present=true).
func TestScanTrafficEndpoint_ProjectionAndFilters(t *testing.T) {
	s := newTestServer(t, nil)
	id := "traffic-proj-1"
	base := time.Now().UTC()
	trafficTestScan(t, s, id, []scanctx.TrafficExchange{
		{At: base, Method: "GET", URL: "https://target.example/api/users", Status: 200, Channel: "bearer", RequestRaw: "GET /api/users HTTP/1.1\r\n\r\n", ResponseRaw: "HTTP/1.1 200 OK\r\n\r\n[]"},
		{At: base.Add(time.Second), Method: "POST", URL: "https://target.example/api/user/update", Status: 200, Channel: "subprocess", HighRisk: "destructive payload feature: drop database", RequestRaw: "POST /api/user/update HTTP/1.1\r\n\r\nsql=DROP DATABASE prod", ResponseRaw: "HTTP/1.1 200 OK\r\n\r\nok"},
		{At: base.Add(2 * time.Second), Method: "POST", URL: "https://other.example/login", Status: 302, Channel: "bearer", RequestRaw: "POST /login HTTP/1.1\r\n\r\np=x", ResponseRaw: "HTTP/1.1 302 Found\r\n\r\n"},
	})

	// Unfiltered: newest window (default limit 20 covers all three here).
	rr := trafficGet(t, s, "/api/scans/"+id+"/traffic")
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view scanTrafficView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Present || view.TotalMatched != 3 || len(view.Exchanges) != 3 {
		t.Fatalf("unfiltered view = present:%v total:%d n:%d", view.Present, view.TotalMatched, len(view.Exchanges))
	}

	// The wire packets come back verbatim — the whole point of the archive.
	if !strings.Contains(view.Exchanges[1].RequestRaw, "DROP DATABASE prod") {
		t.Fatalf("request raw not projected verbatim: %q", view.Exchanges[1].RequestRaw)
	}

	// Endpoint substring match (a finding's endpoint).
	rr = trafficGet(t, s, "/api/scans/"+id+"/traffic?endpoint=/api/user&method=POST")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.TotalMatched != 1 || len(view.Exchanges) != 1 || view.Exchanges[0].Channel != "subprocess" {
		t.Fatalf("endpoint+method match = %+v", view)
	}
	if !strings.Contains(view.Exchanges[0].HighRisk, "drop database") {
		t.Fatalf("high-risk reason lost: %q", view.Exchanges[0].HighRisk)
	}

	// Newest-window limit: 1 of 3, ascending order preserved.
	rr = trafficGet(t, s, "/api/scans/"+id+"/traffic?limit=1")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Truncated || view.TotalMatched != 3 || len(view.Exchanges) != 1 || view.Exchanges[0].Seq != 3 {
		t.Fatalf("limit window = truncated:%v total:%d seq:%d", view.Truncated, view.TotalMatched, view.Exchanges[0].Seq)
	}

	// Method-only filter is case-insensitive.
	rr = trafficGet(t, s, "/api/scans/"+id+"/traffic?method=get")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.TotalMatched != 1 || view.Exchanges[0].Method != "GET" {
		t.Fatalf("case-insensitive method match = %+v", view)
	}

	// Empty match on a present archive.
	rr = trafficGet(t, s, "/api/scans/"+id+"/traffic?endpoint=/nope")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Present || view.TotalMatched != 0 || len(view.Exchanges) != 0 {
		t.Fatalf("empty match = %+v", view)
	}

	// Every exchange carries its derived asset host; host is an exact,
	// case-insensitive join key (asset ↔ traffic evidence binding).
	rr = trafficGet(t, s, "/api/scans/"+id+"/traffic")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Exchanges[0].Host != "target.example" || view.Exchanges[2].Host != "other.example" {
		t.Fatalf("derived host missing: %q %q", view.Exchanges[0].Host, view.Exchanges[2].Host)
	}
	rr = trafficGet(t, s, "/api/scans/"+id+"/traffic?host=TARGET.example")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.TotalMatched != 2 || len(view.Exchanges) != 2 {
		t.Fatalf("host filter = total:%d n:%d", view.TotalMatched, len(view.Exchanges))
	}
	// Host + endpoint compose, so one asset's packet never masquerades as
	// another asset's evidence.
	rr = trafficGet(t, s, "/api/scans/"+id+"/traffic?host=other.example&endpoint=/api/user")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.TotalMatched != 0 {
		t.Fatalf("cross-asset match must be empty: %+v", view)
	}
}

// A scan whose traffic archive never existed (pre-R2b layout, proxy off) must
// read as present=false — a different honest empty state — and an unknown
// scan id stays 404.
func TestScanTrafficEndpoint_MissingArchiveAndUnknownScan(t *testing.T) {
	s := newTestServer(t, nil)
	id := "traffic-absent-1"
	auditTestScan(t, s, id)

	rr := trafficGet(t, s, "/api/scans/"+id+"/traffic")
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view scanTrafficView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Present || view.TotalMatched != 0 || view.Exchanges == nil {
		t.Fatalf("absent archive must be present=false with empty exchanges: %+v", view)
	}

	if rr := trafficGet(t, s, "/api/scans/does-not-exist/traffic"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown scan code = %d, want 404", rr.Code)
	}
	if rr := trafficGet(t, s, "/api/scans//traffic"); rr.Code != http.StatusBadRequest {
		t.Fatalf("missing id code = %d, want 400", rr.Code)
	}
}

// The archive directory survives scan-context teardown by design; this pins
// that the handler reads the FILES, never a live ScanContext (findings are
// viewed after the scan ended).
func TestScanTrafficEndpoint_ReadsFilesNotLiveContext(t *testing.T) {
	s := newTestServer(t, nil)
	id := "traffic-files-1"
	dir := trafficTestScan(t, s, id, []scanctx.TrafficExchange{
		{At: time.Now().UTC(), Method: "DELETE", URL: "https://target.example/api/items/9", Status: 204, Channel: "bearer", RequestRaw: "DELETE /api/items/9 HTTP/1.1\r\n\r\n", ResponseRaw: "HTTP/1.1 204 No Content\r\n\r\n"},
	})

	// Sanity: the seeded file is on disk under the scan dir.
	if _, err := os.Stat(filepath.Join(dir, "traffic", "tr_000001.json")); err != nil {
		t.Fatalf("archive file missing: %v", err)
	}

	rr := trafficGet(t, s, "/api/scans/"+id+"/traffic?endpoint=/api/items/9")
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view scanTrafficView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.TotalMatched != 1 || view.Exchanges[0].Method != "DELETE" {
		t.Fatalf("file-backed match = %+v", view)
	}
}

// A run can own several sibling session directories (checkpoint restore /
// re-dispatch) and each session restarts its archive sequence. The run-scoped
// read must merge them, expose the owning session on every row, and let a
// finding fetch its exact persisted (session, seq) evidence refs.
func TestScanTrafficEndpoint_MergesSiblingSessionsAndRefs(t *testing.T) {
	s := newTestServer(t, nil)
	instanceID := "traffic-instance-1"
	parent := filepath.Join(s.dataDir, "target.example", "2026-01-01")
	olderDir := filepath.Join(parent, "target.example_older")
	newerDir := filepath.Join(parent, "target.example_newer")
	for _, dir := range []string{olderDir, newerDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC()
	s.saveScanRecordTo(&ScanRecord{
		ID: "traffic-older", InstanceID: instanceID, Target: "https://target.example",
		Status: "finished", StartedAt: base.Add(-time.Hour).Format(time.RFC3339),
	}, olderDir)
	s.saveScanRecordTo(&ScanRecord{
		ID: "traffic-newer", InstanceID: instanceID, Target: "https://target.example",
		Status: "finished", StartedAt: base.Format(time.RFC3339),
	}, newerDir)

	olderArchive := scanctx.NewTrafficArchive(olderDir)
	if _, err := olderArchive.Append(&scanctx.TrafficExchange{
		At: base.Add(-time.Hour), Method: "POST", URL: "https://target.example/api/vuln",
		Status: 200, Channel: "bearer",
		RequestRaw: "POST /api/vuln HTTP/1.1\r\nHost: target.example\r\n\r\npayload", ResponseRaw: "HTTP/1.1 200 OK\r\n\r\nvulnerable",
	}); err != nil {
		t.Fatal(err)
	}
	newerArchive := scanctx.NewTrafficArchive(newerDir)
	for i := 0; i < 2; i++ {
		if _, err := newerArchive.Append(&scanctx.TrafficExchange{
			At: base.Add(time.Duration(i) * time.Minute), Method: "GET",
			URL: "https://target.example/health", Status: 200, Channel: "subprocess",
		}); err != nil {
			t.Fatal(err)
		}
	}

	var view scanTrafficView
	rr := trafficGet(t, s, "/api/scans/"+instanceID+"/traffic")
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Present || view.TotalMatched != 3 || len(view.Exchanges) != 3 {
		t.Fatalf("merged view = present:%v total:%d n:%d", view.Present, view.TotalMatched, len(view.Exchanges))
	}
	sessions := map[string]bool{}
	for _, ex := range view.Exchanges {
		sessions[ex.Session] = true
	}
	if !sessions["target.example_older"] || !sessions["target.example_newer"] {
		t.Fatalf("merged rows must carry both sessions: %+v", sessions)
	}
	// Chronological order across sessions (the older packet comes first).
	if !strings.Contains(view.Exchanges[0].RequestRaw, "payload") {
		t.Fatalf("merged order lost the older session's packet: %+v", view.Exchanges[0])
	}

	// Newest-window limit is global across sessions, not per session.
	rr = trafficGet(t, s, "/api/scans/"+instanceID+"/traffic?limit=1")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Truncated || len(view.Exchanges) != 1 || view.Exchanges[0].Session != "target.example_newer" || view.Exchanges[0].Seq != 2 {
		t.Fatalf("global limit = %+v", view.Exchanges)
	}

	// Exact evidence refs: the older session's seq 1, despite the run's newest
	// session being the other directory.
	rr = trafficGet(t, s, "/api/scans/"+instanceID+"/traffic?refs=target.example_older:1")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Exchanges) != 1 || !strings.Contains(view.Exchanges[0].RequestRaw, "payload") {
		t.Fatalf("ref evidence = %+v", view.Exchanges)
	}

	// Path-traversal in a ref session is refused.
	rr = trafficGet(t, s, "/api/scans/"+instanceID+"/traffic?refs=../etc:1")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Exchanges) != 3 {
		t.Fatalf("traversal ref should fall back to the heuristic read, got %d", len(view.Exchanges))
	}
}
