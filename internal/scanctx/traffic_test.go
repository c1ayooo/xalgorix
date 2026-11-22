package scanctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTrafficArchiveAppendReadMatch covers the raw-exchange archive lifecycle:
// append assigns sequences, reopen resumes past existing records (no
// overwrite), ReadTrafficExchanges orders by sequence, and
// MatchTrafficExchanges filters by endpoint/method with a newest-window limit.
func TestTrafficArchiveAppendReadMatch(t *testing.T) {
	scanDir := t.TempDir()
	a := NewTrafficArchive(scanDir)

	base := time.Now().UTC()
	exchanges := []TrafficExchange{
		{At: base, Method: "GET", URL: "https://target.example/api/users", Status: 200, Channel: "bearer", RequestRaw: "GET /api/users HTTP/1.1\r\n\r\n", ResponseRaw: "HTTP/1.1 200 OK\r\n\r\nusers"},
		{At: base.Add(time.Second), Method: "POST", URL: "https://target.example/admin/user?id=1", Status: 403, Channel: "subprocess", HighRisk: "admin panel path", Denied: true, RequestRaw: "POST /admin/user HTTP/1.1\r\n\r\ndrop database prod"},
		{At: base.Add(2 * time.Second), Method: "POST", URL: "https://other.example/login", Status: 200, Channel: "bearer", RequestRaw: "POST /login HTTP/1.1\r\n\r\npassword=x"},
	}
	for i := range exchanges {
		ex := exchanges[i]
		seq, err := a.Append(&ex)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if seq != int64(i+1) {
			t.Fatalf("seq = %d, want %d", seq, i+1)
		}
		if ex.Seq != seq {
			t.Fatalf("exchange seq not stamped: %d != %d", ex.Seq, seq)
		}
	}

	// The raw record is on disk under the scan's own directory, unredacted.
	data, err := os.ReadFile(filepath.Join(scanDir, "traffic", "tr_000002.json"))
	if err != nil {
		t.Fatalf("archive file: %v", err)
	}
	if !strings.Contains(string(data), "drop database prod") {
		t.Fatal("raw request body not persisted verbatim")
	}

	// Reopen: the counter resumes past existing records.
	reopened := NewTrafficArchive(scanDir)
	ex := exchanges[0]
	seq, err := reopened.Append(&ex)
	if err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	if seq != 4 {
		t.Fatalf("reopened seq = %d, want 4 (resume past existing)", seq)
	}

	all, err := ReadTrafficExchanges(scanDir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("read %d exchanges, want 4", len(all))
	}
	if all[0].Seq != 1 || all[3].Seq != 4 {
		t.Fatalf("exchanges not ordered by sequence: %d..%d", all[0].Seq, all[3].Seq)
	}

	// Empty scan dir: no archive, no error.
	empty, err := ReadTrafficExchanges(t.TempDir())
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing archive should read empty: %v %d", err, len(empty))
	}

	// Matching: endpoint substring + method, newest window.
	matched, total, err := MatchTrafficExchanges(scanDir, "/admin/user", "", 10)
	if err != nil || total != 1 || len(matched) != 1 {
		t.Fatalf("endpoint match: %v total=%d n=%d", err, total, len(matched))
	}
	if matched[0].HighRisk == "" || !matched[0].Denied {
		t.Fatalf("denied high-risk exchange lost its metadata: %+v", matched[0])
	}
	matched, total, _ = MatchTrafficExchanges(scanDir, "", "post", 1)
	if total != 2 || len(matched) != 1 || matched[0].URL != "https://other.example/login" {
		t.Fatalf("method match newest window: total=%d got=%+v", total, matched)
	}
	if _, total, _ := MatchTrafficExchanges(scanDir, "/nomatch", "", 10); total != 0 {
		t.Fatalf("no-match total = %d", total)
	}
}
