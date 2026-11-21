package trafficproxy

// Loopback bridge tests. The three close tests assert DETERMINISTIC manager
// state instead of dial probes: on WSL2 a dial against a just-closed loopback
// listener succeeds ~50% of the time (docs/KNOWN_ISSUES.md T-1), so probing
// the port proves nothing. Assertions here inspect the Manager's own fields
// and a listener close hook — no sleeps, no probes, no loosened guarantees.

import (
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/proxy"
)

func TestRequiredProxyRejectsMissingConfiguration(t *testing.T) {
	for _, tc := range []struct {
		useProxy bool
		url      string
	}{
		{false, ""},
		{true, ""},
		{true, "bad proxy"},
	} {
		if err := proxy.InitWithPolicy(tc.useProxy, true, tc.url, "", "roundrobin", time.Second); err == nil {
			t.Fatalf("expected proxy-required config rejection for useProxy=%v url=%q", tc.useProxy, tc.url)
		}
	}
}

// legacyBridge initializes the pre-R2b posture: proxy-required single
// upstream + traffic proxy off (blind tunnel, byte-identical legacy
// behavior).
func legacyBridge(t *testing.T, upstreamURL string) {
	t.Helper()
	if err := proxy.InitWithPolicy(true, true, upstreamURL, "", "roundrobin", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := Configure(false, "", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Close()
		_ = proxy.Close()
	})
}

func TestLocalProxyForwardsHTTPAndAuthenticatedCONNECT(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "target-ok")
	}))
	defer target.Close()

	var sawHTTP, sawCONNECT atomic.Bool
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:secret"))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != wantAuth {
			http.Error(w, "proxy authentication missing", http.StatusProxyAuthRequired)
			return
		}
		if r.Method != http.MethodConnect {
			sawHTTP.Store(true)
			_, _ = io.WriteString(w, "upstream-http-ok")
			return
		}
		sawCONNECT.Store(true)
		destination, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, "destination unavailable", http.StatusBadGateway)
			return
		}
		defer destination.Close()
		client, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { _, _ = io.Copy(destination, buffered) }()
		_, _ = io.Copy(client, destination)
	}))
	defer upstream.Close()

	proxyHost := strings.TrimPrefix(upstream.URL, "http://")
	legacyBridge(t, "http://user:secret@"+proxyHost)
	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(localURL)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(parsed),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // local test server
	}}
	resp, err := client.Get("http://example.invalid/test")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "upstream-http-ok" || !sawHTTP.Load() {
		t.Fatalf("HTTP request did not reach authenticated upstream: %q", body)
	}
	resp, err = client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "target-ok" || !sawCONNECT.Load() {
		t.Fatalf("CONNECT tunnel did not reach authenticated upstream: %q", body)
	}
}

func TestLocalProxyNeverFallsBackDirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadProxy := listener.Addr().String()
	listener.Close()
	legacyBridge(t, "http://"+deadProxy)
	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(localURL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(parsed)}, Timeout: 3 * time.Second}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || targetHits.Load() != 0 {
		t.Fatalf("proxy failure reached target: status=%d hits=%d", resp.StatusCode, targetHits.Load())
	}
}

func TestEnvironmentReplacesInheritedProxySettings(t *testing.T) {
	legacyBridge(t, "http://127.0.0.1:3128")
	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	env, err := Environment([]string{"PATH=/usr/bin", "HTTP_PROXY=http://old", "https_proxy=http://old", "NO_PROXY=*"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "http://old") || strings.Contains(joined, "NO_PROXY=*") {
		t.Fatal("inherited proxy settings were not replaced")
	}
	if !strings.Contains(joined, "HTTPS_PROXY="+localURL) || !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatal("required proxy or unrelated environment setting missing")
	}
}

func TestEnvironmentRequiresEnabledOrRequired(t *testing.T) {
	// Off + not required: Environment must fail exactly like the pre-R2b
	// proxy.Environment did (LocalURL was required-only).
	if err := Configure(false, "", false); err != nil {
		t.Fatal(err)
	}
	_ = proxy.Close()
	t.Cleanup(func() { _ = Close() })
	if _, err := Environment([]string{"PATH=/usr/bin"}); err == nil {
		t.Fatal("Environment must be unavailable without traffic proxy or required mode")
	}
}

