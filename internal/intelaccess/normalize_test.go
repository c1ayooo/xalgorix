package intelaccess

import (
	"strings"
	"testing"
)

func TestNormalizeTarget(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://App.Example.com/admin", "app.example.com:443", true},
		{"http://10.0.0.5:8080/x?y=1", "10.0.0.5:8080", true},
		{"10.0.177.5:22", "10.0.177.5:22", true},
		{"10.0.177.0/24", "10.0.177.0/24", true},
		{"10.0.177.9/24", "10.0.177.0/24", true},
		{"ssh://bastion.test:2222", "bastion.test:2222", true},
		{"app.test:8443/admin/panel", "app.test:8443", true}, // scheme-less host:port/path (intelligence strips the scheme)
		{"app.test/admin/panel", "", false},                  // no port: D2 forbids guessing
		{"  APP.EXAMPLE.COM:8443  ", "app.example.com:8443", true},
		{"app.example.com", "", false},     // bare host: D2 forbids guessing a port
		{"", "", false},                    // empty
		{"10.0.177.5:notaport", "", false}, // port must be numeric
		{"10.0.177.5:0", "", false},        // port range
		{"admin:admin", "", false},         // credential text, not a target
	}
	for _, tc := range cases {
		got, ok := NormalizeTarget(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("NormalizeTarget(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNormalizeTargetIPv6KeepsBrackets(t *testing.T) {
	got, ok := NormalizeTarget("[::1]:8443")
	if !ok {
		t.Fatal("[::1]:8443 must normalize")
	}
	if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]:8443") {
		t.Fatalf("IPv6 target = %q, want bracketed host:port", got)
	}
}
