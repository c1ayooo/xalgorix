// Package discovery owns the progressive-recon policy: WHEN active discovery
// (wordlist content discovery, port/service enumeration) is allowed, as opposed
// to mere evidence-led information gathering.
//
// It is deliberately a leaf package: the agent's engine plan, the recon
// predicate, the finish gate and the R7 planner all import the same functions,
// so one decision cannot drift between execution paths. Nothing here mutates
// state or talks to a target.
//
// R1 progressive-recon rules:
//   - Active discovery is never owed merely because the 22-phase methodology
//     includes it. It is owed for an explicit operator request or a loud
//     posture, and otherwise only PROPOSED by the model — subject to the same
//     gate.
//   - A concrete API surface (spec present) or a mature endpoint inventory means
//     "analyze what is known", not "enumerate generic paths again".
//   - Port discovery additionally requires an explicit host/IP authorization and
//     an independent request budget; a Web URL never implies an all-ports grant.
//   - Quiet posture declines active discovery entirely unless the operator asks.
package discovery

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Postures mirror config.NormalizeNoiseProfile without importing config (leaf
// package). Unknown values fall back to balanced.
const (
	PostureQuiet    = "quiet"
	PostureBalanced = "balanced"
	PostureLoud     = "loud"
)

func normalizePosture(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case PostureQuiet, "low", "stealth", "passive":
		return PostureQuiet
	case PostureLoud, "high", "aggressive":
		return PostureLoud
	default:
		return PostureBalanced
	}
}

// Decision is one policy answer plus the reason to show in a prompt/audit log.
type Decision struct {
	Owed   bool
	Reason string
}

// Input is the immutable evidence the policy reads. Everything is derived from
// the run's own state; the policy never probes.
type Input struct {
	Posture string

	// OperatorRequested means the operator's instruction explicitly asked for
	// content discovery (not merely a full methodology selection).
	OperatorRequested bool
	// OperatorRequestedPort means the instruction explicitly asked for
	// port/service enumeration.
	OperatorRequestedPort bool

	// Deep is the engagement's depth mode (deep enables deeper verification,
	// not a blanket port sweep).
	Deep bool

	// AuthorizedHostTarget: at least one configured target is a concrete host/IP
	// or CIDR (not only a Web URL). A URL target does not authorize port sweeps.
	AuthorizedHostTarget bool
	// RequestBudget: a per-scan request-rate policy is actually configured.
	RequestBudget bool

	// InventoryRoutes is the number of concrete routes the run already knows.
	InventoryRoutes int
	// JSAnalyzed: first-party JS/crawl already produced a concrete inventory.
	JSAnalyzed bool
	// APISpec: an OpenAPI/Swagger/GraphQL description or api-docs artifact was
	// observed, so the API surface should be analyzed rather than enumerated.
	APISpec bool
}

// matureInventory reports whether evidence-led discovery already produced a
// concrete surface. Eight routes is deliberately conservative: a handful of
// crawler links is not an inventory, a mapped application is.
const matureInventoryRoutes = 8

func matureInventory(in Input) bool {
	if in.APISpec {
		return true
	}
	if in.InventoryRoutes >= matureInventoryRoutes {
		return true
	}
	return in.InventoryRoutes >= 3 && in.JSAnalyzed
}

// ContentDiscoveryOwed answers whether the ENGINE may create/require a wordlist
// content-discovery obligation. Under the R1 default (balanced) the engine does
// not: the model may propose discovery, and ProposalAllowed decides that.
func ContentDiscoveryOwed(in Input) Decision {
	if in.OperatorRequested {
		return Decision{Owed: true, Reason: "operator explicitly requested content discovery"}
	}
	switch normalizePosture(in.Posture) {
	case PostureLoud:
		return Decision{Owed: true, Reason: "loud posture accepts bounded content discovery"}
	default:
		// quiet and balanced: evidence-led default, no automatic wordlist.
		return Decision{Owed: false, Reason: "evidence-led default: no automatic wordlist pass (propose it via an intent)"}
	}
}

// ContentDiscoveryProposalAllowed answers whether a MODEL-PROPOSED content
// discovery intent may be admitted. This is the gate R1 item 5 requires: the
// LLM may propose, but authorization and inventory state decide.
func ContentDiscoveryProposalAllowed(in Input) Decision {
	if in.OperatorRequested {
		return Decision{Owed: true, Reason: "operator explicitly requested content discovery"}
	}
	switch normalizePosture(in.Posture) {
	case PostureQuiet:
		return Decision{Owed: false, Reason: "quiet posture declines active content discovery"}
	case PostureLoud:
		return Decision{Owed: true, Reason: "loud posture accepts bounded content discovery"}
	}
	if in.APISpec {
		return Decision{Owed: false, Reason: "API description is present: analyze the known interface instead of enumerating paths"}
	}
	if matureInventory(in) {
		return Decision{Owed: false, Reason: "endpoint inventory is already concrete: go deep on observed routes instead"}
	}
	return Decision{Owed: true, Reason: "inventory is thin: one bounded gap-driven pass is admissible"}
}

// PortDiscoveryOwed answers whether the engine may REQUIRE service/port
// enumeration. It is true only for an operator request that also satisfies the
// authorization and budget conditions.
func PortDiscoveryOwed(in Input) Decision {
	if d := PortProposalAllowed(in); !d.Owed {
		return Decision{Owed: false, Reason: d.Reason}
	}
	if !in.OperatorRequestedPort {
		return Decision{Owed: false, Reason: "port discovery is model-initiated; the engine does not create it automatically"}
	}
	return Decision{Owed: true, Reason: "operator explicitly requested port discovery with authorization and budget"}
}

