// Package trafficproxy implements the single-process shared loopback MITM
// proxy (REDESIGN §4/D4, R2b). Every outbound scan HTTP consumer can route
// through a local loopback listener which
//
//   - forwards through the configured upstream proxy pool (internal/proxy)
//     with per-request rotation, or dials the destination directly when no
//     upstream pool is configured;
//   - intercepts CONNECT tunnels when the traffic-proxy CA is available,
//     observing each request/response as an observed InfoItem in the owning
//     Run's domain journal (off the request path, buffered, bounded);
//   - attributes traffic to a Run via a per-scan bearer token carried in
//     Proxy-Authorization, read BEFORE the hop-by-hop strip;
//   - applies the operator-machine dial guard to direct upstream dials, so
//     the third security ring (GuardDials) survives the MITM hop.
//
// XALGORIX_TRAFFICPROXY=off restores the pre-R2b behavior byte-for-byte: the
// loopback bridge is then only available in proxy-required mode and CONNECT
// stays a blind tunnel (no CA, no interception, no observation).
//
// The proxy does NOT own scope decisions (charter §9 ring 1): the agent's
// scope guards stay at the pre-proxy layer. The dial guard here only keeps
// the operator-machine boundary that tools.GuardDials provided when tools
// dialed destinations themselves.
package trafficproxy

import (
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/proxy"
)

// Manager owns the loopback proxy listener and the interception state. It is
// initialized once via Configure and accessed through the package helpers.
type Manager struct {
	enabled       bool // traffic proxy requested on (XALGORIX_TRAFFICPROXY, default on)
	mitm          bool // TLS interception active (enabled && CA loaded); false → legacy blind tunnel
	tlsSkipVerify bool // XALGORIX_TLS_SKIP_VERIFY — target-side tls.Client semantics
	ca            *CA

	startOnce sync.Once

	mu       sync.Mutex
	closed   bool
	listener net.Listener
	server   *http.Server
	localURL string
	localErr error

	sink atomic.Pointer[BufferedSink] // buffered observed-traffic writer (never on the request path)

	// dialGuard runs on net.Dialer.Control for direct (unproxied) dials —
	// post-DNS, on the exact address (the tools.GuardDials mechanism).
	dialGuard DialGuardFunc

	transMu                    sync.Mutex
	transClosed                bool
	upstreamTransports         map[string]*http.Transport // keyed by proxy raw string
	directTransports           map[string]*http.Transport // keyed by scan context id ("" = system)
	insecureUpstreamTransports map[string]*http.Transport // skip-verify twins (cert-error retry)
	insecureDirectTransports   map[string]*http.Transport

	tunnelsMu sync.Mutex
	tunnels   map[*tunnel]struct{}
}

var defaultManager atomic.Pointer[Manager]

// DialGuardFunc reports why a direct upstream dial must not happen. The first
// argument is the owning scan context id ("" for system-level traffic).
type DialGuardFunc func(scanCtxID, address string) error

// SetDialGuard replaces the package dial guard (tests install stubs; the
// default consults the per-scan scopeguard configuration registered via
// RegisterRun and the process base from SetDialGuardBase). Returns the
// previous guard so callers can restore it.
func SetDialGuard(g DialGuardFunc) DialGuardFunc {
	dialGuardMu.Lock()
	defer dialGuardMu.Unlock()
	prev := packageDialGuard
	packageDialGuard = g
	return prev
}

// Configure initializes the package-level manager. enabled carries
// XALGORIX_TRAFFICPROXY; caDir carries XALGORIX_CA_DIR; tlsSkipVerify carries
// XALGORIX_TLS_SKIP_VERIFY (the MITM's target-side tls.Client follows the
// same semantics: verify by default, skip when configured). A broken CA is
// fail-closed for INTERCEPTION only — it is logged loudly, MITM is refused
// and the bridge keeps forwarding as a blind tunnel (the engine keeps
// running; a CA is never silently re-signed, which would strand every
// consumer trust store).
func Configure(enabled bool, caDir string, tlsSkipVerify bool) error {
	m := &Manager{
		enabled:                    enabled,
		tlsSkipVerify:              tlsSkipVerify,
		tunnels:                    make(map[*tunnel]struct{}),
		upstreamTransports:         make(map[string]*http.Transport),
		directTransports:           make(map[string]*http.Transport),
		insecureUpstreamTransports: make(map[string]*http.Transport),
		insecureDirectTransports:   make(map[string]*http.Transport),
		dialGuard:                  currentDialGuard(),
	}
	if enabled && strings.TrimSpace(caDir) != "" {
		ca, err := LoadOrCreate(caDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[trafficproxy] CA unavailable at %s — MITM interception refused (fail-closed), forwarding without observation: %v\n", caDir, err)
		} else {
			m.ca = ca
			m.mitm = true
		}
	}
	m.sink.Store(NewBufferedSink(observeTraffic, observationQueueSize))
	if old := defaultManager.Swap(m); old != nil {
		_ = old.Close()
	}
	return nil
}

