package trafficproxy

// The loopback proxy handler. Plain HTTP arrives as absolute-form requests;
// TLS arrives as CONNECT. Forwarding picks the upstream per request:
//
//	proxy.GetProxy()      → configured upstream pool (roundrobin/random;
//	                        in proxy-required mode this is exactly one)
//	nil + required        → 502, never direct (fail-closed, pinned by test)
//	nil + not required    → direct dial, guarded by the operator-machine dial
//	                        guard on net.Dialer.Control (post-DNS), so the
//	                        GuardDials ring survives the MITM hop
//
// Attribution is read from Proxy-Authorization BEFORE the hop-by-hop strip;
// the strip then guarantees the token never reaches the upstream target.

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	xproxy "golang.org/x/net/proxy"

	"github.com/xalgord/xalgorix/v4/internal/proxy"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

// hopByHopHeaders lists headers that must not be forwarded by a proxy
// (RFC 7230 Section 6.1).
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection",
	"Te",
	"Trailer",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

func removeHopByHopHeaders(h http.Header) {
	for _, val := range h["Connection"] {
		for _, token := range strings.Split(val, ",") {
			if token = strings.TrimSpace(token); token != "" {
				h.Del(token)
			}
		}
	}
	for _, hdr := range hopByHopHeaders {
		h.Del(hdr)
	}
}

