package web

// scope_fence.go — the runtime (HITL) half of the project authorization
// boundary (REDESIGN §5 × D7; charter §3.5 field authority).
//
// Two layers, one boundary:
//
//	dispatch  validateTargetsInScope (handlers_platform.go) decides which
//	          targets a run may NAME when it starts.
//	runtime   fenceViolation (this file) decides what an approval-gated call
//	          may TOUCH while the run is live: command literals, high-risk
//	          HTTP URLs and proxy-attributed subprocess traffic.
//
// The approval gate is a decision surface, not an authority that can widen the
// operator's authorization: a call outside the fence is never offered for a
// decision. It is recorded as a rejected approval row with decided_by="fence"
// (audit preserved) and handed back to the model as a ⛔ denial, so the loop
// redirects instead of retrying.
//
// Posture notes:
//   - The fence is opt-in (scope.fence.enforce). Without it the gate behaves
//     exactly as before — this file changes nothing unless the operator turned
//     the fence on.
//   - A store failure while READING the fence fails open (charter §6.1: a
//     wedged platform store must never park a tool call). A store failure while
//     WRITING the denial row still denies: the out-of-fence verdict does not
//     depend on the store, so the safe direction is to block.
//   - Command destination extraction is a literal heuristic (URL / host:port /
//     IPv4 / dotted hostname, minus common file suffixes). Opaque material
//     (base64 blobs, interpreter one-liners) is not decoded; deny_command_
//     patterns is the operator's hard stop for those shapes.

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// fenceEnabled returns the fence when it binds this project's approvals.
func fenceEnabled(scope platform.Scope) (platform.Fence, bool) {
	if scope.Fence == nil || !scope.Fence.Enforce {
		return platform.Fence{}, false
	}
	return *scope.Fence, true
}

// fenceViolation evaluates one regulated call against the fence. An empty
// string means "inside the fence" (or the fence is off).
func fenceViolation(scope platform.Scope, v approval.Verdict) string {
	f, ok := fenceEnabled(scope)
	if !ok {
		return ""
	}
	if pattern := fenceCommandPatternMatch(f, v.Command); pattern != "" {
		return fmt.Sprintf("fence: command matches the project's deny_command_patterns entry %q", pattern)
	}
	switch v.Kind {
	case approval.KindCommand:
		for _, dest := range commandDestinations(v.Command) {
			if reason := fenceHostPortReason(scope, f, dest); reason != "" {
				return fmt.Sprintf("fence: command destination %s — %s", dest, reason)
			}
		}
	case approval.KindHTTP:
		if !fenceMethodAllowed(f, v.Method) {
			return fmt.Sprintf("fence: HTTP method %s is not in the project's allow_http_methods (%s)",
				strings.ToUpper(strings.TrimSpace(v.Method)), strings.Join(f.AllowHTTPMethods, ", "))
		}
		host, port := targetHostPort(v.URL)
		if host == "" {
			// A URL without a resolvable host cannot be mapped onto the
			// boundary; it stays a normal approval request.
			return ""
		}
		if reason := fenceHostPortReason(scope, f, hostPortString(host, port)); reason != "" {
			return fmt.Sprintf("fence: HTTP request to %s — %s", hostPortString(host, port), reason)
		}
	}
	return ""
}

// fenceTargetReason evaluates one dispatch-style target (scheme/port allowed)
// against the fence. Empty = inside. Used by the project page's 校验 action so
// the UI can preview exactly what the runtime gate will decide.
func fenceTargetReason(scope platform.Scope, target string) string {
	f, ok := fenceEnabled(scope)
	if !ok {
		return ""
	}
	host, port := targetHostPort(target)
	if host == "" {
		return "not a valid host"
	}
	return fenceHostPortReason(scope, f, hostPortString(host, port))
}

// fenceHostPortReason applies the deny-over-allow boundary to one destination.
// port may be empty when the material does not name one (command literals);
// the allow_ports check is then skipped, because the run cannot reach a port
// the dispatch-time validator never approved for the named target.
func fenceHostPortReason(scope platform.Scope, f platform.Fence, dest string) string {
	host, port := targetHostPort(dest)
	if host == "" {
		return "not a valid host"
	}
	if fenceExemptHost(host) {
		// Loopback is the operator's own machine: scopeguard's unconditional
		// block (security chain ring 1) already governs it, and local proxy
		// hops in command lines must not read as fence violations.
		return ""
	}
	if denied := fenceDenyMatch(f, host); denied != "" {
		return fmt.Sprintf("host %q is excluded by the project fence (deny_targets entry %q)", host, denied)
	}
	allowed := false
	for _, entry := range scope.Targets {
		entryHost, _ := targetHostPort(entry)
		if entryHost == "" {
			continue
		}
		if hostInScope(host, entryHost) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Sprintf("host %q is outside the project scope targets", host)
	}
	if port != "" && len(scope.AllowPorts) > 0 {
		n, err := strconv.Atoi(port)
		if err != nil || !portAllowed(scope.AllowPorts, n) {
			return fmt.Sprintf("port %s is not in the project allow_ports", port)
		}
	}
	return ""
}

