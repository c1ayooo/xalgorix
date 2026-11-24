// Package approval implements the HITL approval policy (REDESIGN §5 / D5):
// which tool calls become approval obligations, and which pass without one.
//
// Scope decision (D5, charter red-line "审批默认只拦命令执行+高危 HTTP"):
//
//	command — terminal_execute / python_action / port_scan / browser dangerous operations
//	          (execute_js, set_cookie, any op carrying code/data), EXCEPT the
//	          read-only reconnaissance allowlist (passive subfinder/httpx,
//	          `nmap -sV`, curl GET/HEAD, ...).
//	http    — write-method (POST/PUT/PATCH/DELETE) requests that hit a
//	          high-risk feature: admin-panel path, destructive payload, or
//	          auth-mutation endpoint/payload. GET/HEAD/OPTIONS always pass and
//	          stay recorded by the existing tool-call audit trail — the
//	          gate deliberately does NOT create approval rows for them, so the
//	          audit interpretation is: read-only HTTP recon is covered by the
//	          per-call tool_call/tool_result event journal, not by the
//	          approvals table.
//
// The classifiers are pure functions so the R2b traffic proxy can reuse them
// verbatim for out-of-registry request assessment. The verdict reason doubles
// as the approval payload's rule name (API_CONTRACT §3 payload.reason).
package approval

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Kind classifies a tool call for the approval gate. "none" = unregulated.
const (
	KindNone    = "none"
	KindCommand = "command"
	KindHTTP    = "http"
)

// Decision is the gate outcome for one call.
type Decision int

const (
	// Allow executes without an approval row (allowlist / read-only /
	// unregulated). The call stays audited through the normal event stream.
	Allow Decision = iota
	// Require suspends the tool call until the operator decides, the
	// deadline elapses (timeout = rejection), or the run stops.
	Require
)

// Verdict carries the classification result to the gate service. When
// Decision is Require, the kind + extracted material become the approval
// row's payload (after the web layer masks secret headers and truncates the
// body); Reason names the matched rule.
type Verdict struct {
	Decision Decision
	Kind     string // KindNone / KindCommand / KindHTTP
	Reason   string // rule name, empty for Allow

	// kind=command material
	Command string
	Workdir string

	// kind=http material
	Method  string
	URL     string
	Headers map[string]string
	Body    string
}

// Evaluate classifies a tool call against the policy. mode=off short-circuits
// to Allow before any classification (no rows, no events — REDESIGN §5.2 R2).
func (p *Policy) Evaluate(toolName string, args map[string]string) Verdict {
	if p.Mode == ModeOff {
		return Verdict{Decision: Allow, Kind: KindNone}
	}
	kind, material := classifyCall(toolName, args)
	switch kind {
	case KindNone:
		return Verdict{Decision: Allow, Kind: KindNone}
	case KindCommand:
		if reason, ok := p.allowCommand(material.command); ok {
			return Verdict{Decision: Allow, Kind: KindCommand, Reason: reason}
		}
		return Verdict{
			Decision: Require, Kind: KindCommand,
			Reason:  material.reason,
			Command: material.command,
			Workdir: material.workdir,
		}
	default: // KindHTTP
		v := AssessHTTP(material.method, material.url, material.headers, material.body)
		v.Kind = KindHTTP
		if v.Reason == "" && v.Decision == Allow {
			v.Reason = "read-only method (recorded in tool-call audit)"
		}
		return v
	}
}

// callMaterial is the raw material extracted from tool args for one kind.
type callMaterial struct {
	reason  string
	command string
	workdir string
	method  string
	url     string
	headers map[string]string
	body    string
}

// classifyCall maps a tool call onto (kind, material). Unknown tools are
// unregulated: the gate only intercepts what D5 names, never the whole tool
// surface (REDESIGN §10 red-line).
func classifyCall(toolName string, args map[string]string) (string, callMaterial) {
	switch toolName {
	case "terminal_execute":
		return KindCommand, callMaterial{
			reason:  "shell command execution requires approval",
			command: args["command"],
			workdir: args["workdir"],
		}
	case "python_action":
		return KindCommand, callMaterial{
			reason:  "python code execution requires approval",
			command: args["code"],
		}
	case "port_scan":
		// Port/service sweeps are active network probing: command-class so the
		// operator sees the exact target + port set before it leaves the box.
		// The rendered command mirrors the gogo invocation so an
		// "approve & remember" prefix rule covers repeat sweeps of one target.
		ports := strings.TrimSpace(args["ports"])
		if ports == "" {
			ports = "top2"
		}
		return KindCommand, callMaterial{
			reason:  "port/service scan requires approval",
			command: fmt.Sprintf("gogo -i %s -p %s", strings.TrimSpace(args["target"]), ports),
		}
	case "browser_action":
		return classifyBrowser(args)
	case "http_request":
		return KindHTTP, callMaterial{
			method:  firstNonEmpty(args["method"], "GET"),
			url:     args["url"],
			headers: parseHeaderJSON(args["headers"]),
			body:    args["body"],
		}
	case "send_request":
		return KindHTTP, callMaterial{
			method:  firstNonEmpty(args["method"], "GET"),
			url:     args["url"],
			headers: parseHeaderJSON(args["headers"]),
			body:    args["body"],
		}
	default:
		// MCP-bridged tools (mcp_<server>_<tool>) execute on an external
		// operator-registered server: command-class by default. The command
		// field carries the bridged name so a remembered command rule can
		// exempt a specific tool ("approve & remember" composes unchanged).
		// Per-server require_approval=false exemptions are resolved by the
		// broker (which owns the per-scan server snapshot), not here — the
		// policy stays store-free.
		if strings.HasPrefix(toolName, "mcp_") {
			return KindCommand, callMaterial{
				reason:  "MCP tool call requires approval (external server)",
				command: toolName,
			}
		}
		return KindNone, callMaterial{}
	}
}

