package websearch

// R2b review acceptance: search HTTP no longer bypasses the security chain.
// With TLS interception active the search client routes through the loopback
// traffic proxy (CA-trusted); off it stays direct with the DialGuard applied
// (tools.GuardDials on its direct dials).

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/proxy"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
	"github.com/xalgord/xalgorix/v4/internal/trafficproxy"
)

// atomicCalls is a tiny counter for guard-invocation assertions.
type atomicCalls struct {
	v atomic.Int32
}

func (a *atomicCalls) add(n int) { a.v.Add(int32(n)) }
func (a *atomicCalls) load() int { return int(a.v.Load()) }

func TestSearchClientRoutesThroughTrafficProxy(t *testing.T) {
	_ = proxy.Close()
	trafficproxy.SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	t.Cleanup(func() { trafficproxy.SetDialGuardBase(scopeguard.Config{}) })
	if err := trafficproxy.Configure(true, t.TempDir()+"/ca", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })

	client := searchClient(nil, 10*time.Second)
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("search transport = %T", client.Transport)
	}
	localURL, err := trafficproxy.LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://search.example/", nil)
	proxyURL, perr := tr.Proxy(req)
	if perr != nil || proxyURL == nil || proxyURL.Host != mustHostPort(t, localURL) {
		t.Fatalf("search client proxy = %v (err=%v), want %s", proxyURL, perr, localURL)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Fatal("search client does not trust the traffic-proxy CA")
	}

	// End-to-end: a search-style request reaches the target through the MITM.
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "search-ok")
	}))
	defer target.Close()
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("search request through the MITM: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "search-ok" {
		t.Fatalf("unexpected response: status=%d body=%q", resp.StatusCode, body)
	}
}

func TestSearchClientOffDirectWithDialGuard(t *testing.T) {
	_ = proxy.Close()
	if err := trafficproxy.Configure(false, "", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })

	var guarded atomicCalls
	client := searchClient(func(address string) error {
		guarded.add(1)
		return fmt.Errorf("blocked by test guard")
	}, 5*time.Second)

	// A direct dial to any address must be checked by the dial guard.
	resp, err := client.Get("http://127.0.0.1:1/search")
	if err == nil {
		resp.Body.Close()
		t.Fatal("dial guard should have refused the direct search dial")
	}
	if guarded.load() == 0 {
		t.Fatal("dial guard was never consulted by the search client")
	}
}

func mustHostPort(t *testing.T, localURL string) string {
	t.Helper()
	parsed, err := parseSearchProxyURL(localURL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Host
}

func parseSearchProxyURL(raw string) (*url.URL, error) {
	return url.Parse(raw)
}
