package trafficproxy

// MITM interception acceptance (R2b decisions 2-3): httptest round-trips and
// deterministic state assertions — no dial probes (T-1 discipline).

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/proxy"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
)

// atomicBoolSink is a tiny test double for flag-style assertions.
type atomicBoolSink struct {
	v atomic.Bool
}

func (a *atomicBoolSink) set(b bool) { a.v.Store(b) }
func (a *atomicBoolSink) load() bool { return a.v.Load() }

// mitmProxy initializes the default-on posture: no upstream pool (direct),
// interception active from a fresh CA, and a capture sink. Returns the
// collected observations accessor.
func mitmProxy(t *testing.T) func() []TrafficObservation {
	t.Helper()
	_ = proxy.Close() // no upstream pool: direct forwarding
	// Operator opt-in so the direct-forwarding guard allows the loopback
	// httptest targets these tests dial (XALGORIX_ALLOW_LOCAL_TARGETS=true
	// is the documented self-hosted equivalent).
	SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })
	var mu sync.Mutex
	var got []TrafficObservation
	SetObservationSink(func(o TrafficObservation) {
		mu.Lock()
		got = append(got, o)
		mu.Unlock()
	})
	return func() []TrafficObservation {
		mu.Lock()
		defer mu.Unlock()
		out := make([]TrafficObservation, len(got))
		copy(out, got)
		return out
	}
}

// proxiedClient returns a client routed through the loopback proxy trusting
// the traffic-proxy CA (no InsecureSkipVerify: CA trust proves interception).
func proxiedClient(t *testing.T) *http.Client {
	t.Helper()
	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(localURL)
	if err != nil {
		t.Fatal(err)
	}
	pool := RootCAs()
	if pool == nil {
		t.Fatal("CA pool missing under MITM")
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(parsed),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
		Timeout: 10 * time.Second,
	}
}

// TestMITMOffKeepsBlindTunnel pins the off-mode byte-equivalence on the TLS
// path: with XALGORIX_TRAFFICPROXY off the loopback bridge stays a blind
// tunnel, so a client trusting ONLY the traffic-proxy CA (which proves
// interception) cannot complete a handshake — the target's own certificate
// reaches the client untouched.
func TestMITMOffKeepsBlindTunnel(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "target-ok")
	}))
	defer target.Close()
	var sawCONNECT atomicBoolSink
	upstream := connectCapableUpstream(&sawCONNECT)
	defer upstream.Close()

	proxyHost := strings.TrimPrefix(upstream.URL, "http://")
	legacyBridge(t, "http://"+proxyHost)
	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(localURL)
	// RootCAs deliberately NOT set — blind mode forwards the real certificate,
	// which the httptest self-signed target fails against. That failure IS
	// the discriminator proving no interception happened.
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(parsed)}, Timeout: 5 * time.Second}
	resp, err := client.Get(target.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("off mode must not intercept TLS: the self-signed target certificate should fail verification")
	}
	if !sawCONNECT.load() {
		t.Fatal("CONNECT tunnel through the upstream was expected")
	}
}

func TestMITMOnInterceptsTLSWithCA(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "intercepted-"+r.URL.Path)
	}))
	defer target.Close()

	collect := mitmProxy(t)
	client := proxiedClient(t)

	resp, err := client.Get(target.URL + "/deep")
	if err != nil {
		t.Fatalf("intercepted TLS round trip: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "intercepted-/deep" {
		t.Fatalf("unexpected body %q", body)
	}

	Close() // drains the sink deterministically
	obs := collect()
	var sawTunnel, sawRequest bool
	for _, o := range obs {
		if o.Method == "CONNECT" && o.Intercepted && strings.Contains(o.URL, targetHost(target.URL)) {
			sawTunnel = true
		}
		if o.Method == "GET" && strings.Contains(o.URL, "/deep") && o.Status == 200 {
			sawRequest = true
		}
	}
	if !sawTunnel || !sawRequest {
		t.Fatalf("missing observations: tunnel=%v request=%v (%+v)", sawTunnel, sawRequest, obs)
	}
}

