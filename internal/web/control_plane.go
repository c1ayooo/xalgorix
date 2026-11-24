package web

import (
	"net/url"
	"strings"
)

// controlPlaneHost normalizes a configured control-plane entry to a bare host.
func controlPlaneHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			return strings.TrimSuffix(u.Hostname(), ".")
		}
	}
	if strings.Count(raw, ":") == 1 {
		if host, _, ok := strings.Cut(raw, ":"); ok {
			raw = host
		}
	}
	return strings.TrimSuffix(raw, ".")
}

// targetHost normalizes a scan target (URL, host, or IP) to a bare host.
func targetHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "//" + raw
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return strings.TrimSuffix(u.Hostname(), ".")
	}
	return controlPlaneHost(raw)
}

// allTargetsControlPlaneHosts reports whether every target is a control-plane
// host (or a subdomain of one). Control-plane-only runs cannot be modeled by
// the worker pool yet: the real targets are challenge containers discovered
// through the platform API, not the platform itself.
func allTargetsControlPlaneHosts(targets, controlPlane []string) bool {
	if len(targets) == 0 || len(controlPlane) == 0 {
		return false
	}
	hosts := make(map[string]bool, len(controlPlane))
	for _, raw := range controlPlane {
		if host := controlPlaneHost(raw); host != "" {
			hosts[host] = true
		}
	}
	if len(hosts) == 0 {
		return false
	}
	for _, target := range targets {
		host := targetHost(target)
		if host == "" {
			return false
		}
		matched := false
		for cp := range hosts {
			if host == cp || strings.HasSuffix(host, "."+cp) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
