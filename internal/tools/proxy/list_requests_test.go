package proxy

import (
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// list_requests must read the run's local MITM archive (not Caido): the agent
// uses it to quote the exact wire evidence behind a finding.
func TestListRequestsReadsLocalArchive(t *testing.T) {
	contextID := "list-requests-archive"
	dir := t.TempDir()
	sc := scanctx.New(contextID, dir)
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(contextID) })

	archive := scanctx.NewTrafficArchive(dir)
	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		ex := scanctx.TrafficExchange{
			At:          base.Add(time.Duration(i) * time.Second),
			Method:      "GET",
			URL:         "https://target.example/api/" + string(rune('a'+i)),
			Status:      200,
			Channel:     "bearer",
			RequestRaw:  "GET /api/" + string(rune('a'+i)) + " HTTP/1.1\r\nHost: target.example\r\n\r\n",
			ResponseRaw: "HTTP/1.1 200 OK\r\n\r\nok-" + string(rune('a'+i)),
		}
		if _, err := archive.Append(&ex); err != nil {
			t.Fatalf("seed archive: %v", err)
		}
	}

	res, err := listRequests(contextID, map[string]string{"count": "2", "filter": "/api/"})
	if err != nil || res.Error != "" {
		t.Fatalf("list failed: %+v %v", res, err)
	}
	if !strings.Contains(res.Output, "#2 ") || !strings.Contains(res.Output, "#3 ") {
		t.Fatalf("newest window missing rows: %s", res.Output)
	}
	if strings.Contains(res.Output, "#1 ") {
		t.Fatalf("oldest row leaked past the window: %s", res.Output)
	}

	raw, err := listRequests(contextID, map[string]string{"seq": "2"})
	if err != nil || raw.Error != "" {
		t.Fatalf("raw read failed: %+v %v", raw, err)
	}
	if !strings.Contains(raw.Output, "--- request ---") || !strings.Contains(raw.Output, "ok-b") {
		t.Fatalf("raw packet missing: %s", raw.Output)
	}
	if raw.Metadata["traffic_seq"] != int64(2) {
		t.Fatalf("metadata seq = %#v", raw.Metadata["traffic_seq"])
	}

	// No live context: an honest message, never an error.
	none, err := listRequests("no-such-scan-context", nil)
	if err != nil || none.Error != "" || !strings.Contains(none.Output, "No live scan context") {
		t.Fatalf("missing context handling: %+v %v", none, err)
	}
}