// proxyHandler serves the loopback listener.
type proxyHandler struct {
	m *Manager
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.m.isClosed() {
		http.Error(w, "traffic proxy is closed", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodConnect {
		h.serveConnect(w, r)
		return
	}
	if r.URL == nil || !r.URL.IsAbs() {
		http.Error(w, "absolute URL required", http.StatusBadRequest)
		return
	}
	h.forward(w, r, r.URL.Scheme, Attribution{})
}

// forward relays one plain (or intercepted) HTTP request and records it.
// baseAttr carries the tunnel-level attribution for intercepted requests
// (their inner requests cannot carry the token themselves); direct/outer
// requests resolve it from Proxy-Authorization — read BEFORE the hop-by-hop
// strip, never forwarded anywhere.
//
// Note on Proxy-Authorization: it is consumed here for attribution and
// stripped hop-by-hop. A tool layer deliberately setting the header itself
// (e.g. to test proxy auth) therefore gets it relayed but not consumed —
// that is by design: tools never speak for the run.
func (h *proxyHandler) forward(w http.ResponseWriter, r *http.Request, scheme string, baseAttr Attribution) {
	m := h.m

	// Protocol-upgrade requests (WebSocket and friends) cannot ride the
	// forwarding transport: the hop-by-hop strip below would drop Upgrade and
	// a 101 response has no relayable body. They fall back to raw byte
	// passthrough instead.
	if isUpgradeRequest(r) {
		h.forwardUpgrade(w, r, scheme, baseAttr)
		return
	}

	attr := baseAttr
	if attr.ScanCtxID == "" && attr.InstanceID == "" {
		attr, _ = extractAttribution(r)
	}
	// Raw capture is attribution-gated (rawcapture.go): system-level traffic
	// has no scan archive to persist into.
	captureRaw := attr.ScanCtxID != ""
	obs := &TrafficObservation{
		At:         time.Now().UTC(),
		InstanceID: attr.InstanceID,
		ScanCtxID:  attr.ScanCtxID,
		Method:     r.Method,
	}

	target := r.URL
	target.Scheme = scheme
	if target.Host == "" {
		target.Host = r.Host
	}
	obs.URL = redactURL(target.String())
	if captureRaw {
		obs.Raw = &scanctx.TrafficExchange{
			At:      obs.At,
			Method:  r.Method,
			URL:     target.String(), // archive keeps the raw URL unmasked
			Channel: channelName(attr.Channel),
		}
	}

	// The body is buffered ONCE up to the raw capture limit and serves both
	// consumers: the high-risk assessment and the raw request rendering.
	// Whatever was buffered is always re-streamed, even on a read error, so
	// the body the upstream sees is byte-identical to what the client sent;
	// beyond the limit the remainder streams through untouched.
	bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, rawCaptureLimit+1))
	reqTruncated := false
	if len(bodyBytes) > rawCaptureLimit {
		bodyBytes = bodyBytes[:rawCaptureLimit]
		reqTruncated = true
	}

	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header = r.Header.Clone()
	if len(bodyBytes) > 0 {
		out.Body = io.NopCloser(io.MultiReader(bytes.NewReader(bodyBytes), r.Body))
	}
	removeHopByHopHeaders(out.Header) // strips Proxy-Authorization AFTER attribution
	obs.ReqHeaderNames = headerNames(out.Header)
	obs.HighRisk = assessHighRisk(obs.Method, obs.URL, string(bodyBytes))
	if captureRaw && obs.Raw != nil {
		obs.Raw.RequestRaw = renderRequestRaw(out, bodyBytes)
		obs.Raw.ReqTruncated = reqTruncated
		obs.Raw.HighRisk = obs.HighRisk
	}

	// Approval closed loop (R2b, gate.go): subprocess-channel high-risk
	// requests are the only traffic adjudicated at the proxy. Bearer traffic
	// already met the tool-layer gate (observe-only); unattributed traffic
	// never gates (system-level red line). The hook blocks synchronously
	// until a verdict exists (operator decision, deadline, run stop); on
	// denial the upstream is never contacted and the client gets 403 with
	// the approval reference. Writing the 403 through http.Error tolerates a
	// disconnected client (write errors dropped, no panic); the observation
	// still records the outcome either way.
	if approved, denial := adjudicateSubprocessHighRisk(attr, obs, string(bodyBytes)); !approved {
		obs.Status = http.StatusForbidden
		if obs.Raw != nil {
			obs.Raw.Status = http.StatusForbidden
			obs.Raw.Denied = true
			obs.Raw.Upstream = "proxy-gate"
		}
		m.observe(obs)
		http.Error(w, denial, http.StatusForbidden)
		return
	}

	resp, err := m.roundTrip(obs, out)
	if err != nil {
		obs.Failed = true
		if obs.Raw != nil {
			obs.Raw.Upstream = obs.Upstream
		}
		m.observe(obs)
		http.Error(w, "upstream proxy request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	obs.Status = resp.StatusCode
	obs.RespHeaderNames = headerNames(resp.Header)

	// The response body is tee-captured while it streams to the client: the
	// first rawCaptureLimit bytes are copied into the archive buffer, every
	// byte still reaches the client immediately (no extra latency, no
	// buffering delay for large or streaming responses).
	var respCapture *boundedCapture
	if captureRaw {
		respCapture = newBoundedCapture(rawCaptureLimit)
	}

	removeHopByHopHeaders(resp.Header)
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if respCapture != nil {
		_, _ = io.Copy(w, io.TeeReader(resp.Body, respCapture))
	} else {
		_, _ = io.Copy(w, resp.Body)
	}

	if obs.Raw != nil {
		obs.Raw.Status = resp.StatusCode
		obs.Raw.Upstream = obs.Upstream
		obs.Raw.ResponseRaw = renderResponseRaw(resp, respCapture.bytes())
		obs.Raw.RespTruncated = respCapture.truncated()
	}
	m.observe(obs)
}

// isUpgradeRequest reports whether the request intends to switch protocols
// (WebSocket and friends): any Upgrade header means the client wants a raw
// byte stream after the handshake, which the forwarding transport cannot
// carry.
func isUpgradeRequest(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("Upgrade")) != ""
}

// forwardUpgrade relays a protocol-upgrade request as raw bytes (blind
// passthrough): the request is re-emitted verbatim — Upgrade/Connection kept,
// nothing stripped — and both directions are piped until EOF. Observation
// degrades to CONNECT-level metadata (no inner request/response split) marked
// upgrade_passthrough. Works for the outer absolute-form leg and for the
// plaintext half of an intercepted tunnel alike (both ResponseWriters can be
// hijacked).
func (h *proxyHandler) forwardUpgrade(w http.ResponseWriter, r *http.Request, scheme string, baseAttr Attribution) {
	m := h.m

	attr := baseAttr
	if attr.ScanCtxID == "" && attr.InstanceID == "" {
		attr, _ = extractAttribution(r)
	}
	obs := &TrafficObservation{
		At:                 time.Now().UTC(),
		InstanceID:         attr.InstanceID,
		ScanCtxID:          attr.ScanCtxID,
		Method:             r.Method,
		UpgradePassthrough: true,
	}
	target := r.URL
	target.Scheme = scheme
	if target.Host == "" {
		target.Host = r.Host
	}
	obs.URL = redactURL(target.String())
	obs.ReqHeaderNames = headerNames(r.Header) // names only — raw request, values never recorded
	obs.HighRisk = assessHighRisk(obs.Method, obs.URL, "")

	// Gate BEFORE the hijack (R2b closed loop, gate.go): a client-supplied
	// Upgrade header must not become a bypass that smuggles a high-risk write
	// past the approval queue. The visible method+URL are assessed exactly
	// like the transport path (no body visibility — the verdict is method/
	// URL-shaped only); denial answers 403 on the still-unhijacked
	// ResponseWriter. After a successful hijack the raw-byte relay stays
	// ungated (documented boundary).
	if approved, denial := adjudicateSubprocessHighRisk(attr, obs, ""); !approved {
		obs.Status = http.StatusForbidden
		m.observe(obs)
		http.Error(w, denial, http.StatusForbidden)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		obs.Failed = true
		m.observe(obs)
		http.Error(w, "HTTP hijacking unavailable", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		obs.Failed = true
		m.observe(obs)
		return
	}

	host, port := splitHostPortDefault(target.Host, defaultPort(scheme))
	targetAddr := net.JoinHostPort(host, port)

	// Outbound leg — same upstream selection as the plain forward:
	//   absolute-form + non-SOCKS5 upstream → raw connection to the upstream
	//   proxy itself, request relayed verbatim (absolute form);
	//   otherwise (TLS origins, SOCKS5, no pool) → CONNECT/SOCKS5 tunnel to
	//   the origin (or a guarded direct dial when no pool is configured),
	//   request re-emitted in origin form.
	// TLS origins get the same target-side semantics as the transport path
	// (verify, one-shot insecure retry on certificate errors) — applied to
	// the raw connection because the request is written by hand here.
	// The request is written with Upgrade/Connection intact.
	p := proxy.GetProxy()
	absoluteForm := p != nil && r.URL.IsAbs() && scheme == "http" && p.Type != proxy.ProxyTypeSOCKS5
	openOutbound := func(insecure bool) (net.Conn, error) {
		if absoluteForm {
			return m.dialUpstreamProxyAddr(p)
		}
		conn, derr := m.tunnelDial(attr.ScanCtxID, targetAddr)
		if derr != nil {
			return nil, derr
		}
		return m.tlsHandshakeOnConn(conn, obs, tlsAuthority(target), scheme, insecure)
	}
	upstreamConn, err := openOutbound(false)
	if err != nil && !m.tlsSkipVerify && isTLSCertError(err) {
		obs.TargetTLSUnverified = true
		upstreamConn, err = openOutbound(true)
	}
	if err != nil || upstreamConn == nil {
		_ = clientConn.Close()
		obs.Failed = true
		m.observe(obs)
		// The connection is already hijacked — the error must go over the raw
		// conn, not through the (now detached) ResponseWriter.
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		_ = clientConn.Close()
		return
	}
	if p != nil {
		obs.Upstream = p.String() // password masked by Proxy.String
	} else {
		obs.Upstream = "direct"
	}

	request := r
	if !absoluteForm {
		request = r.Clone(r.Context())
		request.RequestURI = "" // Request.Write then emits origin-form
	}
	if werr := request.Write(upstreamConn); werr != nil {
		_ = upstreamConn.Close()
		_ = clientConn.Close()
		obs.Failed = true
		m.observe(obs)
		return
	}
	m.observe(obs)

	// Outer absolute-form hijacked conns are untracked by http.Server; the
	// inner case is already tracked by its CONNECT tunnel.
	var t *tunnel
	if r.URL.IsAbs() && r.RequestURI != "" {
		t = &tunnel{conn: clientConn, done: make(chan struct{})}
		m.registerTunnel(t)
	}

	go func() {
		_, _ = io.Copy(upstreamConn, clientBuf)
		// Half-close propagates EOF through TLS too (*tls.Conn has
		// CloseWrite); a bare *net.TCPConn assertion would miss it.
		if cw, ok := upstreamConn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, _ = io.Copy(clientConn, upstreamConn)
	_ = upstreamConn.Close()
	_ = clientConn.Close()
	if t != nil {
		m.unregisterTunnel(t)
	}
}

// tlsHandshakeOnConn wraps a raw origin connection in TLS for https/wss
// origins and completes the handshake. insecure skips verification (the
// one-shot retry path); non-TLS schemes return the connection unchanged.
func (m *Manager) tlsHandshakeOnConn(conn net.Conn, obs *TrafficObservation, host, scheme string, insecure bool) (net.Conn, error) {
	if scheme != "https" && scheme != "wss" {
		return conn, nil
	}
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if insecure {
		cfg.InsecureSkipVerify = true //nolint:gosec // one-shot retry / XALGORIX_TLS_SKIP_VERIFY
	}
	tlsConn := tls.Client(conn, cfg)
	_ = tlsConn.SetDeadline(time.Now().Add(upstreamRespDeadline))
	if err := tlsConn.Handshake(); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	_ = tlsConn.SetDeadline(time.Time{})
	return tlsConn, nil
}

// tlsAuthority is the SNI hostname for an origin URL.
func tlsAuthority(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Hostname()
}

// dialUpstreamProxyAddr opens a raw connection to the upstream proxy itself
// (plain or TLS) for absolute-form passthrough. SOCKS5 upstreams are tunneled
// instead (see the caller's switch).
func (m *Manager) dialUpstreamProxyAddr(p *proxy.Proxy) (net.Conn, error) {
	addr := net.JoinHostPort(p.Host, p.Port)
	if p.Type == proxy.ProxyTypeHTTPS {
		return tls.DialWithDialer(&net.Dialer{Timeout: upstreamDialTimeout}, "tcp", addr, &tls.Config{ServerName: p.Host, MinVersion: tls.VersionTLS12})
	}
	return net.DialTimeout("tcp", addr, upstreamDialTimeout)
}

// splitHostPortDefault splits host:port, defaulting the port when the
// authority carries none (e.g. "example.com" for a ws:// URL).
func splitHostPortDefault(hostport, defPort string) (string, string) {
	if host, port, err := net.SplitHostPort(hostport); err == nil && port != "" {
		return host, port
	}
	return hostport, defPort
}

// defaultPort is the origin port implied by a proxy scheme.
func defaultPort(scheme string) string {
	if scheme == "https" || scheme == "wss" {
		return "443"
	}
	return "80"
}

// roundTrip forwards via the per-request upstream: the configured pool
// (rotated) when present, otherwise a guarded direct transport. Proxy-required
// mode never falls back to a direct dial.
//
// Target-side TLS follows XALGORIX_TLS_SKIP_VERIFY: verify against the system
// roots by default, skip when configured. When verification fails (a
// self-signed test target), the leg is retried once without verification and
// the observation is marked — browser traffic to self-signed targets keeps
// working (it always did via ignore-certificate-errors), and the downgrade
// stays visible in the audit trail.
func (m *Manager) roundTrip(obs *TrafficObservation, req *http.Request) (*http.Response, error) {
	resp, err := m.forwardTransport(obs, req)
	if err != nil && !m.tlsSkipVerify && isTLSCertError(err) {
		obs.TargetTLSUnverified = true
		resp, err = m.insecureForwardTransport(obs).RoundTrip(req)
	}
	return resp, err
}

func (m *Manager) forwardTransport(obs *TrafficObservation, req *http.Request) (*http.Response, error) {
	p := proxy.GetProxy()
	if p == nil {
		if proxy.Required() {
			return nil, errRequiredNoUpstream
		}
		obs.Upstream = "direct"
		tr := m.directTransport(obs.ScanCtxID)
		return tr.RoundTrip(req)
	}
	obs.Upstream = p.String() // password masked by Proxy.String
	tr, err := m.upstreamTransport(p)
	if err != nil {
		return nil, err
	}
	return tr.RoundTrip(req)
}

func (m *Manager) insecureForwardTransport(obs *TrafficObservation) *http.Transport {
	p := proxy.GetProxy()
	if p == nil {
		return m.insecureDirectTransport(obs.ScanCtxID)
	}
	return m.insecureUpstreamTransport(p)
}

// isTLSCertError reports whether err is a TLS certificate-verification
// failure (unknown authority, hostname mismatch, expired, ...).
func isTLSCertError(err error) bool {
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var verification *tls.CertificateVerificationError
	return errors.As(err, &unknown) || errors.As(err, &invalid) ||
		errors.As(err, &hostname) || errors.As(err, &verification)
}

// ---------------------------------------------------------------------------
// Transports
// ---------------------------------------------------------------------------

const (
	dialTimeout          = 30 * time.Second
	dialKeepAlive        = 30 * time.Second
	upstreamDialTimeout  = 15 * time.Second
	upstreamRespDeadline = 30 * time.Second
	maxDirectTransports  = 16
)

// upstreamTransport caches one transport per upstream proxy entry so pool
// connections are reused across rotation.
func (m *Manager) upstreamTransport(p *proxy.Proxy) (*http.Transport, error) {
	proxyURL, err := p.URL()
	if err != nil {
		return nil, err
	}
	m.transMu.Lock()
	if m.transClosed {
		m.transMu.Unlock()
		return nil, fmt.Errorf("traffic proxy manager is closed")
	}
	key := p.Raw
	tr, ok := m.upstreamTransports[key]
	if !ok {
		tr = clonedDefaultTransport()
		tr.Proxy = http.ProxyURL(proxyURL)
		applySkipVerify(tr, m.tlsSkipVerify)
		m.upstreamTransports[key] = tr
	}
	m.transMu.Unlock()
	return tr, nil
}

// insecureUpstreamTransport returns (building and caching on demand) the
// skip-verify twin of an upstream transport, used for the one-shot retry on
// target certificate failures.
func (m *Manager) insecureUpstreamTransport(p *proxy.Proxy) *http.Transport {
	proxyURL, err := p.URL()
	if err != nil {
		tr := clonedDefaultTransport()
		applySkipVerify(tr, true)
		return tr
	}
	m.transMu.Lock()
	if m.transClosed {
		m.transMu.Unlock()
		tr := clonedDefaultTransport()
		applySkipVerify(tr, true)
		return tr
	}
	key := p.Raw
	tr, ok := m.insecureUpstreamTransports[key]
	if !ok {
		tr = clonedDefaultTransport()
		tr.Proxy = http.ProxyURL(proxyURL)
		applySkipVerify(tr, true)
		m.insecureUpstreamTransports[key] = tr
	}
	m.transMu.Unlock()
	return tr
}

// directTransport returns the transport for unproxied forwarding. The
// operator-machine dial guard runs in net.Dialer.Control — after DNS
// resolution, on the exact address being connected to (the GuardDials
// mechanism) — with the owning scan's configuration when the traffic is
// attributed, and the process-level configuration otherwise.
func (m *Manager) directTransport(scanCtxID string) *http.Transport {
	m.transMu.Lock()
	if tr, ok := m.directTransports[scanCtxID]; ok {
		m.transMu.Unlock()
		return tr
	}
	tr := m.newGuardedTransport(scanCtxID, m.tlsSkipVerify)
	if len(m.directTransports) >= maxDirectTransports {
		// Evict one entry; scans are few, but never trust unbounded maps.
		for k, old := range m.directTransports {
			if old != nil {
				old.CloseIdleConnections()
			}
			delete(m.directTransports, k)
			break
		}
	}
	m.directTransports[scanCtxID] = tr
	m.transMu.Unlock()
	return tr
}

// insecureDirectTransport is the skip-verify twin used for the one-shot
// retry on target certificate failures.
func (m *Manager) insecureDirectTransport(scanCtxID string) *http.Transport {
	m.transMu.Lock()
	tr, ok := m.insecureDirectTransports[scanCtxID]
	if !ok {
		tr = m.newGuardedTransport(scanCtxID, true)
		m.insecureDirectTransports[scanCtxID] = tr
	}
	m.transMu.Unlock()
	return tr
}

func (m *Manager) newGuardedTransport(scanCtxID string, tlsSkipVerify bool) *http.Transport {
	tr := clonedDefaultTransport()
	if m.dialGuard != nil {
		guard := m.dialGuard
		dialer := &net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: dialKeepAlive,
			Control: func(_, address string, _ syscall.RawConn) error {
				return guard(scanCtxID, address)
			},
		}
		tr.DialContext = dialer.DialContext
	}
	applySkipVerify(tr, tlsSkipVerify)
	return tr
}

func applySkipVerify(tr *http.Transport, skip bool) {
	if !skip {
		return
	}
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{} //nolint:gosec // XALGORIX_TLS_SKIP_VERIFY semantics
	} else {
		tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	}
	tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // controlled by XALGORIX_TLS_SKIP_VERIFY
}

func (m *Manager) closeTransports() {
	m.transMu.Lock()
	m.transClosed = true
	for _, tr := range m.upstreamTransports {
		tr.CloseIdleConnections()
	}
	for _, tr := range m.directTransports {
		tr.CloseIdleConnections()
	}
	for _, tr := range m.insecureUpstreamTransports {
		tr.CloseIdleConnections()
	}
	for _, tr := range m.insecureDirectTransports {
		tr.CloseIdleConnections()
	}
	m.upstreamTransports = map[string]*http.Transport{}
	m.directTransports = map[string]*http.Transport{}
	m.insecureUpstreamTransports = map[string]*http.Transport{}
	m.insecureDirectTransports = map[string]*http.Transport{}
	m.transMu.Unlock()
}

// clonedDefaultTransport returns a writable clone of the stdlib default
// transport (mirrors internal/proxy.clonedDefaultTransport).
func clonedDefaultTransport() *http.Transport {
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		return dt.Clone()
	}
	return &http.Transport{}
}