// classifyBrowser maps browser_action operations (browser.go action list) onto
// the approval kinds: execute_js / set_cookie / any op carrying code or data
// are command-class dangerous operations; navigation ops (goto, launch,
// verify_*, tab ops) are assessed by their URL (GET → pass + audit trail).
// Interaction ops (click/type/submit/snapshot/…) carry no directly dangerous
// payload and stay unregulated.
func classifyBrowser(args map[string]string) (string, callMaterial) {
	op := strings.ToLower(strings.TrimSpace(args["command"]))
	switch {
	case op == "execute_js":
		return KindCommand, callMaterial{
			reason:  "browser arbitrary JS execution requires approval",
			command: "browser_action execute_js " + args["code"],
		}
	case op == "set_cookie":
		return KindCommand, callMaterial{
			reason:  "browser cookie manipulation requires approval",
			command: "browser_action set_cookie name=" + args["name"] + " domain=" + args["domain"] + " value=" + args["text"],
		}
	case args["code"] != "":
		return KindCommand, callMaterial{
			reason:  "browser operation with embedded code requires approval",
			command: "browser_action " + op + " code=" + args["code"],
		}
	case args["data"] != "":
		// verify_xss POST form submission — a real write against the target.
		return KindCommand, callMaterial{
			reason:  "browser form submission with payload data requires approval",
			command: "browser_action " + op + " url=" + args["url"] + " data=" + args["data"],
		}
	case op == "goto" || op == "launch" || op == "verify_xss" || op == "verify_path_template_xss" || op == "new_tab":
		return KindHTTP, callMaterial{
			method: "GET",
			url:    args["url"],
		}
	default:
		return KindNone, callMaterial{}
	}
}

// parseHeaderJSON decodes the tools' JSON-object header argument. Values may
// be strings, numbers, or arrays of strings (httpclient parameter doc);
// everything is flattened to its string form for assessment and display.
func parseHeaderJSON(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	out := make(map[string]string, len(parsed))
	for k, v := range parsed {
		out[strings.ToLower(k)] = headerValueString(v)
	}
	return out
}

func headerValueString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, headerValueString(item))
		}
		return strings.Join(parts, ", ")
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ── HTTP assessment (shared with the R2b traffic proxy) ─────────────────────

// adminPathSegments names management-plane path segments (case-insensitive
// match against URL path segments and host labels).
var adminPathSegments = map[string]struct{}{
	"admin":         {},
	"administrator": {},
	"console":       {},
	"manager":       {},
	"phpmyadmin":    {},
	"wp-admin":      {},
	"wp-login":      {},
	"cpanel":        {},
	"dashboard":     {},
	"actuator":      {},
	"druid":         {},
	"nacos":         {},
	"jenkins":       {},
}

// destructivePayloadFeatures are destructive operation markers assessed in the
// write body and URL query (case-insensitive substring). SQL DROP of a whole
// database/schema, DDL (ALTER TABLE), Mongo db.dropDatabase() and Redis
// FLUSHALL/FLUSHDB sit beside the original table truncate/delete shapes — a
// pentest payload that destroys data lands the request on the operator queue.
var destructivePayloadFeatures = []string{
	"drop table", "drop database", "dropdatabase", "drop schema",
	"alter table", "delete from", "truncate table", "xp_cmdshell",
	"flushall", "flushdb",
	"rm -rf", "rm -fr", "mkfs", "shutdown", "reboot", "poweroff",
	":(){ :|:& };:",
}

// authMutationFeatures mark authentication/identity mutation endpoints and
// payloads: on a write method, either the URL path/query or a JSON/form field
// name in the body carrying one of these keywords routes the request to
// approval (password/token/session/cookie handling, REDESIGN §5.1).
var authMutationFeatures = []string{
	"password", "passwd", "token", "session", "cookie",
	"apikey", "api_key", "api-key", "secret", "credential", "auth",
	"login", "signin", "sign-in", "signout", "signup", "register",
	"logout", "2fa", "mfa", "otp",
}

