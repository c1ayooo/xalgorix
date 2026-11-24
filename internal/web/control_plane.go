package web

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
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

// scopeControlPlaneHosts narrows the process-wide control-plane denylist to
// the hosts a single dispatch actually declares (targets, operator
// instruction, goal). The env list is operator-global, but its entries can
// belong to one engagement only: leaking them into every other project's
// prompts puts a foreign project's hosts into notes and reports (the
// 2026-11-24 isolation report). A run keeps only the entries its own
// dispatch names, so a run that does not name a host never learns it exists.
//
// Matching is boundary-aware so 110.0.100.58 does not match 10.0.100.58.
// A preceding dot is allowed (a declared subdomain still names its control
// plane), a trailing dot is not (host.evil must not match host).
func scopeControlPlaneHosts(global []string, declared ...string) []string {
	if len(global) == 0 {
		return nil
	}
	texts := make([]string, 0, len(declared))
	for _, raw := range declared {
		if text := strings.ToLower(strings.TrimSpace(raw)); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return nil
	}
	out := make([]string, 0, len(global))
	for _, raw := range global {
		host := controlPlaneHost(raw)
		if host == "" {
			continue
		}
		pattern := regexp.MustCompile(`(^|[^a-z0-9-])` + regexp.QuoteMeta(host) + `([^a-z0-9.-]|$)`)
		for _, text := range texts {
			if pattern.MatchString(text) {
				out = append(out, raw)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// scopedScanConfig returns a copy of cfg whose ControlPlaneHosts only names
// hosts this dispatch declares. The caller's config is never mutated: every
// dispatch site hands runRegisteredMultiScan its own copy, and concurrent
// runs must not share a narrowed slice.
func scopedScanConfig(cfg *config.Config, req ScanRequest) *config.Config {
	if cfg == nil || len(cfg.ControlPlaneHosts) == 0 {
		return cfg
	}
	scoped := *cfg
	declared := make([]string, 0, len(req.Targets)+4)
	declared = append(declared, req.Name, req.Instruction, req.ObjectiveGoal, req.ObjectiveInstruction)
	declared = append(declared, req.Targets...)
	scoped.ControlPlaneHosts = scopeControlPlaneHosts(cfg.ControlPlaneHosts, declared...)
	return &scoped
}