// ---------------------------------------------------------------------------
// CONNECT
// ---------------------------------------------------------------------------

func (h *proxyHandler) serveConnect(w http.ResponseWriter, r *http.Request) {
	if _, _, err := net.SplitHostPort(r.Host); err != nil {
		http.Error(w, "CONNECT host:port required", http.StatusBadRequest)
		return
	}
	if h.m.mitm && h.m.ca != nil {
		h.serveConnectIntercepted(w, r)
		return
	}
	h.serveConnectBlind(w, r)
}

// tunnelDial establishes the tunnel's outbound connection: through the
// per-request upstream when one is configured (the upstream owns destination
// egress; its own address is exempt from the dial guard, mirroring
// GuardDials' proxy exemption), otherwise directly to the target under the
// operator-machine dial guard. Proxy-required mode never dials the target
// directly.
func (m *Manager) tunnelDial(scanCtxID, target string) (net.Conn, error) {
	p := proxy.GetProxy()
	if p != nil {
		return m.dialThroughUpstream(p, target)
	}
	if proxy.Required() {
		return nil, errRequiredNoUpstream
	}
	dialer := &net.Dialer{Timeout: upstreamDialTimeout}
	if m.dialGuard != nil {
		guard := m.dialGuard
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			return guard(scanCtxID, address)
		}
	}
	return dialer.Dial("tcp", target)
}

