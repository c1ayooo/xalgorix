package agent

// scope_guard.go — the project engagement scope, enforced per host.
//
// The operator-machine guard in agent_guard.go is unconditional: it protects
// the machine the scanner runs on. The engagement scope is a second, opt-in
// layer that narrows what a run may TOUCH to the project's authorized targets.
// It is active only when the project's fence says so (Fence.Enforce), so a
// project without an enforced fence — and every benchmark/control-plane run —
// behaves exactly as before this layer existed.
//
// Matching is deterministic and deliberately small (v1):
//
//	example.com       exact host and every subdomain (suffix domain)
//	*.example.com     every subdomain, but not the apex
//	10.0.0.1          exact IPv4 literal
//	10.0.0.0/24       any IPv4 literal inside the CIDR
//
// DenyTargets run the same match and override the allow list. allow_ports is
// consulted only when the reference names a port: an empty list means every
// port on an in-scope host is allowed, and an unnamed port is never treated as
// a violation (the matcher cannot invent one).

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// EngagementScope is one run's enforced project boundary. Targets and Deny are
// host entries (possibly "*.domain" or CIDR); AllowPorts is the project's
// allow_ports list (empty = unrestricted).
type EngagementScope struct {
	Targets    []string
	Deny       []string
	AllowPorts []int
}

// DestinationDenied reports whether raw is explicitly excluded by the deny
// list, independent of the allow list. The dispatch judge needs the
// distinction: a denied target is a hard stop (no approval is ever offered),
// while an out-of-scope target may be reached through an operator-issued
// capability (INTEL_HANDOFF_SPEC D7).
func (s *EngagementScope) DestinationDenied(raw string) (bool, string) {
	if s == nil || len(s.Deny) == 0 {
		return false, ""
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, ""
	}
	host, _ := referenceHostPort(raw)
	if host == "" {
		return false, ""
	}
	if scopeEntriesMatch(s.Deny, host) {
		return true, "the target is explicitly denied by the project fence"
	}
	return false, ""
}

// Configured reports whether an allow list is installed. A scope intent on a
// run without one stays undispatchable (the pre-M3 rule, kept verbatim).
func (s *EngagementScope) Configured() bool {
	return s != nil && len(s.Targets) > 0
}

// DestinationAllowed reports whether raw (URL, host:port, bare host, or a
// literal scope entry such as a CIDR/wildcard) is inside the engagement scope,
// not denied, and on an allowed port. The returned reason is a short clause
// suitable for a refusal message; it is empty when allowed.
func (s *EngagementScope) DestinationAllowed(raw string) (bool, string) {
	if s == nil || len(s.Targets) == 0 {
		return true, ""
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true, "" // nothing named, nothing to police
	}
	// A CIDR destination names a range: it is authorized only when the WHOLE
	// range is inside the allow list. Checking just the network address would
	// widen the scope (a /24 is not authorized because its base is).
	if _, targetNet, err := net.ParseCIDR(raw); err == nil {
		return s.cidrDestinationAllowed(targetNet)
	}
	// A bare IP literal (including IPv6 without brackets) is its own authority:
	// referenceHostPort parses URLs/authorities and would reject the unbracketed
	// form the command/scope layers legitimately produce.
	host, port := "", ""
	if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
		host = ip.String()
	} else {
		host, port = referenceHostPort(raw)
	}
	if host == "" {
		return false, "it is not a recognizable host"
	}
	if len(s.Deny) > 0 && scopeEntriesMatch(s.Deny, host) {
		return false, "the target is explicitly denied"
	}
	if !scopeEntriesMatch(s.Targets, host) {
		return false, "it is not an authorized scope target"
	}
	if port != "" && !scopePortAllowed(s.AllowPorts, port) {
		return false, fmt.Sprintf("port %s is not in the engagement allow_ports", port)
	}
	return true, ""
}

// cidrDestinationAllowed judges a range destination. Deny entries (hosts or
// networks) that overlap the range forbid it; an allow entry must be a network
// that contains the whole range.
func (s *EngagementScope) cidrDestinationAllowed(target *net.IPNet) (bool, string) {
	for _, entry := range s.Deny {
		normalized := normalizeScopeEntry(entry)
		if normalized == "" {
			continue
		}
		if _, denyNet, err := net.ParseCIDR(normalized); err == nil {
			if cidrOverlaps(denyNet, target) {
				return false, "the target is explicitly denied"
			}
			continue
		}
		if ip := net.ParseIP(normalized); ip != nil && target.Contains(ip) {
			return false, "the target is explicitly denied"
		}
	}
	for _, entry := range s.Targets {
		normalized := normalizeScopeEntry(entry)
		if normalized == "" {
			continue
		}
		_, allowNet, err := net.ParseCIDR(normalized)
		if err != nil {
			continue // a host entry can never authorize a whole range
		}
		if cidrContains(allowNet, target) {
			return true, ""
		}
	}
	return false, "it is not an authorized scope target"
}