func TestMITMObservesAttributedTrafficIntoJournal(t *testing.T) {
	// A real scan context with a journal: attributed observations become
	// observed InfoItems; system traffic never does.
	scanDir := t.TempDir()
	sc := scanctx.New("r2b-attributed-scan", scanDir)
	scanctx.Activate(sc)
	t.Cleanup(func() {
		scanctx.Deactivate(sc.ID)
		sc.Close()
	})
	token := "5f1a2b3c4d5e6f708192a3b4c5d6e7f8"
	RegisterRun(token, "instance-r2b", sc.ID, scopeguard.Config{AllowLocalTargets: true})
	t.Cleanup(func() { UnregisterRun(token, sc.ID) })

	const secret = "super-secret-header-value"
	var sawHeader atomicBoolSink
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Probe") == secret {
			sawHeader.set(true)
		}
		_, _ = io.WriteString(w, "journal-ok")
	}))
	defer target.Close()

	// Configure without replacing the sink, then install ONE combined sink
	// that captures for assertions AND runs the production journal writer —
	// the assertions below cover the whole chain from proxy request to
	// domain journal.
	_ = proxy.Close() // no upstream pool: direct forwarding
	SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })
	var mu sync.Mutex
	var got []TrafficObservation
	SetObservationSink(func(o TrafficObservation) {
		mu.Lock()
		got = append(got, o)
		mu.Unlock()
		observeTraffic(o)
	})
	collect := func() []TrafficObservation {
		mu.Lock()
		defer mu.Unlock()
		out := make([]TrafficObservation, len(got))
		copy(out, got)
		return out
	}
	client := proxiedClient(t)

	req, err := http.NewRequest(http.MethodPost, target.URL+"/admin/delete?token=hunter2&x=1", strings.NewReader("payload-body"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Probe", secret)
	req.Header.Set("Proxy-Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("attributed request: %v", err)
	}
	resp.Body.Close()

	Close() // drain sink + stop
	obs := collect()
	var attributed *TrafficObservation
	for i := range obs {
		if obs[i].Method == http.MethodPost && obs[i].ScanCtxID == sc.ID {
			attributed = &obs[i]
		}
	}
	if attributed == nil {
		t.Fatalf("no attributed POST observation among %+v", obs)
	}
	if attributed.InstanceID != "instance-r2b" {
		t.Fatalf("instance = %q", attributed.InstanceID)
	}
	if !strings.Contains(attributed.URL, "token=%2A%2A%2A%2A") && !strings.Contains(attributed.URL, "token=****") {
		t.Fatalf("sensitive query value not masked in %q", attributed.URL)
	}
	if strings.Contains(attributed.URL, "hunter2") {
		t.Fatalf("credential leaked into the observation URL %q", attributed.URL)
	}
	if attributed.HighRisk == "" {
		t.Fatalf("POST /admin/delete was not flagged high-risk: %+v", attributed)
	}
	if !sawHeader.load() {
		t.Fatal("target did not receive the request header")
	}

	// The observation payload must never carry header VALUES or the token.
	raw, err := json.Marshal(attributed)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{secret, token, "Bearer", "payload-body"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("observation leaked %q: %s", leaked, raw)
		}
	}

	// The domain journal carries the observed InfoItem.
	journalBytes, err := os.ReadFile(filepath.Join(scanDir, "domain_journal.jsonl"))
	if err != nil {
		t.Fatalf("domain journal: %v", err)
	}
	journal := string(journalBytes)
	if !strings.Contains(journal, "observe_traffic") {
		t.Fatal("journal has no observe_traffic record")
	}
	if !strings.Contains(journal, `"nature":"observed"`) {
		t.Fatal("InfoItem is not observed")
	}
	if !strings.Contains(journal, `"kind":"trafficproxy"`) {
		t.Fatal("InfoItem source is not trafficproxy")
	}
	if !strings.Contains(journal, `"phase":"traffic"`) {
		t.Fatal("InfoItem context lacks phase")
	}
	if strings.Contains(journal, secret) || strings.Contains(journal, token) {
		t.Fatal("journal leaked a header value or the run token")
	}
}

func TestMITMSystemTrafficProducesNoInfoItem(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "system-ok")
	}))
	defer target.Close()
	scanDir := t.TempDir()
	sc := scanctx.New("r2b-system-scan", scanDir)
	scanctx.Activate(sc)
	t.Cleanup(func() {
		scanctx.Deactivate(sc.ID)
		sc.Close()
	})

	collect := mitmProxy(t)
	client := proxiedClient(t)
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	Close()

	for _, o := range collect() {
		if o.ScanCtxID != "" {
			t.Fatalf("unattributed traffic was attributed: %+v", o)
		}
	}
	journalBytes, err := os.ReadFile(filepath.Join(scanDir, "domain_journal.jsonl"))
	if err == nil && strings.Contains(string(journalBytes), "observe_traffic") {
		t.Fatal("system-level traffic produced an InfoItem")
	}
}

func TestMITMCloseWaitsForTunnels(t *testing.T) {
	// A target that holds the response open: the tunnel stays in flight.
	release := make(chan struct{})
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer target.Close()
	defer close(release)

	collect := mitmProxy(t)
	client := proxiedClient(t)
	done := make(chan error, 1)
	go func() {
		resp, err := client.Get(target.URL)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			t.Logf("unexpected response: status=%d body=%q", resp.StatusCode, body)
			resp.Body.Close()
		}
		done <- err
	}()

	// Wait until the tunnel is tracked, then Close must tear it down and
	// return only after the tracked count drains to zero (deterministic).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && TunnelCount() == 0 {
		select {
		case err := <-done:
			t.Fatalf("request ended before the tunnel was tracked: %v (obs=%+v)", err, collect())
		default:
		}
		time.Sleep(2 * time.Millisecond)
	}
	if TunnelCount() == 0 {
		t.Fatal("in-flight tunnel was never tracked")
	}
	if err := Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := TunnelCount(); n != 0 {
		t.Fatalf("tunnels remained after Close: %d", n)
	}
}

