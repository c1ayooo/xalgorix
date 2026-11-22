package approval

import "testing"

// ── operator allow rules (approve & remember, API_CONTRACT §3.2) ─────────────

// TestMatchCommandRule pins the rule semantics: the rule program bypasses its
// flag checks, every other segment keeps the base semantics, and the deny
// shapes stay non-exemptable (a rule must never allow what the classifier
// cannot see).
func TestMatchCommandRule(t *testing.T) {
	p := &Policy{Mode: ModeEnforce}
	cases := []struct {
		name     string
		command  string
		program  string
		contains string
		want     bool
	}{
		// Positive: the exact fatigue scenario — curl write flags exempted by
		// the rule the operator created from the first approval.
		{"curl write flags exempted", `curl -d '{"user":"a"}' https://api.target-x.com/login`, "curl", "", true},
		{"contains narrows match", `curl -d x https://api.target-x.com/login`, "curl", "target-x.com", true},
		{"contains case-insensitive", `curl -d x https://API.Target-X.com/login`, "curl", "target-x.com", true},
		{"path-prefixed program passes", "/usr/bin/curl -d x http://a", "curl", "", true},
		{"pipeline with text filter passes", `curl -d x http://a | jq .`, "curl", "", true},
		{"nmap rule exempts output flags", "nmap -oN /tmp/x -sV 10.0.0.1", "nmap", "", true},

		// Negative: deny shapes are NEVER exemptable — the rule program
		// matching is irrelevant when the segment carries an unseen shape.
		{"command substitution refused", `curl $(echo -d) http://a`, "curl", "", false},
		{"process substitution refused", `curl <(gen) http://a`, "curl", "", false},
		{"redirection refused", `curl -d x http://a > /tmp/out`, "curl", "", false},
		{"PATH shadowing refused", `PATH=/tmp/evil curl -d x http://a`, "curl", "", false},
		{"inline code execution refused", `curl -d "x=$(system(...))" http://a`, "curl", "", false},

		// Negative: the rule does not leak beyond its program.
		{"second dangerous segment refused", `curl -d x http://a && rm -rf /tmp`, "curl", "", false},
		{"unknown second program refused", `curl -d x http://a | sqlite3 db`, "curl", "", false},
		{"different program no match", `wget -O /tmp/f http://a`, "curl", "", false},
		{"contains mismatch", `curl -d x https://other.example.com`, "curl", "target-x.com", false},
		{"empty program never matches", `curl -d x http://a`, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.MatchCommandRule(tc.command, tc.program, tc.contains); got != tc.want {
				t.Errorf("MatchCommandRule(%q, %q, %q) = %v, want %v", tc.command, tc.program, tc.contains, got, tc.want)
			}
		})
	}
}

// TestMatchCommandRuleDoesNotWidenBase pins the boundary the other way: a
// rule never turns a command into something the BASE classifier would have
// allowed differently — allowCommand is untouched by ruleProgram="".
func TestMatchCommandRuleDoesNotWidenBase(t *testing.T) {
	p := &Policy{Mode: ModeEnforce}
	if _, ok := p.allowCommand(`curl -d x http://a`); ok {
		t.Fatal("base allowCommand must still refuse curl write flags without a rule")
	}
	if _, ok := p.allowCommand(`curl -d x http://a && rm -rf /`); ok {
		t.Fatal("base allowCommand must refuse the compound command")
	}
}

// TestMatchHTTPRule pins the HTTP rule semantics: method equality
// (case-insensitive), host exact or subdomain suffix, path prefix, optional
// contains over URL+body.
func TestMatchHTTPRule(t *testing.T) {
	cases := []struct {
		name                       string
		method, url, body          string
		rMethod, rHost, rPath, rCt string
		want                       bool
	}{
		{"exact match", "POST", "https://api.target-x.com/api/login", "password=x", "post", "api.target-x.com", "api/login", "", true},
		{"method case-insensitive", "post", "https://api.target-x.com/api/login", "", "POST", "api.target-x.com", "", "", true},
		{"subdomain suffix", "POST", "https://www.api.target-x.com/api/x", "", "POST", "target-x.com", "", "", true},
		{"host must not char-prefix", "POST", "https://not-target-x.com/api/x", "", "POST", "target-x.com", "", "", false},
		{"empty path prefix matches any", "PUT", "https://a.target-x.com/anything/else", "", "put", "target-x.com", "", "", true},
		{"path prefix", "POST", "https://a.target-x.com/api/login/reset", "", "post", "target-x.com", "api/login", "", true},
		{"path prefix mismatch", "POST", "https://a.target-x.com/other/x", "", "post", "target-x.com", "api/login", "", false},
		{"method mismatch", "DELETE", "https://a.target-x.com/api/x", "", "post", "target-x.com", "", "", false},
		{"host mismatch", "POST", "https://elsewhere.com/api/x", "", "post", "target-x.com", "", "", false},
		{"contains over body", "POST", "https://a.target-x.com/api/s", `{"q":"drop table"}`, "post", "target-x.com", "", "drop table", true},
		{"contains mismatch", "POST", "https://a.target-x.com/api/s", "benign", "post", "target-x.com", "", "drop table", false},
		{"missing host refused", "POST", "https://a.com/x", "", "post", "", "", "", false},
		{"missing method refused", "POST", "https://a.com/x", "", "", "a.com", "", "", false},
		{"scheme-less url host fallback", "POST", "api.target-x.com/api/login", "", "post", "target-x.com", "api/login", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchHTTPRule(tc.method, tc.url, tc.body, tc.rMethod, tc.rHost, tc.rPath, tc.rCt)
			if got != tc.want {
				t.Errorf("MatchHTTPRule(%q,%q,%q | %q,%q,%q,%q) = %v, want %v",
					tc.method, tc.url, tc.body, tc.rMethod, tc.rHost, tc.rPath, tc.rCt, got, tc.want)
			}
		})
	}
}

// TestMatchCommandRuleDoesNotAffectEnvAllowlist pins that a curl rule does
// not quietly change what the env allowlist does for other commands.
func TestMatchCommandRuleRespectsEnvAllowlistSegments(t *testing.T) {
	p := &Policy{Mode: ModeEnforce, Allowlist: []string{"sqlite3"}}
	// sqlite3 (env-allowed) in a pipeline with a curl write: the curl rule
	// must accept the pipeline because the base classifier already accepted
	// the sqlite3 segment.
	if !p.MatchCommandRule(`sqlite3 db ".dump" | curl -d x http://a`, "curl", "") {
		t.Error("rule match must honor env-allowlisted sibling segments")
	}
}
