package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
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

// TestSetEngagementScopePropagatesToChildren pins XALG019 on the agent layer:
// a live scope replacement reaches already-registered delegated workers.
func TestSetEngagementScopePropagatesToChildren(t *testing.T) {
	root := NewAgent(&config.Config{}, "scope-root", make(chan Event, 8), scopeguard.Config{})
	child := NewAgent(&config.Config{}, "scope-child", make(chan Event, 8), scopeguard.Config{})
	t.Cleanup(root.Stop)
	t.Cleanup(child.Stop)
	root.registerChildAgent(child)
	t.Cleanup(func() { root.unregisterChildAgent(child) })

	root.SetEngagementScope([]string{"10.20.30.0/24"}, []string{"10.20.30.9"}, []int{443})
	for name, ag := range map[string]*Agent{"root": root, "child": child} {
		scope := ag.EngagementScope()
		if scope == nil || len(scope.Targets) != 1 || scope.Targets[0] != "10.20.30.0/24" ||
			len(scope.Deny) != 1 || len(scope.AllowPorts) != 1 {
			t.Fatalf("%s scope = %+v", name, scope)
		}
	}

	root.SetEngagementScope(nil, nil, nil)
	if child.EngagementScope() != nil {
		t.Fatal("cleared scope was not propagated to the child")
	}
}

// TestCheckHTTPHopEnforcesEngagementScope pins XALG018's redirect leg: a hop
// outside the enforced project scope is refused.
func TestCheckHTTPHopEnforcesEngagementScope(t *testing.T) {
	a := NewAgent(&config.Config{}, "hop-scope", make(chan Event, 8), scopeguard.Config{})
	t.Cleanup(a.Stop)
	a.SetEngagementScope([]string{"example.com"}, []string{"deny.example.com"}, nil)

	if err := a.checkHTTPHop("https://example.com/api"); err != nil {
		t.Fatalf("in-scope hop refused: %v", err)
	}
	if err := a.checkHTTPHop("https://evil.test/api"); err == nil {
		t.Fatal("out-of-scope redirect hop accepted")
	}
	if err := a.checkHTTPHop("https://deny.example.com/api"); err == nil {
		t.Fatal("denied host accepted")
	}
}

// TestScopeFenceOnlyChecksPlatformInitiatedOperations pins the decided XALG018
// boundary: the fence applies to actions the platform directs at a destination
// (HTTP, commands, explicit browser navigation), not to a target page's own
// third-party subresources (which never arrive as tool destinations).
func TestScopeFenceOnlyChecksPlatformInitiatedOperations(t *testing.T) {
	a := NewAgent(&config.Config{}, "scope-boundary", make(chan Event, 8), scopeguard.Config{})
	t.Cleanup(a.Stop)
	a.SetEngagementScope([]string{"target.example.com"}, nil, nil)

	// Not a platform network operation: a note may mention any URL.
	if blocked, _ := a.shouldBlockForOutOfScope("add_note", map[string]string{
		"key": "cdn", "value": "the page loads https://cdn.thirdparty.example.com/app.js",
	}); blocked {
		t.Fatal("note mentioning a third-party CDN URL was fenced")
	}

	// Platform-directed HTTP and explicit browser navigation are fenced.
	if blocked, _ := a.shouldBlockForOutOfScope("http_request", map[string]string{
		"url": "https://cdn.thirdparty.example.com/api",
	}); !blocked {
		t.Fatal("explicit out-of-scope HTTP request was not fenced")
	}
	if blocked, _ := a.shouldBlockForOutOfScope("browser_action", map[string]string{
		"url": "https://cdn.thirdparty.example.com/page",
	}); !blocked {
		t.Fatal("explicit out-of-scope browser navigation was not fenced")
	}
	if blocked, _ := a.shouldBlockForOutOfScope("browser_action", map[string]string{
		"url": "https://target.example.com/page",
	}); blocked {
		t.Fatal("in-scope browser navigation was fenced")
	}
}

// A CIDR destination is authorized only when the whole range is allowed:
// checking just the base address would widen the scope.
func TestCIDRDestinationRequiresWholeRange(t *testing.T) {
	wide := &EngagementScope{Targets: []string{"10.0.0.0/8"}}
	if ok, why := wide.DestinationAllowed("10.20.30.0/24"); !ok {
		t.Fatalf("range inside the allow entry rejected: %s", why)
	}
	if ok, _ := wide.DestinationAllowed("10.0.0.0/8"); !ok {
		t.Fatal("exact range rejected")
	}
	if ok, _ := wide.DestinationAllowed("11.0.0.0/24"); ok {
		t.Fatal("range outside the allow entry accepted")
	}

	partiallyDenied := &EngagementScope{
		Targets: []string{"10.0.0.0/8"},
		Deny:    []string{"10.1.0.0/16"},
	}
	if ok, _ := partiallyDenied.DestinationAllowed("10.0.0.0/8"); ok {
		t.Fatal("range overlapping a deny entry accepted")
	}
	if ok, _ := partiallyDenied.DestinationAllowed("10.2.0.0/16"); !ok {
		t.Fatal("range clear of the deny entry rejected")
	}

	// A host entry can never authorize a range.
	hostOnly := &EngagementScope{Targets: []string{"10.20.30.4"}}
	if ok, _ := hostOnly.DestinationAllowed("10.20.30.0/24"); ok {
		t.Fatal("host allow entry authorized a range")
	}
}

// Bare IPv6 literals are judged as themselves (no URL parsing involved).
func TestDestinationAllowedIPv6Literal(t *testing.T) {
	scope := &EngagementScope{Targets: []string{"2001:db8::/32"}}
	if ok, why := scope.DestinationAllowed("2001:db8::5"); !ok {
		t.Fatalf("IPv6 literal rejected: %s", why)
	}
	if ok, _ := scope.DestinationAllowed("2001:db9::5"); ok {
		t.Fatal("IPv6 literal outside the entry accepted")
	}
	if ok, _ := scope.DestinationAllowed("[2001:db8::5]:443"); !ok {
		t.Fatal("bracketed IPv6 host:port rejected")
	}
}