// authFieldValueRe lives in policy.go (single compiled copy): JSON/form-style
// assignments of auth-bearing field names in a body ("password=x",
// '"session": "abc"', "token =") require approval on write methods;
// value-only mentions ("query=password policy") do NOT match, so a search
// string that happens to contain the word stays on the audit trail instead
// of the approval queue.

// AssessHTTP decides whether an HTTP request needs operator approval. Ordinary
// reads pass (recorded in the tool-call audit, never as approval rows), but a
// read whose query carries a destructive primitive is still a destructive
// operation: risk is not decided by the method alone (XALG013). Write methods
// pass unless they hit a high-risk feature. Rule names double as the approval
// payload reason.
func AssessHTTP(method, rawURL string, headers map[string]string, body string) Verdict {
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		m = "GET"
	}
	path, query := splitURLPathQuery(rawURL)
	host := splitURLHost(rawURL)
	lowerBody := strings.ToLower(body)
	lowerQuery := strings.ToLower(query)
	// The same operation must get the same verdict regardless of serialization
	// (percent-encoded query, form '+' encoding, JSON escapes). Normalize a
	// bounded copy for matching only; the request bytes are never changed
	// (XALG014).
	normQuery := normalizeForMatching(query)
	normBody := normalizeForMatching(body)

	switch m {
	case "GET", "HEAD", "OPTIONS":
		v := Verdict{Decision: Allow, Kind: KindHTTP, Method: m, URL: rawURL}
		readHaystack := lowerQuery + "\n" + normQuery
		for _, feature := range destructivePayloadFeatures {
			if strings.Contains(readHaystack, feature) {
				v.Decision = Require
				v.Reason = "destructive payload feature: " + feature
				return v
			}
		}
		return v
	}
	v := Verdict{Decision: Allow, Kind: KindHTTP, Method: m, URL: rawURL, Headers: headers, Body: body}

	payloadHaystack := lowerBody + "\n" + normBody + "\n" + lowerQuery + "\n" + normQuery

	// 1. Destructive payload — highest risk first.
	for _, feature := range destructivePayloadFeatures {
		if strings.Contains(payloadHaystack, feature) {
			v.Decision = Require
			v.Reason = "destructive payload feature: " + feature
			return v
		}
	}
	// 2. Admin-plane endpoint.
	if hitAdminPath(path, host) {
		v.Decision = Require
		v.Reason = "admin panel path: /" + path
		return v
	}
	// 3. Auth-mutation endpoint or payload.
	if containsAny(strings.ToLower(path)+" "+lowerQuery+" "+normQuery, authMutationFeatures) ||
		matchAuthField(normBody) {
		v.Decision = Require
		v.Reason = "auth mutation endpoint or payload"
		return v
	}
	return v
}

// normalizeForMatching returns a bounded, lower-cased copy of a query/body with
// one layer of percent/form decoding and JSON string unescaping. It is a
// matching aid only: the sent request keeps its original bytes. Invalid or
// oversized input falls back to the raw text rather than failing open.
func normalizeForMatching(s string) string {
	const maxNormalizeBytes = 256 << 10
	if s == "" {
		return ""
	}
	if len(s) > maxNormalizeBytes {
		s = s[:maxNormalizeBytes]
	}
	out := s
	if decoded, err := url.QueryUnescape(s); err == nil {
		out = decoded
	}
	trimmed := strings.TrimSpace(out)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var v any
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&v); err == nil {
			if b, err := json.Marshal(v); err == nil {
				out = string(b)
			}
		}
	}
	return strings.ToLower(out)
}

func splitURLPathQuery(rawURL string) (string, string) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return "", ""
	}
	if u, err := url.Parse(s); err == nil && u.Path != "" {
		return strings.Trim(u.Path, "/"), u.RawQuery
	}
	// Scheme-less variants ("example.com/admin?x=1") still assess on the
	// raw string's path/query portion.
	if idx := strings.IndexAny(s, "?"); idx >= 0 {
		return strings.Trim(s[:idx], "/"), s[idx+1:]
	}
	return strings.Trim(s, "/"), ""
}

func splitURLHost(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil {
		return strings.ToLower(u.Hostname())
	}
	return ""
}

// hitAdminPath checks URL path segments and host labels against the
// management-plane vocabulary ("admin.example.com", "/console/login", ...).
func hitAdminPath(path, host string) bool {
	if path != "" {
		for _, seg := range strings.Split(strings.ToLower(path), "/") {
			if _, ok := adminPathSegments[seg]; ok {
				return true
			}
		}
	}
	if host != "" {
		for _, label := range strings.Split(host, ".") {
			if _, ok := adminPathSegments[label]; ok {
				return true
			}
		}
	}
	return false
}

func containsAny(s string, features []string) bool {
	for _, f := range features {
		if strings.Contains(s, f) {
			return true
		}
	}
	return false
}

func matchAuthField(lowerBody string) bool {
	if lowerBody == "" {
		return false
	}
	return authFieldRe.MatchString(lowerBody)
}