// cidrContains reports whether outer covers every address in inner.
func cidrContains(outer, inner *net.IPNet) bool {
	outerOnes, outerBits := outer.Mask.Size()
	innerOnes, innerBits := inner.Mask.Size()
	if outerBits != innerBits || outerOnes > innerOnes {
		return false
	}
	return outer.Contains(inner.IP)
}

// cidrOverlaps reports whether the two networks share any address.
func cidrOverlaps(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// engagementScopeBlocked is the Agent-side check. It returns a refusal message
// when raw is outside an enforced engagement scope.
func (a *Agent) engagementScopeBlocked(raw string) (bool, string) {
	scope := a.engagementScopeSnapshot()
	if a == nil || scope == nil {
		return false, ""
	}
	ok, reason := scope.DestinationAllowed(raw)
	if ok {
		return false, ""
	}
	return true, fmt.Sprintf(
		"%q is outside the engagement scope (%s). Refusing to probe a host the project scope does not authorize.",
		strings.TrimSpace(raw), reason,
	)
}

// scopeHostNeedsCheck reports whether a bare host token extracted from a
// command is worth evaluating against the scope. Python sources yield dotted
// identifiers ("requests.get", "os.path") whose last label is not a real TLD;
// treating those as destinations would block every script. A URL or host:port
// reference to the same name is still checked by the caller, so this only
// narrows the host-only case. In-scope and denied names are always checked,
// and public hostnames (managed TLD) are always checked.
func scopeHostNeedsCheck(scope *EngagementScope, host string) bool {
	if scope == nil {
		return false
	}
	if _, ok := destinationLiteral(host); ok {
		return true
	}
	if scopeEntriesMatch(scope.Targets, host) || scopeEntriesMatch(scope.Deny, host) {
		return true
	}
	return !unmanagedTLDHost(host)
}

// scopeEntriesMatch reports whether host matches any entry in entries.
func scopeEntriesMatch(entries []string, host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return false
	}
	// A zone suffix ("fe80::1%eth0") names the same address.
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	for _, raw := range entries {
		entry := normalizeScopeEntry(raw)
		if entry == "" {
			continue
		}
		if suffix, ok := strings.CutPrefix(entry, "*."); ok {
			suffix = strings.TrimSuffix(suffix, ".")
			// A wildcard names subdomains only; the apex is not included.
			if suffix != "" && strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if strings.Contains(entry, "/") {
			if ip == nil {
				continue // a CIDR entry never matches a hostname
			}
			if _, ipnet, err := net.ParseCIDR(entry); err == nil && ipnet.Contains(ip) {
				return true
			}
			continue
		}
		if entry == host {
			return true
		}
		if net.ParseIP(entry) != nil {
			continue // an IP literal matches only itself, handled above
		}
		// Plain domain: apex plus subdomains (suffix domain).
		if strings.HasSuffix(host, "."+entry) {
			return true
		}
	}
	return false
}

// normalizeScopeEntry reduces a scope entry to its host form. URLs lose their
// scheme/path; a "domain:" prefix is dropped; wildcard and CIDR entries are
// preserved because their matching rules differ.
func normalizeScopeEntry(raw string) string {
	entry := strings.TrimSpace(strings.ToLower(raw))
	if entry == "" {
		return ""
	}
	if strings.HasPrefix(entry, "domain:") {
		entry = strings.TrimPrefix(entry, "domain:")
	}
	if strings.HasPrefix(entry, "*.") {
		return strings.TrimSuffix(entry, ".")
	}
	if strings.Contains(entry, "/") {
		if _, _, err := net.ParseCIDR(entry); err == nil {
			return entry
		}
	}
	if host, _ := referenceHostPort(entry); host != "" {
		return strings.TrimSuffix(host, ".")
	}
	return strings.TrimSuffix(entry, ".")
}

// scopePortAllowed reports whether port (a decimal string, "" when unnamed) is
// permitted by allow. An empty allow list permits every port; an unnamed port
// cannot be checked and is permitted.
func scopePortAllowed(allow []int, port string) bool {
	if len(allow) == 0 || port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	for _, p := range allow {
		if p == n {
			return true
		}
	}
	return false
}
