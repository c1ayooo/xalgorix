package agent

// submit_guard.go — platform submission discipline, enforced in code
// (BENCHMARK_ROUND5_ANALYSIS §7 P0-2 / P0-3).
//
// Round 5 sent 515 submit calls and 449 of them were not flags: probe strings
// (`flag{probe}`, `flag{r6c_probe}`), invented challenge codes (a-19..a-25,
// b-04..b-06, "web", "container") and shell loops that hammered the endpoint
// (195 submits from one intent in 9 minutes). The rule existed only in the
// planner prompt, so it was never enforced. This guard enforces it at the tool
// gate:
//
//  1. provenance — a submitted value must appear in output this run actually
//     observed (charter §3.2 observed/inferred, §4.4 no recollection bypass);
//  2. catalog — when the platform listed its challenge codes, a submit to an
//     unlisted code is refused;
//  3. probe/dummy values are refused outright;
//  4. a completed challenge is not submitted again, and per-scan rate limits
//     stop loops;
//  5. lifecycle calls (start/close/submit) belong to the control-plane lane:
//     an execution-lane worker cannot make them (P0-3 lane closure).
//
// Scope: this guard only touches calls that target a CONFIGURED control-plane
// host (XALGORIX_CONTROL_PLANE_HOSTS). A target application's own /challenges
// route is ordinary scan traffic and is left alone, so normal pentest runs and
// legacy CLI agents are unaffected.

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// platformCallKind classifies one platform-API tool call.
type platformCallKind string

const (
	platformCallRead   platformCallKind = "read"
	platformCallStart  platformCallKind = "start"
	platformCallClose  platformCallKind = "close"
	platformCallSubmit platformCallKind = "submit"
)

// platformCall is the parsed shape of a platform-API call.
//
// Codes and Flags are kept apart on purpose: a challenge code must be checked
// against the platform's catalog, while a submitted flag value must be checked
// against this run's observed output. Mixing them made every literal flag fail
// the catalog test ("flag{...} is not in this round's challenge catalog").
type platformCall struct {
	Kind  platformCallKind
	Codes []string
	Flags []string
	Loop  bool
	Host  string
}

var (
	// submitFlagRE matches a flag-shaped value, including the UUID flags the
	// platform issues.
	submitFlagRE = regexp.MustCompile(`flag\{[^}\s]{1,200}\}`)
	// submitUUIDRE matches a bare UUID flag.
	submitUUIDRE = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	// submitCodeRE matches an explicit unique_code / challenge_id argument in
	// JSON, query-string or shell form.
	submitCodeRE = regexp.MustCompile(`(?i)["']?(?:unique_code|challenge_id|challenge)["']?\s*[=:]\s*["']?([A-Za-z0-9][A-Za-z0-9._:-]{1,79})`)
	// submitLoopRE matches a shell/python loop construct. Round 5's 195 submits
	// came from exactly this shape.
	submitLoopRE = regexp.MustCompile(`(?i)\b(?:for|while|until|xargs)\b|\$\(\s*seq|\{\d+\.\.\d+\}`)
	// submitProbeRE matches placeholder values that must never be submitted.
	// `_`/`-` count as separators so `dummy_flag` and `flag{r6c_probe}` match
	// while an ordinary word inside a longer token does not.
	submitProbeRE = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:probe|dummy|placeholder|contract_probe|reconcile_probe|test_?flag|fake|example)(?:$|[^a-z0-9])`)
	// submitValueRE matches the flag field's value in a JSON body or a query /
	// form string, including non-flag-shaped values (so `dummy_flag` can be
	// rejected as a probe instead of being silently ignored).
	submitValueRE = regexp.MustCompile(`(?i)"flag"\s*:\s*"([^"]{1,200})"`)
	submitQueryRE = regexp.MustCompile(`(?i)[?&]flag=([^&\s]{1,200})`)
	// submitAcceptedRE matches a platform acceptance response.
	submitAcceptedRE = regexp.MustCompile(`(?i)"correct"\s*:\s*true|\bcorrect\b\s*[:=]\s*true|already\s+(?:solved|passed|completed)|flag\s+is\s+correct|提交成功|答案正确`)
)

