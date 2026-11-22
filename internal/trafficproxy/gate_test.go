package trafficproxy

// High-risk gate acceptance (R2b closed loop): channel discipline and the
// suspend/403 path on the proxy side. Cases:
//  1. Basic (subprocess) high-risk POST suspends — the upstream receives
//     NOTHING until the gate approves, then forwards and returns 200.
//  2. Gate denial → 403 with the denial text, upstream untouched, and the
//     observation records the 403 outcome.
//  4. Bearer high-risk traffic NEVER reaches the gate (already adjudicated
//     at the tool layer — the verifier timing invariant depends on it).
//  5. Unattributed high-risk traffic NEVER reaches the gate (system-level).
//
// The deadline/timeout semantics live in the web adapter (ApprovalService
// deadline timer); the proxy side only honors the verdict it is given.

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crypto/tls"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

// gateCall is one invocation recorded by the stub gate.
type gateCall struct {
	attr    Attribution
	verdict approval.Verdict
}

// blockingGate records calls and holds each one until the test hands back a
// verdict over decide. arrivals signals every Adjudicate (buffered, so a
// gate-less path can be detected by absence).
type blockingGate struct {
	mu       sync.Mutex
	calls    []gateCall
	arrivals chan struct{}
	decide   chan gateVerdict
}

type gateVerdict struct {
	approved bool
	text     string
}

func (g *blockingGate) Adjudicate(attr Attribution, verdict approval.Verdict) (bool, string, error) {
	g.mu.Lock()
	g.calls = append(g.calls, gateCall{attr: attr, verdict: verdict})
	g.mu.Unlock()
	select {
	case g.arrivals <- struct{}{}:
	default:
	}
	v := <-g.decide
	return v.approved, v.text, nil
}

func (g *blockingGate) recorded() []gateCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gateCall(nil), g.calls...)
}

// gateProxy is the common rig: MITM on, direct forwarding, loopback targets
// allowed, capture sink, one registered run token, and the given hook
// installed (restored to nil on cleanup).
func gateProxy(t *testing.T, gate HighRiskGate) func() []TrafficObservation {
	t.Helper()
	_ = proxy.Close()
	SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		SetHighRiskGate(nil)
		_ = Close()
	})
	SetHighRiskGate(gate)
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

func registerGateToken(t *testing.T) string {
	t.Helper()
	const token = "7c6b5a4938271605f4e3d2c1b0a99887"
	t.Cleanup(func() { UnregisterRun(token, "gate-scan") })
	RegisterRun(token, "gate-inst", "gate-scan", scopeguard.Config{AllowLocalTargets: true})
	return token
}

func basicAuth(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(token+":"))
}

// waitForArrival polls until the gate recorded n calls (with timeout).
func waitForArrivals(t *testing.T, g *blockingGate, n int) []gateCall {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls := g.recorded(); len(calls) >= n {
			return calls
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("gate recorded %d calls, want %d", len(g.recorded()), n)
	return nil
}

// waitForUpstreamHits polls a target hit counter, returning the current
// value before the deadline (never fatal — callers assert on the value).
func waitForUpstreamHits(hits *atomic.Int32, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if int(hits.Load()) >= want {
			return int(hits.Load())
		}
		time.Sleep(2 * time.Millisecond)
	}
	return int(hits.Load())
}