// Enabled reports whether the traffic proxy is switched on (XALGORIX_TRAFFICPROXY).
func Enabled() bool {
	m := defaultManager.Load()
	return m != nil && m.enabled
}

// MITMEnabled reports whether CONNECT tunnels are TLS-intercepted and observed.
func MITMEnabled() bool {
	m := defaultManager.Load()
	return m != nil && m.mitm
}

// CAPath returns the path of the traffic-proxy CA certificate, or "" when
// no CA is loaded. Consumers embed it into curl/wget/python TLS config.
func CAPath() string {
	m := defaultManager.Load()
	if m == nil || m.ca == nil {
		return ""
	}
	return m.ca.CertPath()
}

// BundlePath returns the combined (traffic-proxy CA + system) trust bundle
// path, or "" when no CA is loaded.
func BundlePath() string {
	m := defaultManager.Load()
	if m == nil || m.ca == nil {
		return ""
	}
	return m.ca.BundlePath()
}

// RootCAs returns the pool trusting the traffic-proxy CA, or nil when no CA
// is loaded (consumers must then not override the transport trust store).
func RootCAs() *x509.CertPool {
	m := defaultManager.Load()
	if m == nil || m.ca == nil {
		return nil
	}
	return m.ca.CertPool()
}

// LocalURL returns the loopback HTTP proxy URL. Available when the traffic
// proxy is enabled (default on) or in proxy-required mode; the manager
// refuses a closed manager. Upstream enforcement is per dial: in
// proxy-required mode the forward path fails closed when the configured
// upstream is unavailable and never dials the destination directly (pinned
// by TestLocalProxyNeverFallsBackDirect).
func LocalURL() (string, error) {
	m := defaultManager.Load()
	if m == nil {
		return "", fmt.Errorf("traffic proxy is not initialized")
	}
	if !m.enabled && !proxy.Required() {
		return "", fmt.Errorf("traffic proxy requires XALGORIX_TRAFFICPROXY enabled or proxy-required mode")
	}
	m.startOnce.Do(func() {
		// The whole start is serialized against Close on m.mu, so either the
		// listener starts and Close tears it down, or Close ran first and the
		// start refuses. No window can leak a listener on a closed manager.
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed {
			m.localErr = fmt.Errorf("traffic proxy manager is closed")
			return
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			m.localErr = fmt.Errorf("listen on loopback: %w", err)
			return
		}
		m.localURL = "http://" + ln.Addr().String()
		m.listener = ln
		srv := &http.Server{
			Handler:           &proxyHandler{m: m},
			ReadHeaderTimeout: 10 * time.Second,
		}
		if m.mitm {
			// Explicit idle bound for intercepted tunnels; the legacy blind
			// tunnel keeps its historical no-idle-timeout behavior.
			srv.IdleTimeout = 120 * time.Second
		}
		m.server = srv
		go func() { _ = srv.Serve(ln) }()
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", fmt.Errorf("traffic proxy manager is closed")
	}
	return m.localURL, m.localErr
}

// Close shuts down the manager: the loopback listener, the HTTP server, every
// tracked tunnel (hijacked or intercepted connections are not tracked by
// http.Server) and idle connections in the forwarding transports. Close waits
// for tracked tunnels to unwind so tests can assert deterministic cleanup.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	// Snapshot the tunnels while still holding m.mu: registerTunnel takes
	// the same mutex across its insert, so nothing can slip in after the
	// snapshot and leak a hijacked connection.
	m.tunnelsMu.Lock()
	tunnels := make([]*tunnel, 0, len(m.tunnels))
	for t := range m.tunnels {
		tunnels = append(tunnels, t)
	}
	m.tunnelsMu.Unlock()
	var firstErr error
	if m.server != nil {
		if err := m.server.Close(); err != nil {
			firstErr = err
		}
		m.server = nil
	}
	// Serve may not have started yet, in which case http.Server.Close has
	// not registered the listener and cannot close it for us.
	if m.listener != nil {
		_ = m.listener.Close()
		m.listener = nil
	}
	m.localURL = ""
	// Never reset startOnce while LocalURL may still be using it. A closed
	// manager is terminal; Configure constructs a fresh manager instead.

	for _, t := range tunnels {
		t.closeConn()
	}
	for _, t := range tunnels {
		select {
		case <-t.done:
		case <-time.After(5 * time.Second):
			return firstErr // bounded wait; a wedged tunnel is visible to the operator
		}
	}
	m.closeTransports()
	if sink := m.sink.Load(); sink != nil {
		sink.Close()
	}
	return firstErr
}