// fenceDenyMatch returns the matching deny entry, empty when the host is not
// excluded. Entries follow the scope-host grammar: exact host, implicit
// subdomain of a bare entry, or an explicit "*.domain" wildcard.
func fenceDenyMatch(f platform.Fence, host string) string {
	for _, entry := range f.DenyTargets {
		entryHost, _ := targetHostPort(entry)
		if entryHost == "" {
			continue
		}
		if hostInScope(host, entryHost) {
			return entry
		}
	}
	return ""
}

// fenceMethodAllowed: an empty allow list means "no method restriction".
func fenceMethodAllowed(f platform.Fence, method string) bool {
	if len(f.AllowHTTPMethods) == 0 {
		return true
	}
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		m = "GET"
	}
	for _, allowed := range f.AllowHTTPMethods {
		if strings.EqualFold(strings.TrimSpace(allowed), m) {
			return true
		}
	}
	return false
}

// fenceCommandPatternMatch returns the first matching deny pattern.
func fenceCommandPatternMatch(f platform.Fence, command string) string {
	if command == "" || len(f.DenyCommandPatterns) == 0 {
		return ""
	}
	lower := strings.ToLower(command)
	for _, pattern := range f.DenyCommandPatterns {
		p := strings.TrimSpace(pattern)
		if p == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(p)) {
			return p
		}
	}
	return ""
}