// TestGateSubprocessHighRiskSuspendsUntilApproved is case ①: the proxy parks
// the request BEFORE the upstream hop, the target sees nothing while the
// verdict is pending, and approval releases the request byte-for-byte.
func TestGateSubprocessHighRiskSuspendsUntilApproved(t *testing.T) {
	gate := &blockingGate{arrivals: make(chan struct{}, 8), decide: make(chan gateVerdict, 8)}
	collect := gateProxy(t, gate)
	token := registerGateToken(t)

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "upstream-ok")
	}))
	defer target.Close()

	client := gateClient(t)
	done := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, target.URL+"/admin/delete", strings.NewReader("payload"))
		req.Header.Set("Proxy-Authorization", basicAuth(token))
		resp, err := client.Do(req)
		if err == nil {
			done <- resp
		}
	}()

	calls := waitForArrivals(t, gate, 1)
	// The upstream must NOT have been contacted while pending.
	if n := waitForUpstreamHits(&hits, 1, 300*time.Millisecond); n != 0 {
		t.Fatalf("upstream received %d requests while approval was pending", n)
	}
	call := calls[0]
	if call.attr.Channel != ChannelSubprocess || call.attr.InstanceID != "gate-inst" || call.attr.ScanCtxID != "gate-scan" {
		t.Fatalf("gate attribution = %+v, want subprocess/gate-inst/gate-scan", call.attr)
	}
	if call.verdict.Kind != approval.KindHTTP || call.verdict.Method != http.MethodPost {
		t.Fatalf("gate verdict = %+v", call.verdict)
	}
	if !strings.Contains(call.verdict.URL, "/admin/delete") {
		t.Fatalf("gate verdict URL lost the path: %q", call.verdict.URL)
	}
	if call.verdict.Body != "payload" {
		t.Fatalf("gate verdict body preview = %q", call.verdict.Body)
	}
	if call.verdict.Reason == "" {
		t.Fatal("gate verdict lost the AssessHTTP reason")
	}

	gate.decide <- gateVerdict{approved: true}
	select {
	case resp := <-done:
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "upstream-ok" {
			t.Fatalf("after approval: status=%d body=%q", resp.StatusCode, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request never completed after approval")
	}
	if n := waitForUpstreamHits(&hits, 1, 2*time.Second); n != 1 {
		t.Fatalf("upstream hits after approval = %d, want exactly 1", n)
	}

	// The observation records the forwarded outcome, not a denial.
	Close()
	var sawForward bool
	for _, o := range collect() {
		if o.Method == http.MethodPost && o.ScanCtxID == "gate-scan" && o.Status == http.StatusOK && !o.Failed {
			sawForward = true
		}
	}
	if !sawForward {
		t.Fatalf("no forwarded POST observation: %+v", collect())
	}
}

// TestGateRejectionReturns403WithoutUpstream is case ②: denial → 403 with
// the denial text (approval reference + reason), upstream never contacted,
// and the observation records the 403.
func TestGateRejectionReturns403WithoutUpstream(t *testing.T) {
	denial := "⛔ APPROVAL REJECTED — the operator declined this HTTP request (approval row-42).\nReason: admin panel path: /admin/delete"
	gate := &instantGate{verdict: gateVerdict{approved: false, text: denial}}
	collect := gateProxy(t, gate)
	token := registerGateToken(t)

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "must-not-reach")
	}))
	defer target.Close()

	client := gateClient(t)
	req, _ := http.NewRequest(http.MethodPost, target.URL+"/admin/delete", strings.NewReader("payload"))
	req.Header.Set("Proxy-Authorization", basicAuth(token))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("denied request should return a response, not a transport error: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denial status = %d, want 403", resp.StatusCode)
	}
	if !strings.Contains(string(body), "row-42") || !strings.Contains(string(body), "admin panel path") {
		t.Fatalf("403 body lost the approval reference or reason: %q", body)
	}
	if n := waitForUpstreamHits(&hits, 1, 300*time.Millisecond); n != 0 {
		t.Fatalf("upstream received %d requests after denial", n)
	}

	Close()
	var sawDenied bool
	for _, o := range collect() {
		if o.Method == http.MethodPost && o.Status == http.StatusForbidden {
			sawDenied = true
		}
	}
	if !sawDenied {
		t.Fatalf("no 403 observation recorded: %+v", collect())
	}
}

// instantGate answers immediately with a fixed verdict.
type instantGate struct {
	verdict gateVerdict
	calls   atomic.Int32
}

func (g *instantGate) Adjudicate(attr Attribution, verdict approval.Verdict) (bool, string, error) {
	g.calls.Add(1)
	return g.verdict.approved, g.verdict.text, nil
}