// Close shuts down the package-level traffic proxy manager.
func Close() error {
	return defaultManager.Swap(nil).Close()
}

// Reset clears the package-level manager.
func Reset() {
	_ = Close()
}

// tunnel tracks one hijacked CONNECT connection (blind or intercepted) so
// Close can tear it down deterministically — http.Server stops tracking
// connections once they are hijacked.
type tunnel struct {
	conn  net.Conn     // the hijacked client-side connection
	inner *http.Server // per-connection server for intercepted tunnels; nil when blind
	done  chan struct{}
}

func (m *Manager) registerTunnel(t *tunnel) {
	// m.mu spans the closed check AND the insert: Close snapshots the tunnel
	// set under m.mu after marking closed, so no tunnel can register unseen.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.tunnelsMu.Lock()
	m.tunnels[t] = struct{}{}
	m.tunnelsMu.Unlock()
}

func (m *Manager) unregisterTunnel(t *tunnel) {
	m.tunnelsMu.Lock()
	delete(m.tunnels, t)
	m.tunnelsMu.Unlock()
	close(t.done)
}

func (t *tunnel) closeConn() {
	if t.inner != nil {
		_ = t.inner.Close()
	}
	_ = t.conn.Close()
}

// TunnelCount reports the number of tracked in-flight tunnels. Test-only
// seam (exported for white-box deterministic close assertions; not used on
// the request path).
func TunnelCount() int {
	m := defaultManager.Load()
	if m == nil {
		return 0
	}
	m.tunnelsMu.Lock()
	defer m.tunnelsMu.Unlock()
	return len(m.tunnels)
}

// closed reports whether this manager is shut down.
func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// observe hands one observation to the buffered sink. Never blocks the
// request path: overflow drops and counts.
func (m *Manager) observe(obs *TrafficObservation) {
	if m == nil || obs == nil {
		return
	}
	if sink := m.sink.Load(); sink != nil {
		sink.Enqueue(*obs)
	}
}

// SetObservationSink replaces the buffered observation sink of the current
// manager. Production Configure installs the journal-writing sink; tests
// install capture sinks. The previous sink is drained and stopped.
func SetObservationSink(s Sink) {
	m := defaultManager.Load()
	if m == nil || s == nil {
		return
	}
	if old := m.sink.Swap(NewBufferedSink(s, observationQueueSize)); old != nil {
		old.Close()
	}
}

// Environment gives proxy-aware subprocesses the shared loopback traffic
// proxy. This is not a security boundary for programs that ignore proxy
// environment variables; those require OS/container network isolation.
func Environment(base []string) ([]string, error) {
	localURL, err := LocalURL()
	if err != nil {
		return nil, err
	}
	env := make([]string, 0, len(base)+8)
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if ok {
			switch strings.ToLower(key) {
			case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
				continue
			}
		}
		env = append(env, kv)
	}
	return append(env,
		"HTTP_PROXY="+localURL,
		"HTTPS_PROXY="+localURL,
		"ALL_PROXY="+localURL,
		"http_proxy="+localURL,
		"https_proxy="+localURL,
		"all_proxy="+localURL,
		"NO_PROXY=localhost,127.0.0.1,::1",
		"no_proxy=localhost,127.0.0.1,::1",
	), nil
}
