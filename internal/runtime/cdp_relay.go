package runtime

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CDPProvider is implemented by drivers that can host a Chromium DevTools
// endpoint inside a project container.
type CDPProvider interface {
	CDPEndpoint(ctx context.Context, projectID, proxyURL string) (string, error)
}

// CDPRelay forwards the container's loopback-bound DevTools port to an address
// the platform can reach.
//
// DockerDriver probes the published port on the host's 127.0.0.1, which is
// unreachable from a platform that itself runs in a container. runnerd owns
// that host network, so it accepts a relay listener, keeps the upstream
// address current across container restarts, and advertises a host name the
// platform resolves (for example host.docker.internal).
type CDPRelay struct {
	bind      string
	advertise string
	logf      func(format string, args ...any)

	mu     sync.Mutex
	relays map[string]*cdpRelay
}

type cdpRelay struct {
	listener net.Listener

	targetMu sync.Mutex
	target   string

	closeOnce sync.Once
}

// NewCDPRelay builds a relay. bind is the listener address (default
// 127.0.0.1:0); advertise is the host the platform should dial (empty derives
// it from bind / the first non-loopback interface).
func NewCDPRelay(bind, advertise string) *CDPRelay {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		bind = "127.0.0.1:0"
	}
	return &CDPRelay{
		bind:      bind,
		advertise: strings.TrimSpace(advertise),
		logf:      log.Printf,
		relays:    make(map[string]*cdpRelay),
	}
}

// SetLogger overrides the logger (tests).
func (r *CDPRelay) SetLogger(fn func(string, ...any)) {
	if fn != nil {
		r.logf = fn
	}
}

// Endpoint resolves the project's CDP endpoint and returns a relay URL the
// platform can dial. Repeated calls reuse the listener and refresh its upstream
// target, because a replaced container can change the published host port.
func (r *CDPRelay) Endpoint(ctx context.Context, provider CDPProvider, projectID, proxyURL string) (string, error) {
	if provider == nil {
		return "", NewExecError(CodeUnsupported, PhaseExec, "this driver cannot host a CDP endpoint")
	}
	upstream, err := provider.CDPEndpoint(ctx, projectID, proxyURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Host == "" {
		return "", NewExecError(CodeContainerStart, PhaseExec, "invalid CDP endpoint %q", upstream)
	}
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "ws"
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if host == "" || port == "" {
		return "", NewExecError(CodeContainerStart, PhaseExec, "CDP endpoint %q has no host:port", upstream)
	}
	target := net.JoinHostPort(host, port)

	r.mu.Lock()
	relay := r.relays[projectID]
	if relay == nil {
		listener, listenErr := net.Listen("tcp", r.bind)
		if listenErr != nil {
			r.mu.Unlock()
			return "", NewExecError(CodeRuntimeUnavailable, PhaseExec, "listen for CDP relay: %v", listenErr)
		}
		relay = &cdpRelay{listener: listener, target: target}
		r.relays[projectID] = relay
		go relay.serve(r.logf)
	} else {
		relay.setTarget(target)
	}
	advertise := r.advertiseHostLocked(relay)
	relayPort := relay.port()
	r.mu.Unlock()

	path := parsed.Path
	if path == "" {
		path = "/"
	}
	endpoint := fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort(advertise, strconv.Itoa(relayPort)), path)
	return endpoint, nil
}

// Remove closes one project's relay (container stopped/removed).
func (r *CDPRelay) Remove(projectID string) {
	r.mu.Lock()
	relay := r.relays[projectID]
	delete(r.relays, projectID)
	r.mu.Unlock()
	if relay != nil {
		_ = relay.listener.Close()
	}
}

// Close closes every relay.
func (r *CDPRelay) Close() error {
	r.mu.Lock()
	relays := make([]*cdpRelay, 0, len(r.relays))
	for id, relay := range r.relays {
		relays = append(relays, relay)
		delete(r.relays, id)
	}
	r.mu.Unlock()
	for _, relay := range relays {
		_ = relay.listener.Close()
	}
	return nil
}

func (r *CDPRelay) advertiseHostLocked(relay *cdpRelay) string {
	if r.advertise != "" {
		return r.advertise
	}
	if addr, ok := relay.listener.Addr().(*net.TCPAddr); ok && !addr.IP.IsUnspecified() && !addr.IP.IsLoopback() {
		return addr.IP.String()
	}
	host, _, err := net.SplitHostPort(r.bind)
	if err == nil && host != "" && host != "0.0.0.0" && host != "::" {
		return host
	}
	if ip := firstNonLoopbackIPv4(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

func firstNonLoopbackIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 != nil && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return ""
}

func (r *cdpRelay) port() int {
	if addr, ok := r.listener.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

func (r *cdpRelay) setTarget(target string) {
	r.targetMu.Lock()
	r.target = target
	r.targetMu.Unlock()
}

func (r *cdpRelay) currentTarget() string {
	r.targetMu.Lock()
	defer r.targetMu.Unlock()
	return r.target
}

func (r *cdpRelay) serve(logf func(string, ...any)) {
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = conn.Close() }()
			upstream, dialErr := net.DialTimeout("tcp", r.currentTarget(), 5*time.Second)
			if dialErr != nil {
				logf("[cdp-relay] dial %s: %v", r.currentTarget(), dialErr)
				return
			}
			defer func() { _ = upstream.Close() }()
			pipeBoth(conn, upstream)
		}()
	}
}

// pipeBoth copies bytes in both directions and half-closes each side so a
// websocket close handshake is not truncated.
func pipeBoth(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(b, a)
		if tcp, ok := b.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, b)
		if tcp, ok := a.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}