// serveConnectBlind preserves the pre-R2b CONNECT semantics: hijack the
// client connection, acknowledge, and pipe bytes both ways without ever
// looking inside the TLS stream. Used when the traffic proxy is off
// (proxy-required legacy bridge) or when the CA refused to load.
func (h *proxyHandler) serveConnectBlind(w http.ResponseWriter, r *http.Request) {
	m := h.m
	attr, _ := extractAttribution(r)
	obs := &TrafficObservation{
		At:         time.Now().UTC(),
		InstanceID: attr.InstanceID,
		ScanCtxID:  attr.ScanCtxID,
		Method:     http.MethodConnect,
		URL:        redactURL(r.Host),
	}

	upstreamConn, err := m.tunnelDial(attr.ScanCtxID, r.Host)
	if err != nil {
		obs.Failed = true
		m.observe(obs)
		http.Error(w, "upstream proxy CONNECT failed", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstreamConn.Close()
		obs.Failed = true
		m.observe(obs)
		http.Error(w, "HTTP hijacking unavailable", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuffer, err := hijacker.Hijack()
	if err != nil {
		_ = upstreamConn.Close()
		return
	}
	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = upstreamConn.Close()
		_ = clientConn.Close()
		return
	}
	obs.Status = http.StatusOK
	m.observe(obs)

	t := &tunnel{conn: clientConn, done: make(chan struct{})}
	m.registerTunnel(t)
	defer m.unregisterTunnel(t)

	go func() {
		_, _ = io.Copy(upstreamConn, clientBuffer)
		if tcp, ok := upstreamConn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	_, _ = io.Copy(clientConn, upstreamConn)
}

// dialThroughUpstream connects to target through one upstream proxy
// (SOCKS5, HTTPS-CONNECT or plain HTTP-CONNECT). Moved from the pre-R2b
// local proxy with a context parameter added.
func (m *Manager) dialThroughUpstream(p *proxy.Proxy, target string) (net.Conn, error) {
	proxyAddress := net.JoinHostPort(p.Host, p.Port)
	if p.Type == proxy.ProxyTypeSOCKS5 {
		var auth *xproxy.Auth
		if p.Username != "" {
			auth = &xproxy.Auth{User: p.Username, Password: p.Password}
		}
		dialer, err := xproxy.SOCKS5("tcp", proxyAddress, auth, &net.Dialer{Timeout: upstreamDialTimeout})
		if err != nil {
			return nil, err
		}
		conn, err := dialer.Dial("tcp", target)
		return conn, err
	}
	var conn net.Conn
	var err error
	if p.Type == proxy.ProxyTypeHTTPS {
		d := &net.Dialer{Timeout: upstreamDialTimeout}
		conn, err = tls.DialWithDialer(d, "tcp", proxyAddress, &tls.Config{ServerName: p.Host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = (&net.Dialer{Timeout: upstreamDialTimeout}).Dial("tcp", proxyAddress)
	}
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(upstreamRespDeadline))
	var request strings.Builder
	fmt.Fprintf(&request, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if p.Username != "" {
		credentials := base64.StdEncoding.EncodeToString([]byte(p.Username + ":" + p.Password))
		fmt.Fprintf(&request, "Proxy-Authorization: Basic %s\r\n", credentials)
	}
	request.WriteString("\r\n")
	if _, err := io.WriteString(conn, request.String()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("upstream CONNECT returned HTTP %d", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}