// PortProposalAllowed answers whether a model-proposed port/service intent may
// be admitted. Authorization + budget are hard requirements; a Web URL alone
// never authorizes an all-ports sweep. An explicit operator request overrides
// the quiet posture but not the authorization/budget gates.
func PortProposalAllowed(in Input) Decision {
	if normalizePosture(in.Posture) == PostureQuiet && !in.OperatorRequestedPort {
		return Decision{Owed: false, Reason: "quiet posture declines port discovery"}
	}
	if !in.Deep {
		return Decision{Owed: false, Reason: "port discovery is a deep-mode capability"}
	}
	if !in.AuthorizedHostTarget {
		return Decision{Owed: false, Reason: "no explicit host/IP target authorizes port discovery (a Web URL does not)"}
	}
	if !in.RequestBudget {
		return Decision{Owed: false, Reason: "no request budget is configured for this run"}
	}
	return Decision{Owed: true, Reason: "host/IP authorized, deep mode, request budget present"}
}

// ── operator-request detection ──────────────────────────────────────────────

var contentDiscoveryRequest = regexp.MustCompile(`(?i)(目录(扫描|枚举|发现|爆破)|路径(扫描|枚举|爆破|发现)|内容发现|目录爆破|` +
	`dirbust|directory (brute|bruteforce|enumeration|discovery)|content discovery|` +
	`wordlist|ffuf|gobuster|feroxbuster|dirsearch|dirb\b)`)

var portDiscoveryRequest = regexp.MustCompile(`(?i)(全端口|端口扫描|端口发现|服务枚举|` +
	`port[- ]?scan|full[- ]port|all[- ]port|nmap|masscan|naabu|rustscan)`)

// OperatorRequestedContentDiscovery reports whether the operator's instruction
// explicitly asks for content discovery.
func OperatorRequestedContentDiscovery(instruction string) bool {
	return contentDiscoveryRequest.MatchString(instruction)
}

// OperatorRequestedPortDiscovery reports whether the instruction explicitly asks
// for port/service enumeration.
func OperatorRequestedPortDiscovery(instruction string) bool {
	return portDiscoveryRequest.MatchString(instruction)
}

// AuthorizedHostTarget reports whether targets contain at least one concrete
// host/IP or CIDR. A bare URL (http(s)://…) or a bare hostname does NOT
// authorize port discovery: it names an application entry, not a host grant.
func AuthorizedHostTarget(targets []string) bool {
	for _, raw := range targets {
		if targetAuthorizesPorts(raw) {
			return true
		}
	}
	return false
}

func targetAuthorizesPorts(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "://") {
		return false // Web URL: application entry only
	}
	if _, _, err := net.ParseCIDR(raw); err == nil {
		return true
	}
	if net.ParseIP(raw) != nil {
		return true
	}
	// host:port is a concrete service grant; a bare hostname is not.
	if host, port, err := net.SplitHostPort(raw); err == nil && strings.TrimSpace(port) != "" {
		if net.ParseIP(host) != nil {
			return true
		}
		if u, perr := url.Parse("//" + raw); perr == nil && u.Hostname() != "" {
			return true
		}
	}
	return false
}

// LooksLikeContentDiscoveryIntent classifies a proposed intent by its text.
// It is intentionally conservative: an intent must name content-discovery
// vocabulary; a normal endpoint test is not reclassified. Path traversal / LFI
// are vulnerability classes, not content discovery, and are excluded.
func LooksLikeContentDiscoveryIntent(title, vulnClass, notes string) bool {
	text := strings.ToLower(title + " " + vulnClass + " " + notes)
	if strings.Contains(text, "traversal") || strings.Contains(text, "lfi") ||
		strings.Contains(text, "file inclusion") || strings.Contains(text, "路径穿越") {
		return false
	}
	return contentDiscoveryIntentRE.MatchString(text)
}

var contentDiscoveryIntentRE = regexp.MustCompile(`(?i)(\bdirbust\b|\bdirectory\b|\bwordlist\b|\bffuf\b|\bgobuster\b|\bferoxbuster\b|\bdirsearch\b|content discovery|目录|路径枚举|路径爆破|内容发现)`)

var (
	portNounRE = regexp.MustCompile(`(?i)\b(port|ports|service|services)\b`)
	portVerbRE = regexp.MustCompile(`(?i)\b(scan|scans|scanning|scanned|discovery|discover|enumeration|enumerate|fingerprint|fingerprinting|identify|identification)\b`)
	toolPortRE = regexp.MustCompile(`(?i)\b(nmap|masscan|naabu|rustscan)\b`)
	cjkPortRE  = regexp.MustCompile(`全端口|端口扫描|端口发现|服务枚举|端口枚举`)
)

// LooksLikePortDiscoveryIntent classifies a proposed intent by its text.
func LooksLikePortDiscoveryIntent(title, vulnClass, notes string) bool {
	text := strings.ToLower(title + " " + vulnClass + " " + notes)
	if toolPortRE.MatchString(text) || cjkPortRE.MatchString(text) {
		return true
	}
	// English shapes: "full TCP port...", "service/version discovery",
	// "port scan", "port enumeration". Require both a port/service noun and a
	// discovery verb so an endpoint test or a report task is never reclassified.
	return portNounRE.MatchString(text) && portVerbRE.MatchString(text)
}