// TestGateBearerHighRiskNeverGates is case ④: Bearer traffic is produced by
// the Go tool transports whose requests already met the registry approval
// gate — the proxy must observe it without ever consulting the hook (this is
// what keeps verifier retests off the operator queue).
func TestGateBearerHighRiskNeverGates(t *testing.T) {
	gate := &instantGate{}
	collect := gateProxy(t, gate)
	token := registerGateToken(t)

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "bearer-ok")
	}))
	defer target.Close()

	client := gateClient(t)
	req, _ := http.NewRequest(http.MethodPost, target.URL+"/admin/delete", strings.NewReader("payload"))
	req.Header.Set("Proxy-Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "bearer-ok" {
		t.Fatalf("bearer request: status=%d body=%q", resp.StatusCode, body)
	}
	if n := gate.calls.Load(); n != 0 {
		t.Fatalf("gate consulted %d times for Bearer traffic", n)
	}
	if n := waitForUpstreamHits(&hits, 1, 2*time.Second); n != 1 {
		t.Fatalf("bearer request did not forward: hits=%d", n)
	}
	Close()
	saw := false
	for _, o := range collect() {
		if o.Method == http.MethodPost && o.ScanCtxID == "gate-scan" && o.Status == http.StatusOK && o.HighRisk != "" {
			saw = true // observed AND flagged high-risk, yet never gated
		}
	}
	if !saw {
		t.Fatalf("bearer high-risk observation missing: %+v", collect())
	}
}

// TestGateUnattributedHighRiskNeverGates is case ⑤: no token → system-level
// traffic — no gate call, no attribution, forwarded.
func TestGateUnattributedHighRiskNeverGates(t *testing.T) {
	gate := &instantGate{}
	collect := gateProxy(t, gate)
	_ = registerGateToken(t) // a live token exists, but this request carries none

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "system-ok")
	}))
	defer target.Close()

	client := gateClient(t)
	resp, err := client.Post(target.URL+"/admin/delete", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "system-ok" {
		t.Fatalf("system request: status=%d body=%q", resp.StatusCode, body)
	}
	if n := gate.calls.Load(); n != 0 {
		t.Fatalf("gate consulted %d times for unattributed traffic", n)
	}
	Close()
	for _, o := range collect() {
		if o.Method == http.MethodPost && (o.ScanCtxID != "" || o.InstanceID != "") {
			t.Fatalf("unattributed traffic was attributed: %+v", o)
		}
	}
}

// TestGateUpgradeSubprocessHighRiskSuspends pins the L-1 boundary: a
// client-supplied Upgrade header must not smuggle a high-risk write past the
// approval queue. The visible method+URL are assessed before the hijack —
// subprocess channel suspends (upstream untouched), and approval releases the
// raw-byte relay to the origin (101 + echo round trip).
func TestGateUpgradeSubprocessHighRiskSuspends(t *testing.T) {
	gate := &blockingGate{arrivals: make(chan struct{}, 8), decide: make(chan gateVerdict, 8)}
	gateProxy(t, gate)
	token := registerGateToken(t)

	var originHit atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHit.Store(true)
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
	defer upstream.Close()

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
	fmt.Fprintf(raw, "POST %s/admin/delete HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nProxy-Authorization: %s\r\n\r\n", upstream.URL, upstream.URL[len("http://"):], basicAuth(token))

	calls := waitForArrivals(t, gate, 1)
	if calls[0].attr.Channel != ChannelSubprocess || calls[0].attr.ScanCtxID != "gate-scan" {
		t.Fatalf("upgrade gate attribution = %+v", calls[0].attr)
	}
	if calls[0].verdict.Method != http.MethodPost || !strings.Contains(calls[0].verdict.URL, "/admin/delete") {
		t.Fatalf("upgrade verdict = %+v", calls[0].verdict)
	}
	// The origin must not have been contacted while pending.
	if originHit.Load() {
		t.Fatal("origin contacted while upgrade approval was pending")
	}

	gate.decide <- gateVerdict{approved: true}
	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("upgrade after approval: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d, want 101", resp.StatusCode)
	}
	// Switched-protocol echo proves the raw relay survived the gate.
	raw.SetWriteDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(raw, "ping\r\n")
	raw.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ping" {
		t.Fatalf("switched-protocol echo failed: %q err=%v", line, err)
	}
	if !originHit.Load() {
		t.Fatal("origin never received the released upgrade request")
	}
}

