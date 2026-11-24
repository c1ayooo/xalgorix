// normalize.go — the single target normalizer for the access tables.
//
// D2 of the spec: reachability is stored as structured targets, never as text
// to be reparsed at decision time (XALG020's lesson is that two parsers of the
// same scope syntax is an incident source). Hosts are canonicalized through
// scopeguard.ReadingHosts — the repo's only host reader (IDNA mapping, IPv4
// variant rewriting) — so this file adds shape, not a second dialect.

package intelaccess

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

// NormalizeTarget canonicalizes one target into the only shape the access
// tables accept: "host:port" or a CIDR network.
//
// Accepted inputs: http(s) URLs (scheme default port), host:port, [v6]:port,
// CIDR. A bare host without a port does not resolve — D2 forbids guessing a
// port, so an unportable endpoint stays a fact without grants.
func NormalizeTarget(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	// URLs first: their scheme's "//" must not be mistaken for a CIDR slash.
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		port := u.Port()
		if port == "" {
			port = schemeDefaultPort(u.Scheme)
		}
		return canonicalHostPort(u.Hostname(), port)
	}
	if strings.Contains(s, "/") {
		// A CIDR network, or the scheme-less "host:port/path" form the
		// intelligence store keeps (it strips the scheme for dedup).
		if _, network, err := net.ParseCIDR(s); err == nil {
			return network.String(), true
		}
		if authority, _, ok := strings.Cut(s, "/"); ok {
			if host, port, err := net.SplitHostPort(authority); err == nil {
				return canonicalHostPort(host, port)
			}
		}
		return "", false
	}
	if host, port, err := net.SplitHostPort(s); err == nil {
		return canonicalHostPort(host, port)
	}
	return "", false
}

// schemeDefaultPort resolves the port a URL without an explicit port implies.
func schemeDefaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http", "ws":
		return "80"
	case "https", "wss":
		return "443"
	case "ssh":
		return "22"
	case "ftp":
		return "21"
	default:
		return ""
	}
}

// canonicalHostPort lower-cases/IDNA-maps the host and validates the port. An
// IPv6 literal is bracketed so the result stays a single target shape.
func canonicalHostPort(host, port string) (string, bool) {
	portNum, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || portNum < 1 || portNum > 65535 {
		return "", false
	}
	hosts := scopeguard.ReadingHosts(strings.TrimSpace(host))
	if len(hosts) == 0 {
		return "", false
	}
	h := hosts[0]
	if h == "" {
		return "", false
	}
	if strings.Contains(h, ":") {
		return net.JoinHostPort(h, strconv.Itoa(portNum)), true
	}
	return h + ":" + strconv.Itoa(portNum), true
}

// hostOf strips the port (or keeps a CIDR) so the row's subject names the
// asset the result is about.
func hostOf(target string) string {
	if strings.Contains(target, "/") {
		return target
	}
	if host, _, err := net.SplitHostPort(target); err == nil {
		return host
	}
	return target
}