// fenceExemptHost reports hosts the fence deliberately does not judge:
// loopback / localhost only. Everything else — including RFC1918 addresses —
// must be a listed scope target.
func fenceExemptHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(host), "[]"), "."))
	if h == "" {
		return false
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || h == "localhost.localdomain" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func hostPortString(host, port string) string {
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

// ── command destination extraction ──────────────────────────────────────────

var (
	// scheme://host… — the whole URL is matched, then url.Parse pulls the host.
	fenceURLPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s"'<>;|&()\\,]+`)
	// host:port — a dotted/quoted label followed by a numeric port.
	fenceHostPortPattern = regexp.MustCompile(`(?i)\b((?:[a-z0-9](?:[a-z0-9.\-]*[a-z0-9])?|\[[0-9a-f:]+\]):[0-9]{1,5})\b`)
	// bare IPv4 literals.
	fenceIPv4Pattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	// dotted hostnames (at least one dot, alphabetic TLD).
	fenceHostPattern = regexp.MustCompile(`(?i)\b((?:[a-z0-9](?:[a-z0-9\-]*[a-z0-9])?\.)+[a-z]{2,24})\b`)
)

// fenceFileSuffixes are dotted tokens that are overwhelmingly file names in a
// shell command ("report.pdf", "notes.md"). Treating them as destinations would
// make the fence refuse ordinary scanning work.
var fenceFileSuffixes = map[string]struct{}{
	"pdf": {}, "txt": {}, "json": {}, "xml": {}, "html": {}, "htm": {},
	"csv": {}, "log": {}, "md": {}, "png": {}, "jpg": {}, "jpeg": {}, "gif": {},
	"svg": {}, "zip": {}, "tar": {}, "gz": {}, "tgz": {}, "bz2": {}, "7z": {},
	"sh": {}, "py": {}, "js": {}, "ts": {}, "tsx": {}, "jsx": {}, "go": {},
	"rb": {}, "php": {}, "pl": {}, "yaml": {}, "yml": {}, "toml": {}, "ini": {},
	"conf": {}, "cfg": {}, "db": {}, "sql": {}, "bin": {}, "out": {}, "tmp": {},
	"lock": {}, "map": {}, "wasm": {}, "jar": {}, "exe": {}, "dll": {}, "so": {},
	"o": {}, "a": {}, "list": {}, "wordlist": {}, "lst": {}, "bak": {},
}

// commandDestinations extracts the literal destinations a shell command names.
// Best effort by design: the regexes cannot see through variable expansion or
// encoded payloads, which is what deny_command_patterns exists for.
func commandDestinations(command string) []string {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	seen := make(map[string]struct{})
	seenHost := make(map[string]struct{})
	out := make([]string, 0, 4)
	add := func(dest string) {
		dest = strings.TrimSpace(dest)
		if dest == "" {
			return
		}
		if _, dup := seen[dest]; dup {
			return
		}
		seen[dest] = struct{}{}
		if host, _ := targetHostPort(dest); host != "" {
			seenHost[host] = struct{}{}
		}
		out = append(out, dest)
	}

	for _, raw := range fenceURLPattern.FindAllString(command, -1) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		add(hostPortString(strings.ToLower(u.Hostname()), u.Port()))
	}
	for _, raw := range fenceHostPortPattern.FindAllString(command, -1) {
		host, port := targetHostPort(raw)
		if host == "" {
			continue
		}
		add(hostPortString(host, port))
	}
	for _, raw := range fenceIPv4Pattern.FindAllString(command, -1) {
		if net.ParseIP(raw) == nil {
			continue
		}
		add(raw)
	}
	for _, raw := range fenceHostPattern.FindAllString(command, -1) {
		host := strings.ToLower(strings.TrimSuffix(raw, "."))
		if strings.Contains(host, "@") || strings.HasPrefix(host, "-") {
			continue
		}
		labels := strings.Split(host, ".")
		if len(labels) < 2 {
			continue
		}
		if _, isFile := fenceFileSuffixes[labels[len(labels)-1]]; isFile {
			continue
		}
		if strings.HasPrefix(host, "--") || strings.Contains(host, "=") {
			continue
		}
		if _, already := seenHost[host]; already {
			// The port-carrying form won; the bare host adds no information.
			continue
		}
		add(host)
	}
	return out
}

// ── gate integration ────────────────────────────────────────────────────────

// fenceReason reads the owning project's fence and evaluates one verdict.
// Empty = inside the fence, fence off, unknown project, or an unreadable store
// (fail-open: reading the fence must never park a tool call).
func (s *ApprovalService) fenceReason(ctx context.Context, store platform.Repository, projectID string, verdict approval.Verdict) string {
	if store == nil || projectID == "" {
		return ""
	}
	project, err := store.GetProject(ctx, projectID)
	if err != nil {
		log.Printf("[approval] fence lookup for project %s failed: %v — allowing the call to proceed to the normal gate", projectID, err)
		return ""
	}
	return fenceViolation(project.Scope, verdict)
}

// fenceDenialMessage renders the ⛔ text a fence-denied call hands back to the
// caller (the model via the tool result, the proxy as a 403 body).
func fenceDenialMessage(row *platform.Approval) string {
	if row == nil {
		return "⛔ FENCE DENIAL — the destination leaves the project's authorized scope, so the call was NOT executed. Work only inside the registered targets."
	}
	return approvalDenialMessage(submitOutcome{Status: row.Status, Row: row})
}

// fenceDenial is the single entry point every gate layer (Eino broker,
// registry gate, traffic-proxy gate) calls BEFORE operator rules are consulted:
// a remembered "approve & remember" rule may only exempt calls that are already
// inside the authorization boundary, never widen it.
//
// The second return value reports whether the fence refused the call. A nil row
// with denied=true means the refusal stands but its audit row could not be
// persisted — the caller must still block.
func (s *ApprovalService) fenceDenial(instanceID string, sctx *scanctx.ScanContext, verdict approval.Verdict, secrets []string) (*platform.Approval, bool) {
	store := s.srv.currentPlatformStore()
	if store == nil {
		return nil, false // fail-open: scanning never depends on the platform
	}
	ctx, cancel := storeCtx()
	defer cancel()
	projectID, taskID, runID := s.resolveOwnership(ctx, store, instanceID)
	reason := s.fenceReason(ctx, store, projectID, verdict)
	if reason == "" {
		return nil, false
	}
	log.Printf("[approval] fence denied a %s call in run %s: %s", verdict.Kind, instanceID, reason)
	if err := s.waitForSlot(instanceID, sctx); err != nil {
		// The scan is stopping; the caller's cancellation path owns the exit.
		return nil, true
	}
	row, err := s.submitFenceDenial(ctx, store, instanceID, projectID, taskID, runID, verdict, reason, secrets)
	if err != nil {
		s.releaseSlot(instanceID)
		log.Printf("[approval] fence denial row for run %s could not be persisted: %v", instanceID, err)
		return nil, true
	}
	return row, true
}

// submitFenceDenial records and immediately closes a fence-denied call: the
// row is the audit record (decided_by="fence"), the closed state is what wakes
// the waiting tool call. The caller must already hold an approval slot; the
// finalize path releases it.
func (s *ApprovalService) submitFenceDenial(ctx context.Context, store platform.Repository, instanceID, projectID, taskID, runID string, verdict approval.Verdict, reason string, secrets []string) (*platform.Approval, error) {
	input := platform.ApprovalInput{
		ProjectID:  projectID,
		TaskID:     taskID,
		RunID:      runID,
		InstanceID: instanceID,
		Kind:       verdict.Kind,
		Payload:    buildApprovalPayload(verdict, secrets),
		DeadlineAt: time.Now().Add(s.policy.Timeout),
	}
	input.Payload.Reason = reason
	row, err := store.CreateApproval(ctx, input)
	if err != nil {
		return nil, err
	}
	s.srv.broadcastToInstance(instanceID, WSEvent{
		Type:     "approval_requested",
		Content:  fmt.Sprintf("Approval required: %s", reason),
		Approval: row,
	})
	s.srv.appendConversationEvent(instanceID, WSEvent{
		Type:     "approval_requested",
		Content:  fmt.Sprintf("Approval required: %s", reason),
		Approval: row,
	})
	final, ferr := s.finalize(row, platform.ApprovalDecision{
		Decision:  platform.ApprovalRejected,
		DecidedBy: "fence",
		Reason:    reason,
	})
	if ferr != nil {
		if adopted, adoptErr := s.adoptConflict(row, ferr); adoptErr == nil {
			return adopted, nil
		}
		return row, ferr
	}
	return final, nil
}
