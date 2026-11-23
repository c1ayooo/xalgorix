package web

// scan_session_notify_test.go — the summary notification must name what was
// found (operator feedback: count-only cards carry no vulnerability info).

import (
	"strings"
	"testing"
)

func TestVulnDigestList(t *testing.T) {
	vulns := []VulnSummary{
		{Title: "SQLi", Severity: "medium", Endpoint: "/login", CVSS: 6.1},
		{Title: "RCE", Severity: "critical", Endpoint: "/admin", CVSS: 9.8},
		{Title: "XSS", Severity: "low", Endpoint: "/search"},
	}
	out := vulnDigestList(vulns, 2)
	for _, want := range []string{"Findings:", "RCE", "SQLi", "CVSS 9.8", "… and 1 more"} {
		if !strings.Contains(out, want) {
			t.Fatalf("digest missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "XSS") {
		t.Fatalf("limit not enforced:\n%s", out)
	}
	// Severity ranking: critical must precede medium.
	if strings.Index(out, "RCE") > strings.Index(out, "SQLi") {
		t.Fatalf("digest not severity-ranked:\n%s", out)
	}
	if vulnDigestList(nil, 5) != "" {
		t.Fatal("empty digest for no findings")
	}
}
