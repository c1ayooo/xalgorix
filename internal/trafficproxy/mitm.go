package trafficproxy

// MITM interception of CONNECT tunnels (REDESIGN §4, R2b decisions 2-3).
// The handshake: sign a leaf for the CONNECT host with the traffic-proxy CA,
// answer the client's CONNECT with 200, wrap the hijacked connection in
// tls.Server, and serve HTTP/1.1 over it with a one-shot http.Server so the
// stdlib handles keep-alive, header framing and idle timeouts for the
// plaintext half. Every inner request is forwarded through the normal
// per-request upstream chain and observed.
//
// Connection lifecycle: every tunnel registers a `tunnel` with the Manager;
// Close closes listener, server and all tracked tunnels and waits (bounded)
// for their goroutines, so no goroutine leaks and tests can assert
// deterministic cleanup. Idle timeouts are explicit (inner IdleTimeout,
// outer IdleTimeout when interception is active).

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	tlsHandshakeTimeout = 10 * time.Second
	innerIdleTimeout    = 120 * time.Second
)

// serveConnectIntercepted upgrades the CONNECT tunnel to an observed TLS
// session signed by the traffic-proxy CA.
func (h *proxyHandler) serveConnectIntercepted(w http.ResponseWriter, r *http.Request, baseAttr Attribution) {
	m := h.m
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "CONNECT host:port required", http.StatusBadRequest)
		return
	}

	leaf, leafErr := m.ca.LeafForHost(host)
	if leafErr != nil {
		// Fail closed for interception: without a leaf there is nothing to
		// serve the client; refusing beats silently falling back to a blind
		// tunnel that no consumer trust store was told about.
		http.Error(w, "traffic proxy TLS interception unavailable", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "HTTP hijacking unavailable", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = clientConn.Close()
		return
	}

	// The hijack hands back a buffered reader: any bytes the HTTP server
	// already consumed into the buffer (a pipelined ClientHello) must still
	// reach the TLS layer, so the TLS conn reads through the buffer and
	// writes straight to the raw connection.
	innerConn := &bufferedConn{r: clientBuffer.Reader, c: clientConn}
	tlsConn := tls.Server(innerConn, &tls.Config{ //nolint:gosec // certificates are set below; MinVersion pinned
		Certificates: []tls.Certificate{leaf},
		MinVersion:   tls.VersionTLS12,
		// No NextProtos: HTTP/1.1 only, so the plaintext half stays simple.
	})
	tlsConn.SetReadDeadline(time.Now().Add(tlsHandshakeTimeout))
	if err := tlsConn.Handshake(); err != nil {
		_ = tlsConn.Close()
		return
	}
	_ = tlsConn.SetReadDeadline(time.Time{})

	obs := &TrafficObservation{
		At:          time.Now().UTC(),
		Method:      "CONNECT",
		URL:         redactURL(r.Host),
		Status:      http.StatusOK,
		Intercepted: true,
	}
	tunnelAttr := baseAttr
	if headerAttr, ok := extractAttribution(r); ok {
		switch {
		case baseAttr.ScanCtxID == "" && baseAttr.InstanceID == "":
			tunnelAttr = headerAttr
		case headerAttr.Channel == ChannelBearer &&
			(headerAttr.ScanCtxID == "" || headerAttr.ScanCtxID == baseAttr.ScanCtxID):
			// A Bearer CONNECT from the same run (container HTTP worker) means
			// the tunnel's requests already met the tool-layer gate; keep that
			// channel so the proxy does not re-adjudicate them.
			tunnelAttr = headerAttr
		}
	}
	if tunnelAttr.ScanCtxID != "" || tunnelAttr.InstanceID != "" {
		obs.ScanCtxID = tunnelAttr.ScanCtxID
		obs.InstanceID = tunnelAttr.InstanceID
	}
	m.observe(obs)

	inner := &http.Server{
		Handler:           &interceptHandler{m: m, hostPort: r.Host, attr: tunnelAttr},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       innerIdleTimeout,
	}
	// The tunnel is fully built (inner server included) before registration
	// publishes it, so Close never reads a half-initialized tunnel.
	t := &tunnel{conn: clientConn, inner: inner, done: make(chan struct{}), scanID: tunnelAttr.ScanCtxID}
	m.registerTunnel(t)
	// The tunnel lives as long as the CONNECTION: the one-shot listener makes
	// Serve return right after the single Accept while the connection
	// goroutine keeps serving. The inner server closes the connection when it
	// is done (naturally or via Close), and the close watcher unregisters the
	// tunnel at exactly that point.
	_ = inner.Serve(&oneShotListener{conn: &closeWatcher{Conn: tlsConn, onClose: func() { m.unregisterTunnel(t) }}})
}