// submitGuard returns the scan-shared submission ledger, or nil when the agent
// has no scan context (defensive; real agents always have one).
func (a *Agent) submitGuard() *scanctx.SubmitGuard {
	if a == nil || a.scanCtx == nil {
		return nil
	}
	return a.scanCtx.EnsureSubmitGuard()
}

// platformCallFor parses a tool call into a platformCall. It returns false when
// the call does not target a configured control-plane host, which keeps the
// guard off ordinary target traffic.
func (a *Agent) platformCallFor(toolName string, args map[string]string) (platformCall, bool) {
	if a == nil || len(a.controlPlaneHosts) == 0 {
		return platformCall{}, false
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "http_request", "send_request":
		raw := strings.TrimSpace(args["url"])
		if raw == "" {
			raw = strings.TrimSpace(args["_raw"])
		}
		host := platformURLHost(raw)
		if !agentControlPlaneHost(host, a.controlPlaneHosts) {
			return platformCall{}, false
		}
		text := raw + " " + args["body"] + " " + args["data"] + " " + args["json"]
		call, ok := parsePlatformCall(text)
		call.Host = host
		return call, ok
	case "terminal_execute", "python_action":
		cmd := strings.TrimSpace(args["command"])
		if cmd == "" {
			cmd = strings.TrimSpace(args["code"])
		}
		if cmd == "" || !strings.Contains(strings.ToLower(cmd), "/challenges") {
			return platformCall{}, false
		}
		host, ok := controlPlaneHostInText(cmd, a.controlPlaneHosts)
		if !ok {
			// A command that names /challenges without naming the platform host
			// may still reach it through a variable. Treat it as a platform call
			// only when it also carries a platform-shaped path.
			if !strings.Contains(strings.ToLower(cmd), "openapi/") {
				return platformCall{}, false
			}
		}
		call, parsed := parsePlatformCall(cmd)
		call.Host = host
		return call, parsed
	default:
		return platformCall{}, false
	}
}

// parsePlatformCall classifies the text and extracts candidate codes.
func parsePlatformCall(text string) (platformCall, bool) {
	lower := strings.ToLower(text)
	idx := strings.Index(lower, "/challenges")
	if idx < 0 {
		return platformCall{}, false
	}
	tail := lower[idx:]
	call := platformCall{Kind: platformCallRead}
	switch {
	case strings.Contains(tail, "/submit"):
		call.Kind = platformCallSubmit
	case strings.Contains(tail, "/start"):
		call.Kind = platformCallStart
	case strings.Contains(tail, "/close"):
		call.Kind = platformCallClose
	}
	call.Codes, call.Flags = platformCallValues(text)
	call.Loop = submitLoopRE.MatchString(text) ||
		strings.Count(lower, "/challenges/submit") > 1
	return call, true
}

// platformCallValues extracts challenge codes and submitted flag values.
// Codes are unique_code/challenge_id arguments; flags are flag-shaped strings
// and bare UUIDs. A code argument whose value is itself flag-shaped is treated
// as a flag, never as a code (the platform never names a challenge `flag{...}`).
func platformCallValues(text string) (codes, flags []string) {
	seen := make(map[string]bool)
	add := func(dst *[]string, v string) {
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		*dst = append(*dst, v)
	}
	for _, m := range submitFlagRE.FindAllString(text, -1) {
		add(&flags, m)
	}
	for _, m := range submitUUIDRE.FindAllString(text, -1) {
		add(&flags, m)
	}
	for _, m := range submitCodeRE.FindAllStringSubmatch(text, -1) {
		if submitFlagRE.MatchString(m[1]) {
			add(&flags, m[1])
			continue
		}
		add(&codes, m[1])
	}
	for _, m := range submitValueRE.FindAllStringSubmatch(text, -1) {
		add(&flags, m[1])
	}
	for _, m := range submitQueryRE.FindAllStringSubmatch(text, -1) {
		add(&flags, m[1])
	}
	return codes, flags
}

// platformURLHost extracts the hostname from a URL argument. A scheme-less
// value is parsed as https:// (the shape http_request sends).
func platformURLHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// controlPlaneHostInText reports whether text names one of the configured
// control-plane hosts.
func controlPlaneHostInText(text string, controlPlane []string) (string, bool) {
	lower := strings.ToLower(text)
	for _, raw := range controlPlane {
		cp := normalizeAgentHost(raw)
		if cp == "" {
			continue
		}
		if strings.Contains(lower, cp) {
			return cp, true
		}
	}
	return "", false
}