func connectCapableUpstream(saw *atomicBoolSink) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "connect only", http.StatusBadRequest)
			return
		}
		saw.set(true)
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
}

func targetHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return fmt.Sprintf("%s:%s", u.Hostname(), u.Port())
}

// TestMITMUpgradePassthroughIntercepted proves WebSocket-like upgrade
// requests survive the MITM: the client (wss-style, inside the intercepted
// tunnel) receives the real 101 and the switched-protocol byte stream
// round-trips — the forwarding transport would have stripped Upgrade and
// dropped the 101 (MEDIUM-1).
func TestMITMUpgradePassthroughIntercepted(t *testing.T) {
	upstream := wsEchoUpstream()
	defer upstream.Close()

	collect := mitmProxy(t)
	host, port := targetHost(upstream.URL), ""
	if h, p, err := net.SplitHostPort(targetHost(upstream.URL)); err == nil {
		host, port = h, p
	}

	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	lu, _ := url.Parse(localURL)
	raw, err := net.Dial("tcp", lu.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	fmt.Fprintf(raw, "CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n\r\n", host, port, host, port)
	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT through MITM: %v %+v", err, resp)
	}
	tlsConn := tls.Client(raw, &tls.Config{RootCAs: RootCAs(), ServerName: host}) //nolint:gosec // test target
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS with the MITM: %v", err)
	}

	fmt.Fprintf(tlsConn, "GET /ws HTTP/1.1\r\nHost: %s:%s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n", host, port)
	tlsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	handshakeResp, err := http.ReadResponse(bufio.NewReader(tlsConn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("upgrade handshake through MITM: %v", err)
	}
	if handshakeResp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d, want 101", handshakeResp.StatusCode)
	}

	// Switched-protocol echo round trip.
	tlsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(tlsConn, "ping\r\n")
	line, err := bufio.NewReader(tlsConn).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ping" {
		t.Fatalf("switched-protocol echo failed: %q err=%v", line, err)
	}

	Close()
	var sawPassthrough bool
	for _, o := range collect() {
		if o.UpgradePassthrough && strings.Contains(o.URL, host) {
			sawPassthrough = true
		}
	}
	if !sawPassthrough {
		t.Fatalf("upgrade passthrough not observed: %+v", collect())
	}
}

// TestMITMUpgradePassthroughPlainHTTP proves the outer absolute-form leg
// (ws:// through the proxy) also falls back to raw passthrough and reaches
// the origin (direct, guarded) with the upgrade intact.
func TestMITMUpgradePassthroughPlainHTTP(t *testing.T) {
	upstream := wsEchoUpstreamPlain()
	defer upstream.Close()

	_ = proxy.Close()
	SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })

	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	lu, _ := url.Parse(localURL)
	raw, err := net.Dial("tcp", lu.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	fmt.Fprintf(raw, "GET %s/ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n", upstream.URL, upstream.URL[len("http://"):])
	resp, err := http.ReadResponse(bufio.NewReader(raw), &http.Request{Method: http.MethodGet})
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("outer upgrade through MITM: %v %+v", err, resp)
	}
	fmt.Fprint(raw, "pong\r\n")
	line, err := bufio.NewReader(raw).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "pong" {
		t.Fatalf("outer switched-protocol echo failed: %q err=%v", line, err)
	}
}

// wsEchoUpstream is a WebSocket-like origin: 101 Switching Protocols on any
// Upgrade request, then a line echo over the raw (TLS) connection.
var wsUpstreamSeenHeaders atomic.Pointer[string]

func wsEchoUpstream() *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen := fmt.Sprintf("method=%s upgrade=%q connection=%q requesturi=%q", r.Method, r.Header.Get("Upgrade"), r.Header.Get("Connection"), r.RequestURI)
		wsUpstreamSeenHeaders.Store(&seen)
		if strings.TrimSpace(r.Header.Get("Upgrade")) == "" {
			http.Error(w, "upgrade expected", http.StatusBadRequest)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")); err != nil {
			return
		}
		scanner := bufio.NewScanner(buf.Reader)
		for scanner.Scan() {
			fmt.Fprintf(conn, "%s\r\n", scanner.Text())
		}
	}))
}

// wsEchoUpstreamPlain is the plain-HTTP variant for the outer leg.
func wsEchoUpstreamPlain() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("Upgrade")) == "" {
			http.Error(w, "upgrade expected", http.StatusBadRequest)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")); err != nil {
			return
		}
		scanner := bufio.NewScanner(buf.Reader)
		for scanner.Scan() {
			fmt.Fprintf(conn, "%s\r\n", scanner.Text())
		}
	}))
}