// closeWatcher unregisters a tunnel when the connection is closed — the
// inner http.Server closes each served connection when it finishes, which is
// the authoritative end-of-connection signal.
type closeWatcher struct {
	net.Conn
	once    sync.Once
	onClose func()
}

func (c *closeWatcher) Close() error {
	c.once.Do(c.onClose)
	return c.Conn.Close()
}

// bufferedConn serves reads from the hijack's buffered reader and writes to
// the raw connection.
type bufferedConn struct {
	r *bufio.Reader
	c net.Conn
}

func (c *bufferedConn) Read(p []byte) (int, error)         { return c.r.Read(p) }
func (c *bufferedConn) Write(p []byte) (int, error)        { return c.c.Write(p) }
func (c *bufferedConn) Close() error                       { return c.c.Close() }
func (c *bufferedConn) LocalAddr() net.Addr                { return c.c.LocalAddr() }
func (c *bufferedConn) RemoteAddr() net.Addr               { return c.c.RemoteAddr() }
func (c *bufferedConn) SetDeadline(t time.Time) error      { return c.c.SetDeadline(t) }
func (c *bufferedConn) SetReadDeadline(t time.Time) error  { return c.c.SetReadDeadline(t) }
func (c *bufferedConn) SetWriteDeadline(t time.Time) error { return c.c.SetWriteDeadline(t) }

// interceptHandler serves the plaintext half of an intercepted tunnel. Every
// request is rewritten to its absolute https origin form and forwarded through
// the normal observation + upstream path, carrying the tunnel's attribution:
// inner requests cannot carry the run token themselves (the client believes
// it talks origin-to-origin).
type interceptHandler struct {
	m        *Manager
	hostPort string
	attr     Attribution
}

func (h *interceptHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host == "" {
		r.Host = h.hostPort
	}
	(&proxyHandler{m: h.m}).forward(w, r, "https", h.attr)
}

// oneShotListener serves exactly one pre-established connection.
type oneShotListener struct {
	conn     net.Conn
	accepted bool
}

func (l *oneShotListener) Accept() (net.Conn, error) {
	if l.accepted {
		return nil, errors.New("trafficproxy: one-shot listener exhausted")
	}
	l.accepted = true
	return l.conn, nil
}

func (l *oneShotListener) Close() error   { return nil }
func (l *oneShotListener) Addr() net.Addr { return l.conn.LocalAddr() }

// AttributionRoundTripper stamps the scan's run token onto requests sent
// through the loopback proxy. It is installed by HTTP tool transports ONLY
// when TLS interception is active: a blind tunnel forwards to a remote
// upstream that must never see the run token. The header is hop-by-hop and
// stripped by the proxy before the upstream hop; an explicitly set
// Proxy-Authorization (the agent may be testing proxy auth) is never
// overwritten.
func AttributionRoundTripper(inner http.RoundTripper, scanCtxID string) http.RoundTripper {
	if inner == nil || strings.TrimSpace(scanCtxID) == "" {
		return inner
	}
	return &AttributionTransport{Inner: inner, ScanCtxID: strings.TrimSpace(scanCtxID)}
}

// AttributionTransport stamps the scan's run token onto requests. The token
// lookup happens per request so re-registered sessions (resume) attribute
// correctly, and nothing is stamped toward a remote upstream when
// interception is inactive.
type AttributionTransport struct {
	Inner     http.RoundTripper
	ScanCtxID string
}

// Unwrap exposes the wrapped transport (for white-box assertions).
func (a *AttributionTransport) Unwrap() http.RoundTripper { return a.Inner }

func (a *AttributionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if MITMEnabled() && req.Header.Get("Proxy-Authorization") == "" {
		if token := RunTokenFor(a.ScanCtxID); token != "" {
			stamped := req.Clone(req.Context())
			stamped.Header.Set("Proxy-Authorization", "Bearer "+token)
			return a.Inner.RoundTrip(stamped)
		}
	}
	return a.Inner.RoundTrip(req)
}
