package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

func TestEngagementScopeMatching(t *testing.T) {
	scope := &EngagementScope{
		Targets:    []string{"example.com", "*.wild.test", "10.0.0.1", "10.0.1.0/24"},
		Deny:       []string{"deny.example.com"},
		AllowPorts: []int{80, 443},
	}
	cases := []struct {
		name    string
		raw     string
		allowed bool
		reason  string
	}{
		{name: "exact host", raw: "example.com", allowed: true},
		{name: "suffix subdomain", raw: "api.example.com", allowed: true},
		{name: "wildcard subdomain", raw: "a.wild.test", allowed: true},
		{name: "wildcard excludes apex", raw: "wild.test", allowed: false, reason: "not an authorized scope target"},
		{name: "ipv4 literal", raw: "10.0.0.1", allowed: true},
		{name: "ipv4 outside literal", raw: "10.0.0.2", allowed: false, reason: "not an authorized scope target"},
		{name: "cidr member", raw: "10.0.1.55", allowed: true},
		{name: "cidr outside", raw: "10.0.2.55", allowed: false, reason: "not an authorized scope target"},
		{name: "url on allowed port", raw: "https://app.example.com:443/x", allowed: true},
		{name: "url default port", raw: "https://example.com/x", allowed: true},
		{name: "url disallowed port", raw: "http://app.example.com:8080/", allowed: false, reason: "allow_ports"},
		{name: "deny overrides allow", raw: "https://deny.example.com/", allowed: false, reason: "explicitly denied"},
		{name: "out of scope", raw: "https://evil.test/", allowed: false, reason: "not an authorized scope target"},
		{name: "empty reference", raw: "   ", allowed: true},
		{name: "cidr entry literal", raw: "10.0.1.0/24", allowed: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := scope.DestinationAllowed(c.raw)
			if ok != c.allowed {
				t.Fatalf("DestinationAllowed(%q) = %v (%s), want %v", c.raw, ok, reason, c.allowed)
			}
			if c.reason != "" && !strings.Contains(reason, c.reason) {
				t.Fatalf("DestinationAllowed(%q) reason = %q, want substring %q", c.raw, reason, c.reason)
			}
		})
	}
}

func TestEngagementScopeEmptyPortsAllowEverything(t *testing.T) {
	scope := &EngagementScope{Targets: []string{"example.com"}}
	if ok, reason := scope.DestinationAllowed("http://example.com:9999/"); !ok {
		t.Fatalf("empty allow_ports must allow every port, got %s", reason)
	}
}

func TestShouldBlockForOutOfScope_EngagementScope(t *testing.T) {
	a := &Agent{
		localGuard: scopeguard.Config{BindAddr: "127.0.0.1", Port: 9137},
		engagementScope: &EngagementScope{
			Targets:    []string{"example.com", "127.0.0.1"},
			Deny:       []string{"deny.example.com"},
			AllowPorts: []int{443},
		},
	}

	allowed := []struct {
		tool string
		args map[string]string
	}{
		{"http_request", map[string]string{"url": "https://app.example.com/x"}},
		{"send_request", map[string]string{"url": "https://example.com:443/x"}},
		{"terminal_execute", map[string]string{"command": "curl https://example.com/"}},
		{"python_action", map[string]string{"code": "import requests; requests.get('https://app.example.com')"}},
	}
	for _, c := range allowed {
		if blocked, reason := a.shouldBlockForOutOfScope(c.tool, c.args); blocked {
			t.Errorf("%s %v: want allowed, got %s", c.tool, c.args, reason)
		}
	}

	blocked := []struct {
		tool       string
		args       map[string]string
		wantReason string
	}{
		{"http_request", map[string]string{"url": "https://evil.test/"}, "outside the engagement scope"},
		{"http_request", map[string]string{"url": "http://app.example.com:8080/"}, "outside the engagement scope"},
		{"http_request", map[string]string{"url": "https://deny.example.com/"}, "outside the engagement scope"},
		{"terminal_execute", map[string]string{"command": "curl http://evil.test/"}, "outside the engagement scope"},
		{"python_action", map[string]string{"code": "requests.get('http://evil.test')"}, "outside the engagement scope"},
		{"terminal_execute", map[string]string{"command": "curl http://app.example.com:8080/"}, "outside the engagement scope"},
	}
	for _, c := range blocked {
		blockedNow, reason := a.shouldBlockForOutOfScope(c.tool, c.args)
		if !blockedNow {
			t.Errorf("%s %v: want blocked, got allowed", c.tool, c.args)
			continue
		}
		if !strings.Contains(reason, c.wantReason) {
			t.Errorf("%s %v: reason = %q, want substring %q", c.tool, c.args, reason, c.wantReason)
		}
	}

	// The operator-machine guard runs first and keeps its own message even when
	// the scope would allow the literal address.
	if blocked, reason := a.shouldBlockForOutOfScope("http_request", map[string]string{"url": "http://127.0.0.1:9137/api/version"}); !blocked {
		t.Fatal("operator-machine guard must still block the dashboard listener")
	} else if !strings.Contains(reason, "operator's machine") {
		t.Fatalf("operator-machine guard must win, got %q", reason)
	}
	if blocked, reason := a.shouldBlockForOutOfScope("terminal_execute", map[string]string{"command": "curl http://127.0.0.1:9137/"}); !blocked {
		t.Fatal("operator-machine guard must still block a loopback command")
	} else if !strings.Contains(reason, "operator's machine") {
		t.Fatalf("operator-machine guard must win for commands, got %q", reason)
	}
}

func TestDelegatedOptionsPropagateEngagementScope(t *testing.T) {
	root := &EngagementScope{Targets: []string{"example.com"}, AllowPorts: []int{443}}
	child := &Agent{}
	for _, opt := range delegatedAgentOptions(nil, nil, nil, "sub", false, root) {
		opt(child)
	}
	if child.engagementScope == nil {
		t.Fatal("delegated child did not inherit the engagement scope")
	}
	if !scopeEntriesMatch(child.engagementScope.Targets, "app.example.com") {
		t.Fatalf("inherited scope targets = %v", child.engagementScope.Targets)
	}

	plain := &Agent{}
	for _, opt := range delegatedAgentOptions(nil, nil, nil, "sub", false) {
		opt(plain)
	}
	if plain.engagementScope != nil {
		t.Fatal("no scope should not install one")
	}
}
