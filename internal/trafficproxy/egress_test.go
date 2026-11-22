package trafficproxy

// Outbound-chain generalization + GuardDials grafting acceptance (R2b
// decisions 3-4): per-request pool rotation, direct forwarding, the pinned
// proxy-required fail-closed semantics, and the operator-machine dial guard
// on direct upstream dials (pool addresses exempt, mirroring GuardDials'
// proxy exemption).

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

// TestPoolRotationThroughMITM proves upstream selection rotates per request
// through the loopback proxy: two upstream proxies answer with their own
// marker, so the client bodies show A, B, A under roundrobin.
func TestPoolRotationThroughMITM(t *testing.T) {
	var aCount, bCount atomic.Int32
	marker := func(name string, count *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			count.Add(1)
			_, _ = io.WriteString(w, name)
		}
	}
	upA := httptest.NewServer(http.HandlerFunc(marker("A", &aCount)))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(marker("B", &bCount)))
	defer upB.Close()

	poolFile := filepath.Join(t.TempDir(), "proxies.txt")
	if err := os.WriteFile(poolFile, []byte(strings.TrimPrefix(upA.URL, "http://")+"\n"+strings.TrimPrefix(upB.URL, "http://")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := proxy.InitWithPolicy(true, false, "", poolFile, "roundrobin", time.Second); err != nil {
		t.Fatal(err)
	}
	SetDialGuardBase(scopeguard.Config{})
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Close()
		_ = proxy.Close()
	})

	client := plainClient(t)
	var bodies []string
	for i := 0; i < 3; i++ {
		resp, err := client.Get("http://target.example/rotate")
		if err != nil {
			t.Fatalf("rotation request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		bodies = append(bodies, string(body))
	}
	if strings.Join(bodies, ",") != "A,B,A" {
		t.Fatalf("roundrobin rotation broken: %v", bodies)
	}
	if aCount.Load() != 2 || bCount.Load() != 1 {
		t.Fatalf("upstream hits a=%d b=%d, want 2/1", aCount.Load(), bCount.Load())
	}
}

// TestDirectForwardWithoutPool proves the default posture (traffic proxy on,
// no upstream pool) forwards directly to the destination.
func TestDirectForwardWithoutPool(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "direct-ok")
	}))
	defer target.Close()

	_ = proxy.Close()
	SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })

	client := plainClient(t)
	resp, err := client.Get(target.URL + "/direct")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "direct-ok" || hits.Load() != 1 {
		t.Fatalf("direct forward failed: body=%q hits=%d", body, hits.Load())
	}
}

// TestDirectDialGuardRejectsOperatorMachine proves the GuardDials graft: the
// MITM's direct CONNECT dial to an operator-machine address is refused on
// net.Dialer.Control with the owning scan's attribution — the third security
// ring survives the MITM hop.
func TestDirectDialGuardRejectsOperatorMachine(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "should-never-happen")
	}))
	defer target.Close()

	_ = proxy.Close()
	var guardedAddrs atomic.Int32
	prev := SetDialGuard(func(scanCtxID, address string) error {
		guardedAddrs.Add(1)
		if scanCtxID != "scan-guarded" {
			return fmt.Errorf("guard received unexpected scan id %q", scanCtxID)
		}
		return fmt.Errorf("blocked by test guard")
	})
	t.Cleanup(func() { SetDialGuard(prev) })
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })

	RegisterRun("guardtoken", "inst", "scan-guarded", scopeguard.Config{})
	t.Cleanup(func() { UnregisterRun("guardtoken", "scan-guarded") })

	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(localURL)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(parsed),
			TLSClientConfig: &tls.Config{RootCAs: RootCAs()},
		},
		Timeout: 5 * time.Second,
	}
	req, err := http.NewRequest(http.MethodGet, target.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Proxy-Authorization", "Bearer guardtoken")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client error (expected an in-tunnel 502): %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("guard rejection should surface as 502, got %d (%q)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "upstream proxy request failed") {
		t.Fatalf("unexpected 502 body %q", body)
	}
	if guardedAddrs.Load() == 0 {
		t.Fatal("dial guard was never consulted")
	}
}

// TestPoolDialExemptFromGuard proves the upstream exemption: with a pool
// configured, the guard is never consulted and traffic still flows (the
// upstream owns destination egress, mirroring GuardDials' proxy exemption).
func TestPoolDialExemptFromGuard(t *testing.T) {
	var guardCalls atomic.Int32
	prev := SetDialGuard(func(scanCtxID, address string) error {
		guardCalls.Add(1)
		return fmt.Errorf("guard must not run for pool dials")
	})
	defer SetDialGuard(prev)

	var sawProbe atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// An intercepting test upstream: answers absolute-form GETs directly.
		sawProbe.Store(true)
		_, _ = io.WriteString(w, "pool-ok")
	}))
	defer upstream.Close()

	if err := proxy.InitWithPolicy(true, false, strings.TrimPrefix(upstream.URL, "http://"), "", "roundrobin", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Close()
		_ = proxy.Close()
	})

	client := plainClient(t)
	resp, err := client.Get("http://target.example/exempt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "pool-ok" || !sawProbe.Load() {
		t.Fatalf("pool forwarding failed: %q", body)
	}
	if guardCalls.Load() != 0 {
		t.Fatalf("guard consulted %d times for pool dials", guardCalls.Load())
	}
}

// plainClient routes through the loopback proxy without CA trust — used for
// plain-HTTP (absolute-form) forwarding tests.
func plainClient(t *testing.T) *http.Client {
	t.Helper()
	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(localURL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(parsed)}, Timeout: 10 * time.Second}
}
