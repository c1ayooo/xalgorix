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
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
)

// Manager owns the loopback proxy listener and the interception state. It is
// initialized once via Configure and accessed through the package helpers.
type Manager struct {
	enabled       bool // traffic proxy requested on (XALGORIX_TRAFFICPROXY, default on)
	mitm          bool // TLS interception active (enabled && CA loaded); false → legacy blind tunnel
	tlsSkipVerify bool // XALGORIX_TLS_SKIP_VERIFY — target-side tls.Client semantics
	ca            *CA
	// bind is the listener address; advertise overrides the host:port handed
	// to consumers when bind is unspecified (0.0.0.0/::).
	bind      string
	advertise string

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
	// record distinguishes the recorder (MITM interception + journal/archive)
	// from the forward-only egress router: the router still selects the run's
	// upstream but writes no traffic evidence.
	record bool

	// scanListeners are per-run listeners bound on the manager's bind host.
	// Chromium cannot send proxy credentials, so a port that already implies
	// the run is the only attribution channel that works for browser traffic
	// (and for containerised tools, whose env proxy URL carries no token).
	scanListeners map[string]*scanListener
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
	return ConfigureWithListen(enabled, caDir, tlsSkipVerify, "", "")
}

// ConfigureWithListen is Configure plus the listener address and the advertised
// host:port. A containerised platform whose tools run in project containers
// needs the proxy reachable from the project network: bind to an address on
// that network (or 0.0.0.0) and advertise a host name the project can resolve,
// such as host.docker.internal. Empty values preserve the historical loopback
// behaviour.
func ConfigureWithListen(enabled bool, caDir string, tlsSkipVerify bool, bind, advertise string) error {
	m := &Manager{
		enabled:                    enabled,
		record:                     true,
		tlsSkipVerify:              tlsSkipVerify,
		bind:                       strings.TrimSpace(bind),
		advertise:                  strings.TrimSpace(advertise),
		tunnels:                    make(map[*tunnel]struct{}),
		scanListeners:              make(map[string]*scanListener),
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

// ConfigureForwardOnly starts the platform-internal egress router: the same
// listener/attribution/tunnel machinery as the recorder, but with interception
// and evidence writes disabled. It exists so a task's egress proxy can change
// mid-run (and containers never need recreating) while recording is OFF.
func ConfigureForwardOnly(bind, advertise string) error {
	m := &Manager{
		enabled:                    true,
		record:                     false,
		bind:                       strings.TrimSpace(bind),
		advertise:                  strings.TrimSpace(advertise),
		tunnels:                    make(map[*tunnel]struct{}),
		scanListeners:              make(map[string]*scanListener),
		upstreamTransports:         make(map[string]*http.Transport),
		directTransports:           make(map[string]*http.Transport),
		insecureUpstreamTransports: make(map[string]*http.Transport),
		insecureDirectTransports:   make(map[string]*http.Transport),
		dialGuard:                  currentDialGuard(),
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
// LocalPort returns the loopback proxy's TCP port. Egress enforcement uses it
// to allow only the dedicated child UID to reach the proxy.
func LocalPort() (int, error) {
	raw, err := LocalURL()
	if err != nil {
		return 0, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return 0, fmt.Errorf("parse traffic proxy URL: %w", err)
	}
	port := u.Port()
	if port == "" {
		return 0, fmt.Errorf("traffic proxy URL has no port: %s", raw)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("traffic proxy URL has invalid port: %s", raw)
	}
	return n, nil
}

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
		bind := m.bind
		if bind == "" {
			bind = "127.0.0.1:0"
		}
		ln, err := net.Listen("tcp", bind)
		if err != nil {
			m.localErr = fmt.Errorf("listen on %s: %w", bind, err)
			return
		}
		m.localURL = proxyURLForListener(ln.Addr(), m.advertise)
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

// proxyURLForListener builds the URL handed to consumers. An unspecified bind
// (0.0.0.0/::) is not dialable, so it needs an explicit advertise host; without
// one the URL falls back to loopback for in-process consumers.
func proxyURLForListener(addr net.Addr, advertise string) string {
	port := ""
	if tcp, ok := addr.(*net.TCPAddr); ok {
		port = strconv.Itoa(tcp.Port)
	}
	adv := strings.TrimSpace(advertise)
	if adv != "" {
		if strings.Contains(adv, "://") {
			if u, err := url.Parse(adv); err == nil && u.Host != "" {
				adv = u.Host
			}
		}
		if _, _, err := net.SplitHostPort(adv); err != nil {
			adv = net.JoinHostPort(adv, port)
		}
		return "http://" + adv
	}
	if tcp, ok := addr.(*net.TCPAddr); ok && !tcp.IP.IsUnspecified() {
		return "http://" + addr.String()
	}
	return "http://127.0.0.1:" + port
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
	for id, sl := range m.scanListeners {
		if sl.server != nil {
			_ = sl.server.Close()
		}
		if sl.listener != nil {
			_ = sl.listener.Close()
		}
		delete(m.scanListeners, id)
	}
	m.closeTransports()
	if sink := m.sink.Load(); sink != nil {
		sink.Close()
	}
	// Best-effort audit flush: usage accumulated by this manager must not be
	// lost when settings re-configure or the process shuts down.
	go func() { _ = FlushEgressUsage(context.Background()) }()
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
	conn   net.Conn     // the hijacked client-side connection
	inner  *http.Server // per-connection server for intercepted tunnels; nil when blind
	done   chan struct{}
	scanID string // owning run (attribution); used to force-close one run's tunnels
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

// CloseScanTunnels force-closes every open tunnel belonging to one run. Used
// when a task's egress proxy changes so the next request reconnects through the
// new proxy instead of draining the old connection for minutes. Returns the
// number of tunnels closed.
func (m *Manager) CloseScanTunnels(scanCtxID string) int {
	if m == nil || scanCtxID == "" {
		return 0
	}
	m.tunnelsMu.Lock()
	defer m.tunnelsMu.Unlock()
	closed := 0
	for t := range m.tunnels {
		if t.scanID != scanCtxID {
			continue
		}
		_ = t.conn.Close()
		closed++
	}
	return closed
}

// CloseScanTunnels is the package-level wrapper over the default manager.
func CloseScanTunnels(scanCtxID string) int {
	m := defaultManager.Load()
	if m == nil {
		return 0
	}
	return m.CloseScanTunnels(scanCtxID)
}

func (m *Manager) unregisterTunnel(t *tunnel) {
	m.tunnelsMu.Lock()
	delete(m.tunnels, t)
	m.tunnelsMu.Unlock()
	close(t.done)
}

// scanListener is one run-scoped loopback listener. Every request arriving on
// it is attributed to that run by construction (baseAttr), which is how
// browser traffic — Chromium has no proxy-auth channel — and containerised
// tools (whose env proxy URL carries no run token) still become observed,
// archived, and approval-gated.
type scanListener struct {
	listener net.Listener
	server   *http.Server
	url      string
	port     int
}

// ListenForScan returns the run-scoped proxy URL, binding a dedicated listener
// on first use. The listener binds on the manager's configured host with an
// ephemeral port; the returned URL always uses a dialable host (loopback when
// the bind is unspecified). The same URL is stable for the life of the run so
// Chromium's process arguments never need to change mid-scan.
func ListenForScan(scanCtxID string) (string, error) {
	m := defaultManager.Load()
	if m == nil {
		return "", fmt.Errorf("traffic proxy is not initialized")
	}
	return m.listenForScan(scanCtxID)
}

// ScanListenerPort reports the TCP port of a run-scoped listener (0,false when
// none). Container runtimes need the port alone: they address the platform
// through their project network's gateway.
func ScanListenerPort(scanCtxID string) (int, bool) {
	m := defaultManager.Load()
	if m == nil {
		return 0, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sl, ok := m.scanListeners[scanCtxID]; ok && sl.port > 0 {
		return sl.port, true
	}
	return 0, false
}

// CloseScanListener tears down one run-scoped listener (scan teardown/resume
// session rotation). Unknown ids are a no-op.
func CloseScanListener(scanCtxID string) {
	m := defaultManager.Load()
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sl, ok := m.scanListeners[scanCtxID]; ok {
		if sl.server != nil {
			_ = sl.server.Close()
		}
		if sl.listener != nil {
			_ = sl.listener.Close()
		}
		delete(m.scanListeners, scanCtxID)
	}
}

func (m *Manager) listenForScan(scanCtxID string) (string, error) {
	scanCtxID = strings.TrimSpace(scanCtxID)
	if scanCtxID == "" {
		return "", fmt.Errorf("traffic proxy: scan context id is required")
	}
	if !m.enabled && !proxy.Required() {
		return "", fmt.Errorf("traffic proxy requires XALGORIX_TRAFFICPROXY enabled or proxy-required mode")
	}
	// Read the registry before taking m.mu: ListenForScan → attributionMu and
	// UnregisterRun → attributionMu → CloseScanListener → m.mu would otherwise
	// invert the lock order.
	instanceID, _ := attributionForScan(scanCtxID)
	baseAttr := Attribution{ScanCtxID: scanCtxID, InstanceID: instanceID, Channel: ChannelSubprocess}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", fmt.Errorf("traffic proxy manager is closed")
	}
	if sl, ok := m.scanListeners[scanCtxID]; ok {
		return sl.url, nil
	}

	host := ""
	if m.bind != "" {
		if h, _, err := net.SplitHostPort(m.bind); err == nil {
			host = h
		}
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return "", fmt.Errorf("traffic proxy: listen for scan %s: %w", scanCtxID, err)
	}
	srv := &http.Server{
		Handler:           &proxyHandler{m: m, baseAttr: baseAttr},
		ReadHeaderTimeout: 10 * time.Second,
	}
	if m.mitm {
		srv.IdleTimeout = 120 * time.Second
	}
	go func() { _ = srv.Serve(ln) }()

	tcp, _ := ln.Addr().(*net.TCPAddr)
	port := 0
	if tcp != nil {
		port = tcp.Port
	}
	advHost := "127.0.0.1"
	if tcp != nil && !tcp.IP.IsUnspecified() {
		advHost = tcp.IP.String()
	}
	url := "http://" + net.JoinHostPort(advHost, strconv.Itoa(port))
	m.scanListeners[scanCtxID] = &scanListener{listener: ln, server: srv, url: url, port: port}
	return url, nil
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
	if m == nil || obs == nil || !m.record {
		return // forward-only egress router: route, never record
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
	return environmentWithProxyURL(base, localURL)
}

// proxyUserinfoPlaceholder is the fixed, non-secret password part embedded
// after the run token in the proxy URL userinfo. Some python runtimes
// (urllib/requests on 3.12+) only send Proxy-Authorization: Basic when the
// proxy userinfo carries BOTH a username and a password, so the token rides
// as the username and this placeholder as the password. The proxy's Basic
// attribution reads only the username (everything before the first ':'), so
// the placeholder never needs to be secret.
const proxyUserinfoPlaceholder = "xalgorix"

// EnvironmentForScan is Environment with the scan's run token embedded as the
// proxy URL's userinfo (http://<token>:xalgorix@127.0.0.1:<port>). curl and
// python requests/urllib translate proxy userinfo into Proxy-Authorization:
// Basic — which is the subprocess attribution channel (see attribution.go)
// and the trigger for the proxy-side high-risk approval gate (gate.go). The
// token rides the SAME registry RegisterRun already maintains; there is no
// second registry. No token for the scan context (mode off, mint failure,
// session already torn down) degrades honestly to the unattributed
// system-level form — the traffic still forwards, it just never claims a Run.
func EnvironmentForScan(base []string, scanCtxID string) ([]string, error) {
	localURL, err := LocalURL()
	if err != nil {
		return nil, err
	}
	if token := RunTokenFor(scanCtxID); token != "" {
		if u, perr := url.Parse(localURL); perr == nil {
			u.User = url.UserPassword(token, proxyUserinfoPlaceholder)
			localURL = u.String()
		}
	}
	return environmentWithProxyURL(base, localURL)
}

func environmentWithProxyURL(base []string, localURL string) ([]string, error) {
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