// TestGateUpgradeBearerPassthroughAndSystemLevel pins the upgrade-channel
// discipline: Bearer-shaped upgrade traffic (already adjudicated at the tool
// gate) and unattributed upgrade traffic relay as raw bytes with no gate call.
func TestGateUpgradeBearerPassthroughAndSystemLevel(t *testing.T) {
	gate := &instantGate{}
	gateProxy(t, gate)
	token := registerGateToken(t)

	upstream := wsEchoUpstreamPlain()
	defer upstream.Close()

	localURL, err := LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	lu, _ := url.Parse(localURL)
	relay := func(auth string) int {
		raw, err := net.Dial("tcp", lu.Host)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		authLine := ""
		if auth != "" {
			authLine = "Proxy-Authorization: " + auth + "\r\n"
		}
		fmt.Fprintf(raw, "POST %s/admin/delete HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n%s\r\n", upstream.URL, upstream.URL[len("http://"):], authLine)
		resp, err := http.ReadResponse(bufio.NewReader(raw), &http.Request{Method: http.MethodPost})
		if err != nil {
			t.Fatalf("upgrade through proxy: %v", err)
		}
		return resp.StatusCode
	}

	if status := relay("Bearer " + token); status != http.StatusSwitchingProtocols {
		t.Fatalf("bearer upgrade status = %d, want 101", status)
	}
	if status := relay(""); status != http.StatusSwitchingProtocols {
		t.Fatalf("unattributed upgrade status = %d, want 101", status)
	}
	if n := gate.calls.Load(); n != 0 {
		t.Fatalf("gate consulted %d times for bearer/unattributed upgrades", n)
	}
}

// gateClient routes through the loopback proxy trusting the traffic-proxy CA.
func gateClient(t *testing.T) *http.Client {
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
		Timeout: 15 * time.Second,
	}
}

// TestGateSubprocessCONNECTTunnelGatesInnerRequests is the real-world
// subprocess shape: curl https → CONNECT carrying Proxy-Authorization: Basic
// <token:> → intercepted tunnel → inner plaintext POST. The tunnel-level
// attribution (subprocess channel) must gate the inner high-risk request —
// inner requests cannot carry proxy credentials themselves.
func TestGateSubprocessCONNECTTunnelGatesInnerRequests(t *testing.T) {
	gate := &blockingGate{arrivals: make(chan struct{}, 8), decide: make(chan gateVerdict, 8)}
	gateProxy(t, gate)
	token := registerGateToken(t)

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "tunnel-ok")
	}))
	defer target.Close()

	host, port := targetHost(target.URL), ""
	if h, p, err := net.SplitHostPort(targetHost(target.URL)); err == nil {
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
	fmt.Fprintf(raw, "CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\nProxy-Authorization: %s\r\n\r\n", host, port, host, port, basicAuth(token))
	resp, err := http.ReadResponse(bufio.NewReader(raw), &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT with subprocess credentials: %v %+v", err, resp)
	}
	tlsConn := tls.Client(raw, &tls.Config{RootCAs: RootCAs(), ServerName: host}) //nolint:gosec // test target
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS with the MITM: %v", err)
	}

	done := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, "https://"+host+":"+port+"/admin/delete", strings.NewReader("payload"))
		resp, err := (&http.Client{
			// The tunnel is already TLS-established with the MITM: hand the
			// pre-handshook conn straight to the transport (one request).
			Transport: &http.Transport{
				DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return tlsConn, nil
				},
			},
			Timeout: 15 * time.Second,
		}).Do(req)
		if err == nil {
			done <- resp
		}
	}()

	calls := waitForArrivals(t, gate, 1)
	if calls[0].attr.Channel != ChannelSubprocess {
		t.Fatalf("tunnel attribution channel = %v, want subprocess", calls[0].attr.Channel)
	}
	if calls[0].attr.ScanCtxID != "gate-scan" {
		t.Fatalf("tunnel attribution = %+v", calls[0].attr)
	}
	if n := waitForUpstreamHits(&hits, 1, 300*time.Millisecond); n != 0 {
		t.Fatalf("inner request reached the upstream while pending: %d", n)
	}
	gate.decide <- gateVerdict{approved: true}
	select {
	case inner := <-done:
		inner.Body.Close()
		if inner.StatusCode != http.StatusOK {
			t.Fatalf("inner request status = %d", inner.StatusCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inner request never completed after approval")
	}
	if n := waitForUpstreamHits(&hits, 1, 2*time.Second); n != 1 {
		t.Fatalf("upstream hits = %d, want exactly 1", n)
	}
}
