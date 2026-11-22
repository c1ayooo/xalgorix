package config

import (
	"log"
	"strings"
	"testing"
)

// TestApprovalDefaults pins the R2a policy defaults (REDESIGN §5 / D5): the
// pipeline ships OFF (enforce is an explicit operator choice — a headless
// scheduled scan must never stall waiting for approvals), with the contract
// timeout of 600s and the single-pending-slot anti-storm default.
func TestApprovalDefaults(t *testing.T) {
	t.Setenv("XALGORIX_APPROVAL_MODE", "")
	t.Setenv("XALGORIX_APPROVAL_TIMEOUT", "")
	t.Setenv("XALGORIX_APPROVAL_ALLOWLIST", "")
	t.Setenv("XALGORIX_APPROVAL_MAX_PENDING", "")
	cfg := load()
	if cfg.ApprovalMode != "off" {
		t.Errorf("ApprovalMode default = %q, want off (D5: enforce is opt-in)", cfg.ApprovalMode)
	}
	if cfg.ApprovalTimeoutSeconds != 600 {
		t.Errorf("ApprovalTimeoutSeconds default = %d, want 600", cfg.ApprovalTimeoutSeconds)
	}
	if cfg.ApprovalAllowlist != "" {
		t.Errorf("ApprovalAllowlist default = %q, want empty", cfg.ApprovalAllowlist)
	}
	if cfg.ApprovalMaxPending != 1 {
		t.Errorf("ApprovalMaxPending default = %d, want 1", cfg.ApprovalMaxPending)
	}
}

// TestApprovalModeNormalization pins the fail-open-on-config-error posture:
// unknown values fall back to off WITH a loud warning; "enforce" (any
// case/spacing) is the only way in. A typo must never silently park headless
// scans.
func TestApprovalModeNormalization(t *testing.T) {
	cases := map[string]string{
		"enforce":   "enforce",
		" Enforce ": "enforce",
		"ENFORCE":   "enforce",
		"off":       "off",
		"":          "off",
		"wat":       "off",
		"on":        "off",
	}
	for raw, want := range cases {
		if got := normalizeApprovalMode(raw); got != want {
			t.Errorf("normalizeApprovalMode(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestApprovalModeNormalizationWarnsOnUnknown pins L1: a non-empty unknown
// value logs the same WARN shape as other config downgrades; empty and
// explicit "off" stay silent.
func TestApprovalModeNormalizationWarnsOnUnknown(t *testing.T) {
	var buf strings.Builder
	original := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(original)

	_ = normalizeApprovalMode("wat")
	if !strings.Contains(buf.String(), "WARN") || !strings.Contains(buf.String(), "XALGORIX_APPROVAL_MODE") {
		t.Errorf("unknown mode must WARN, log: %q", buf.String())
	}

	buf.Reset()
	_ = normalizeApprovalMode("")
	_ = normalizeApprovalMode("off")
	if strings.Contains(buf.String(), "WARN") {
		t.Errorf("off/empty must stay silent, got: %q", buf.String())
	}
}
