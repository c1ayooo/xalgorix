package trafficproxy

// BufferedSink acceptance (R2b decision 7): bounded queue, single writer,
// overflow dropped and counted, the proxy request path never waits for the
// journal, and no credential ever survives redaction.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/proxy"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
)

func TestBufferedSinkDropsOnOverflowAndCounts(t *testing.T) {

	var drained atomic.Int32
	release := make(chan struct{})
	b := NewBufferedSink(func(TrafficObservation) {
		<-release
		drained.Add(1)
	}, 8)

	// The writer is parked on the first item, so the queue holds 7 more.
	if !b.Enqueue(TrafficObservation{Method: "GET"}) {
		t.Fatal("unexpected drop while the queue was empty")
	}
	for i := 0; i < 7; i++ {
		if !b.Enqueue(TrafficObservation{Method: "GET"}) {
			t.Fatalf("unexpected drop at slot %d while the queue was not full", i)
		}
	}
	if b.Enqueue(TrafficObservation{Method: "GET"}) {
		t.Fatal("overflow observation was accepted")
	}
	if b.Drops() != 1 {
		t.Fatalf("drops = %d, want 1", b.Drops())
	}
	close(release)
	b.Close() // drains everything already enqueued
	if n := drained.Load(); n != 8 {
		t.Fatalf("drained %d observations, want 8", n)
	}
	if b.Drops() != 1 {
		t.Fatalf("drops changed after drain: %d", b.Drops())
	}
}

// TestProxyRequestsNeverWaitForTheJournal pins the latency constraint: the
// journal writer is blocked forever, yet proxied requests complete — the
// request path only does a non-blocking enqueue. If forwarding ever waited
// on the sink, the requests would stall until release and the test would
// fail by timeout.
func TestProxyRequestsNeverWaitForTheJournal(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	_ = proxy.Close()
	SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })
	SetObservationSink(func(TrafficObservation) { <-release })

	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "latency-ok")
	}))
	defer target.Close()

	client := plainClient(t)
	done := make(chan error, 1)
	for i := 0; i < 3; i++ {
		go func() {
			resp, err := client.Get(target.URL + "/latency")
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			done <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("latency request %d failed: %v", i, err)
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("proxy request waited on the blocked journal writer")
		}
	}
	if hits.Load() != 3 {
		t.Fatalf("target hits = %d, want 3", hits.Load())
	}
}

func TestObservationRedaction(t *testing.T) {
	t.Parallel()

	obs := TrafficObservation{
		At:              time.Now().UTC(),
		InstanceID:      "inst",
		ScanCtxID:       "scan",
		Method:          "POST",
		URL:             redactURL("https://target.example/admin/reset?token=hunter2&user=alice"),
		Status:          200,
		Upstream:        "direct",
		ReqHeaderNames:  headerNames(http.Header{"Authorization": {"Bearer top-secret"}, "Content-Type": {"application/json"}}),
		RespHeaderNames: headerNames(http.Header{"Set-Cookie": {"session=leaky"}}),
		HighRisk:        "auth mutation endpoint or payload",
	}
	item := buildTrafficInfoItem(obs)
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hunter2", "top-secret", "session=leaky", "Bearer"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("InfoItem leaked %q: %s", secret, raw)
		}
	}
	if !strings.Contains(item.Coordinates.Content.Text, "POST") || !strings.Contains(item.Coordinates.Content.Text, "target.example") {
		t.Fatalf("summary lost the request coordinates: %q", item.Coordinates.Content.Text)
	}
	if item.Nature != "observed" {
		t.Fatalf("nature = %q, want observed", item.Nature)
	}
	if item.Coordinates.Subject.Kind != "endpoint" {
		t.Fatalf("subject kind = %q", item.Coordinates.Subject.Kind)
	}
	if item.Coordinates.Context["high_risk_reason"] == "" {
		t.Fatal("high-risk reason missing from context")
	}
	if !strings.Contains(item.Coordinates.Context["request_headers"], "Authorization") {
		t.Fatalf("header names missing from context: %v", item.Coordinates.Context["request_headers"])
	}
	// Masked query values survive as **** (url-encoded by re-serialization),
	// non-sensitive ones stay intact.
	ident := item.Coordinates.Subject.Identifier
	u, perr := url.Parse(strings.TrimPrefix(ident, "POST "))
	if perr == nil {
		if got := u.Query().Get("token"); got != "****" {
			t.Fatalf("token query value not masked: %q", ident)
		}
		if got := u.Query().Get("user"); got != "alice" {
			t.Fatalf("non-sensitive query value was masked: %q", ident)
		}
	} else if !strings.Contains(ident, "token=****") {
		t.Fatalf("token query value not masked: %q", ident)
	}
	if !strings.Contains(item.Coordinates.Subject.Identifier, "user=alice") {
		t.Fatalf("non-sensitive query value was masked: %q", item.Coordinates.Subject.Identifier)
	}
}
