package approval

// Operator-authored allow rules (REDESIGN §5.2 R2a addendum, API_CONTRACT §3.2):
// the persistent half of the "approve & remember" loop. One operator decision
// becomes a reusable exemption consulted by BOTH gate layers — the command
// gate (einoApprovalBroker) and the wire gate (proxyHighRiskGate) — so the
// same intent is asked at most once. Matching lives here, next to the
// classifier, because the web layer must not re-implement command/URL
// semantics and the proxy already reuses this package verbatim.
//
// Hard boundary (review HIGH-2 carried over): a rule NEVER exempts a deny
// shape (command substitution, redirection, inline env shadowing, inline code
// execution). Those shapes are "what the classifier cannot see"; a rule is a
// category-level exemption the operator granted AFTER the classifier surfaced
// a clean Require verdict. Matching therefore runs every segment through
// rejectSegmentShape first — identical to the base classification.
//
// Trust semantics, stated honestly: a command rule trusts the program's whole
// flag surface (a `curl` rule exempts write flags; an `nmap` rule exempts
// output flags and unsafe --script categories). That is wider than the exact
// command the operator approved and narrower than XALGORIX_APPROVAL_ALLOWLIST
// only by the `contains` constraint — the operator narrows it at creation time
// (the UI prefills the target host). HTTP rules are structural: method + host
// (exact or subdomain suffix) + path prefix (+ optional body/query substring).

import "strings"

// MatchCommandRule reports whether command satisfies an operator command rule
// {program, contains}. program matches the basename of any segment's program;
// every other segment must independently pass the built-in allowance (deny
// shapes refuse the whole command). contains, when non-empty, must appear in
// the command (case-insensitive — target hosts are matched without case).
// The rule's program bypasses the nmap/curl dedicated flag checks: the rule's
// trust is the program's flag surface.
func (p *Policy) MatchCommandRule(command, program, contains string) bool {
	program = strings.ToLower(strings.TrimSpace(program))
	if program == "" || strings.TrimSpace(command) == "" {
		return false
	}
	if !containsFold(strings.TrimSpace(command), contains) {
		return false
	}
	_, ok := p.allowCommandWith(command, program)
	return ok
}

// MatchHTTPRule reports whether (method, url, body) satisfies an operator
// HTTP rule {method, host, pathPrefix, contains}. method compares
// case-insensitively; host matches exactly or as a subdomain suffix
// ("example.com" matches api.example.com); an empty pathPrefix matches any
// path; contains, when non-empty, must appear in the URL or body
// (case-insensitive).
func MatchHTTPRule(method, rawURL, body, ruleMethod, ruleHost, rulePathPrefix, contains string) bool {
	if strings.TrimSpace(ruleMethod) == "" || strings.TrimSpace(ruleHost) == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(method), strings.TrimSpace(ruleMethod)) {
		return false
	}
	// Scheme-less hand-typed rule target ("api.example.com/path"): the shared
	// host/path parsers need a scheme (otherwise url.Parse treats the whole
	// string as a path). Wire URLs always carry one; over-blocking on exotic
	// shapes stays the sanctioned failure mode.
	rawURL = strings.TrimSpace(rawURL)
	if rawURL != "" && !strings.Contains(rawURL, "://") {
		rawURL = "https://" + strings.TrimPrefix(rawURL, "//")
	}
	host := strings.ToLower(splitURLHost(rawURL))
	ruleHost = strings.ToLower(strings.TrimSpace(ruleHost))
	if host != ruleHost && !strings.HasSuffix(host, "."+ruleHost) {
		return false
	}
	path, _ := splitURLPathQuery(rawURL)
	if pfx := strings.Trim(strings.TrimSpace(rulePathPrefix), "/"); pfx != "" && !strings.HasPrefix(strings.Trim(path, "/"), pfx) {
		return false
	}
	if !containsFold(rawURL+" "+body, contains) {
		return false
	}
	return true
}

// containsFold reports whether sub (when non-empty) appears in s,
// case-insensitively; an empty sub always matches.
func containsFold(s, sub string) bool {
	if sub == "" {
		return true
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