func TestLocalProxyClose(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxyHost := strings.TrimPrefix(upstream.URL, "http://")
	legacyBridge(t, "http://"+proxyHost)
	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(localURL)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the local proxy is actively serving
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(parsed)}, Timeout: 2 * time.Second}
	resp, err := client.Get("http://example.invalid/ping")
	if err != nil {
		t.Fatalf("expected proxy to be reachable, got %v", err)
	}
	resp.Body.Close()

	m := defaultManager.Load()
	if m == nil {
		t.Fatal("manager missing")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("unexpected Close error: %v", err)
	}

	// T-1 (docs/KNOWN_ISSUES.md): deterministic state assertions — no dial
	// probes, no sleeps.
	m.mu.Lock()
	closed := m.closed
	listener := m.listener
	server := m.server
	clearedURL := m.localURL
	m.mu.Unlock()
	if !closed {
		t.Fatal("manager not marked closed")
	}
	if listener != nil {
		t.Fatal("listener not released after Close")
	}
	if server != nil {
		t.Fatal("server not released after Close")
	}
	if clearedURL != "" {
		t.Fatal("local URL not cleared after Close")
	}
	if TunnelCount() != 0 {
		t.Fatal("tunnels remained tracked after Close")
	}
	if _, err := LocalURL(); err == nil {
		t.Fatal("LocalURL must fail on a closed manager")
	}

	// Swap the default away (as the package Close does) and check the
	// policy flags the test historically pinned.
	_ = Close()
	_ = proxy.Close()
	if proxy.Enabled() {
		t.Fatal("expected proxy to not be enabled after Close")
	}
	if proxy.Required() {
		t.Fatal("expected proxy to not be required after Close")
	}
}

func TestConcurrentLocalURLAndClose(t *testing.T) {
	for attempt := 0; attempt < 25; attempt++ {
		legacyBridge(t, "http://127.0.0.1:3128")
		start := make(chan struct{})
		var wg sync.WaitGroup
		var localURL string
		wg.Add(3)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				if u, err := LocalURL(); err == nil {
					localURL = u
				}
			}
		}()
		for i := 0; i < 2; i++ {
			go func() {
				defer wg.Done()
				<-start
				_ = Close()
			}()
		}
		close(start)
		wg.Wait()
		if m := defaultManager.Load(); m != nil {
			t.Fatal("traffic proxy manager remained active after concurrent Close")
		}
		// T-1: deterministic — the manager is swapped to nil by Close, so a
		// subsequent LocalURL must fail without racing any listener.
		if localURL != "" {
			if _, err := LocalURL(); err == nil {
				t.Fatal("loopback proxy URL remained usable after Close")
			}
		}
	}
}

// closeHookListener records whether Close was invoked, so the
// close-before-Serve test can assert deterministic state instead of probing
// the port (T-1).
type closeHookListener struct {
	net.Listener
	closed atomic.Bool
}

func (l *closeHookListener) Close() error {
	l.closed.Store(true)
	return l.Listener.Close()
}

func TestManagerCloseBeforeLocalServeStarts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hooked := &closeHookListener{Listener: ln}
	m := &Manager{
		listener:                   hooked,
		tunnels:                    make(map[*tunnel]struct{}),
		upstreamTransports:         make(map[string]*http.Transport),
		directTransports:           make(map[string]*http.Transport),
		insecureUpstreamTransports: make(map[string]*http.Transport),
		insecureDirectTransports:   make(map[string]*http.Transport),
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if !hooked.closed.Load() {
		t.Fatal("listener Close was not invoked when Close ran before Serve")
	}
	if m.listener != nil {
		t.Fatal("listener not released when Close ran before Serve")
	}
}

func TestLocalProxyStripsHopByHopHeaders(t *testing.T) {
	var receivedHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Keep-Alive", "timeout=5, max=1000")
		w.Header().Set("X-Custom-Header", "keep-response")
		w.Header().Set("Proxy-Connection", "close")
		w.Header().Set("Proxy-Authenticate", "Basic realm=\"proxy\"")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "strip-test")
	}))
	defer upstream.Close()

	proxyHost := strings.TrimPrefix(upstream.URL, "http://")
	legacyBridge(t, "http://"+proxyHost)

	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(localURL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(parsed)}, Timeout: 3 * time.Second}

	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/hop-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "keep-alive, X-Client-Hop")
	req.Header.Set("X-Client-Hop", "strip-client")
	req.Header.Set("Proxy-Authorization", "Basic secret")
	req.Header.Set("Proxy-Connection", "keep-alive")
	req.Header.Set("X-Client-Keep", "preserve-client")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Check headers received at upstream
	if receivedHeaders.Get("X-Client-Hop") != "" {
		t.Errorf("upstream received hop-by-hop header X-Client-Hop")
	}
	if receivedHeaders.Get("Proxy-Authorization") != "" {
		t.Errorf("upstream received Proxy-Authorization")
	}
	if receivedHeaders.Get("Proxy-Connection") != "" {
		t.Errorf("upstream received Proxy-Connection")
	}
	if receivedHeaders.Get("X-Client-Keep") != "preserve-client" {
		t.Errorf("upstream did not receive non-hop header X-Client-Keep")
	}

	// Check headers returned to client
	if resp.Header.Get("Keep-Alive") != "" {
		t.Errorf("client received hop-by-hop header Keep-Alive")
	}
	if resp.Header.Get("Proxy-Authenticate") != "" {
		t.Errorf("client received hop-by-hop header Proxy-Authenticate")
	}
	if resp.Header.Get("Proxy-Connection") != "" {
		t.Errorf("client received Proxy-Connection")
	}
	if resp.Header.Get("X-Custom-Header") != "keep-response" {
		t.Errorf("client did not receive non-hop header X-Custom-Header")
	}
}