// shouldBlockForPlatformDiscipline is the tool-gate entry point.
func (a *Agent) shouldBlockForPlatformDiscipline(toolName string, args map[string]string) (bool, string) {
	call, ok := a.platformCallFor(toolName, args)
	if !ok {
		return false, ""
	}
	// P0-3: the platform API is the control-plane lane's business. An
	// execution/analysis worker that reads, starts, closes or submits challenges
	// is exactly how round 5's API-lane terminal loops happened.
	if a.toolSurface != SurfaceOrchestrator && a.toolSurface != SurfaceLegacyRoot {
		return true, fmt.Sprintf(
			"platform %s calls belong to the control-plane lane (target_kind=control_plane_api). "+
				"This agent runs the %q surface: do not call the platform API from a container or analysis lane. "+
				"Record what you found in notes/evidence; the control-plane lane owns the platform API.",
			call.Kind, a.toolSurface.String(),
		)
	}
	if call.Kind == platformCallRead {
		return false, ""
	}
	if call.Loop {
		return true, "looped platform call refused: one platform call per tool invocation, no shell loops, no repeated submits. " +
			"Batch submission loops are the largest source of false submissions and wasted tokens."
	}
	if call.Kind != platformCallSubmit {
		return false, ""
	}
	if len(call.Codes) == 0 {
		if len(call.Flags) == 0 {
			return true, "submit without a parseable unique_code: name the challenge's unique_code (or its flag) exactly as the platform printed it."
		}
		return true, "submit names a flag but no unique_code: include the challenge's unique_code exactly as the platform printed it."
	}
	guard := a.submitGuard()
	if guard == nil {
		return false, ""
	}
	all := make([]string, 0, len(call.Codes)+len(call.Flags))
	all = append(all, call.Codes...)
	all = append(all, call.Flags...)
	for _, value := range all {
		if submitProbeRE.MatchString(value) {
			return true, fmt.Sprintf(
				"%q is a probe/placeholder value, not a flag the target produced. Never submit probe, dummy or example values; "+
					"they return an error and burn the submit budget.", value,
			)
		}
	}
	// A challenge code is validated against the platform's own listing. When
	// no listing was observed there is nothing to compare against, so the code
	// is allowed and the platform's 404 decides: refusing legitimate codes
	// whenever the listing fetch failed costs more than it protects.
	for _, code := range call.Codes {
		if guard.CatalogSize() > 0 && !guard.CatalogContains(code) {
			return true, fmt.Sprintf(
				"%q is not in this round's challenge catalog (%d codes observed from /challenges). "+
					"Submit only codes the platform listed; invented ids such as bare words or out-of-range suffixes are refused.",
				code, guard.CatalogSize(),
			)
		}
	}
	// A flag value must have been printed by a tool in this run. The catalog is
	// about challenge codes, never about flag strings.
	for _, flag := range call.Flags {
		if !guard.Observed(flag) {
			return true, fmt.Sprintf(
				"%q was not observed anywhere in this run's tool output. Submitting a value recalled from notes, memory or a previous run "+
					"is a recollection bypass: only submit a flag this run actually read from the target.",
				flag,
			)
		}
	}
	for _, code := range call.Codes {
		if verdict := guard.Reserve(code, time.Now()); !verdict.Allowed {
			return true, verdict.Reason
		}
	}
	return false, ""
}

// observePlatformResult records a finished tool output for provenance and, when
// the call was a submit, marks the accepted challenge complete.
func (a *Agent) observePlatformResult(toolName string, args map[string]string, output string) {
	guard := a.submitGuard()
	if guard == nil {
		return
	}
	if strings.TrimSpace(output) == "" {
		return
	}
	guard.Observe(output)
	call, ok := a.platformCallFor(toolName, args)
	if !ok || call.Kind != platformCallSubmit {
		return
	}
	if !submitAcceptedRE.MatchString(output) {
		return
	}
	for _, code := range call.Codes {
		guard.MarkCompleted(code)
	}
}
