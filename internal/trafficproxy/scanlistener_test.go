package trafficproxy

// Run-scoped listener tests: Chromium cannot send Proxy-Authorization, so the
// dedicated listener port is the attribution channel. This pins that a tokenless
// request on the run's port is attributed, observed, and raw-captured exactly
// like a token-stamped one.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

func TestScanListenerAttributesTokenlessTraffic(t *testing.T) {
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })

	SetDialGuardBase(scopeguard.Config{AllowLocalTargets: true})
	t.Cleanup(func() { SetDialGuardBase(scopeguard.Config{}) })

	RegisterRun("scan-listener-token", "scan-listener-inst", "scan-listener-ctx", scopeguard.Config{AllowLocalTargets: true})
	t.Cleanup(func() { UnregisterRun("scan-listener-token", "scan-listener-ctx") })

	observations := make(chan TrafficObservation, 8)
	SetObservationSink(func(o TrafficObservation) {
		if o.Method == http.MethodGet {
			observations <- o
		}
	})

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "scan-listener-ok")
	}))
	defer target.Close()

	scanURL, err := ListenForScan("scan-listener-ctx")
	if err != nil {
		t.Fatalf("ListenForScan: %v", err)
	}
	if port, ok := ScanListenerPort("scan-listener-ctx"); !ok || port <= 0 {
		t.Fatalf("ScanListenerPort = %d,%v", port, ok)
	}
	parsed, err := url.Parse(scanURL)
	if err != nil {
		t.Fatalf("parse scan URL: %v", err)
	}

	// No Proxy-Authorization header anywhere: the port alone must attribute.
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(parsed)},
		Timeout:   5 * time.Second,
	}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("GET through scan listener: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	select {
	case obs := <-observations:
		if obs.ScanCtxID != "scan-listener-ctx" || obs.InstanceID != "scan-listener-inst" {
			t.Fatalf("observation attribution = scan=%q instance=%q", obs.ScanCtxID, obs.InstanceID)
		}
		if obs.Raw == nil || obs.Raw.RequestRaw == "" || obs.Raw.ResponseRaw == "" {
			t.Fatalf("tokenless traffic must still be raw-captured: %+v", obs.Raw)
		}
		if obs.Raw.Channel != "subprocess" {
			t.Fatalf("channel = %q, want subprocess (gated like other non-bearer traffic)", obs.Raw.Channel)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no observation recorded for tokenless scan-listener traffic")
	}

	// A Bearer token for the SAME run (the container HTTP worker) upgrades the
	// channel to bearer: that request already met the tool-layer gate, so the
	// proxy must not re-adjudicate it as subprocess.
	bearerReq, err := http.NewRequest(http.MethodGet, target.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	bearerReq.Header.Set("Proxy-Authorization", "Bearer scan-listener-token")
	resp, err = client.Do(bearerReq)
	if err != nil {
		t.Fatalf("Bearer GET through scan listener: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	select {
	case obs := <-observations:
		if obs.Raw == nil || obs.Raw.Channel != "bearer" {
			t.Fatalf("same-run Bearer channel = %+v, want bearer", obs.Raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no observation recorded for Bearer traffic on the scan listener")
	}

	// Teardown removes the port: a later lookup must report none.
	CloseScanListener("scan-listener-ctx")
	if _, ok := ScanListenerPort("scan-listener-ctx"); ok {
		t.Fatal("scan listener port survived CloseScanListener")
	}
}
