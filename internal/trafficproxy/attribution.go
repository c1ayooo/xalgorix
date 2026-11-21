package trafficproxy

// Attribution: scan sessions register a per-run bearer token at session
// start; the proxy resolves it from Proxy-Authorization BEFORE the hop-by-hop
// strip and uses it to (a) attribute observed traffic to the owning Run and
// (b) select the scan's operator-machine dial-guard configuration for direct
// upstream dials.
//
// The token is a fresh 128-bit hex slug minted by the web layer (never the
// session id, which embeds the target name and is not a secret). Unattributed
// traffic (browser, curl, LLM/webhook/dashboard egress) stays system-level
// and never produces an InfoItem.
//
// The registry also carries the per-scan scopeguard configuration so the
// dial guard reproduces exactly the semantics the tool-level GuardDials had
// before the MITM hop — including per-scan provisioned loopback ports.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
)

// Attribution identifies the Run a proxied request belongs to.
type Attribution struct {
	InstanceID string
	ScanCtxID  string
}

type attributionEntry struct {
	token      string
	instanceID string
	scanCtxID  string
	guard      scopeguard.Config
}

var (
	attributionMu sync.RWMutex
	byToken       = map[string]attributionEntry{}
	byScanCtx     = map[string]attributionEntry{}
	dialGuardBase scopeguard.Config

	// dialGuardGuard serializes the package dial guard swaps (SetDialGuard is
	// a test seam; Configure captures the current guard under it).
	dialGuardMu sync.Mutex
	// packageDialGuard reports why a direct upstream dial to address must not
	// happen. It runs on net.Dialer.Control — after DNS resolution, on the
	// exact address being connected to (the tools.GuardDials mechanism) — so
	// a DNS-rebinding answer cannot slip past the operator-machine boundary.
	// Dials to a configured upstream proxy are exempt (the operator's proxy
	// often listens on loopback; the upstream owns the destination's egress
	// rules), mirroring GuardDials' proxy exemption.
	packageDialGuard DialGuardFunc = guardFor
)

// currentDialGuard returns the package dial guard under its swap lock.
func currentDialGuard() DialGuardFunc {
	dialGuardMu.Lock()
	defer dialGuardMu.Unlock()
	return packageDialGuard
}

// RegisterRun signs a scan session into the attribution registry. Called by
// the web layer at scan-session start; UnregisterRun at cleanup.
func RegisterRun(token, instanceID, scanCtxID string, guard scopeguard.Config) {
	token = strings.TrimSpace(token)
	scanCtxID = strings.TrimSpace(scanCtxID)
	if token == "" || scanCtxID == "" {
		return
	}
	entry := attributionEntry{token: token, instanceID: instanceID, scanCtxID: scanCtxID, guard: guard}
	attributionMu.Lock()
	defer attributionMu.Unlock()
	byToken[token] = entry
	byScanCtx[scanCtxID] = entry
}

// UnregisterRun removes a finished session's token. token may be empty to
// unregister only by scan context id.
func UnregisterRun(token, scanCtxID string) {
	attributionMu.Lock()
	defer attributionMu.Unlock()
	if token != "" {
		delete(byToken, token)
	}
	if scanCtxID != "" {
		if existing, ok := byScanCtx[scanCtxID]; ok && (token == "" || existing.scanCtxID == scanCtxID) {
			delete(byScanCtx, scanCtxID)
		}
	}
}

// RunTokenFor returns the run token registered for a scan context ("" when
// none). HTTP tools call this to stamp their outbound requests.
func RunTokenFor(scanCtxID string) string {
	attributionMu.RLock()
	defer attributionMu.RUnlock()
	return byScanCtx[scanCtxID].token // zero value "" via map default
}

// SetDialGuardBase installs the process-level operator-machine configuration
// used for traffic that carries no run token (browser, curl, system egress).
func SetDialGuardBase(cfg scopeguard.Config) {
	attributionMu.Lock()
	defer attributionMu.Unlock()
	dialGuardBase = cfg
}

// ProxyBypassList returns the Chromium proxy-bypass-list value for a scan.
// Loopback stays forced through the proxy ("<-loopback>") except for
// provisioned loopback ports the scan explicitly opted into, which Chromium
// dials directly: their per-scan scope-guard allowlist already vets them at
// the pre-proxy layer, and the dial guard must not refuse unattributed
// browser traffic to a target the operator stood up for this scan.
// ok is false when TLS interception is inactive — callers then keep their
// legacy behavior byte-for-byte.
func ProxyBypassList(scanCtxID string) (string, bool) {
	m := defaultManager.Load()
	if m == nil || !m.mitm {
		return "", false
	}
	attributionMu.RLock()
	entry := byScanCtx[scanCtxID]
	attributionMu.RUnlock()
	list := "<-loopback>"
	for _, port := range entry.guard.AllowLoopbackPorts {
		if port <= 0 || port == entry.guard.Port {
			continue
		}
		list += fmt.Sprintf(";127.0.0.1:%d;[::1]:%d", port, port)
	}
	return list, true
}

// resolveToken maps a bearer token back to its Run.
func resolveToken(token string) (Attribution, bool) {
	attributionMu.RLock()
	defer attributionMu.RUnlock()
	entry, ok := byToken[token]
	if !ok {
		return Attribution{}, false
	}
	return Attribution{InstanceID: entry.instanceID, ScanCtxID: entry.scanCtxID}, true
}

// guardFor applies the operator-machine rule to a direct upstream dial,
// preferring the owning scan's configuration (which carries per-scan
// provisioned loopback ports) over the process-level base.
func guardFor(scanCtxID, address string) error {
	// The base read shares attributionMu with SetDialGuardBase — the swap is
	// a test seam, but the guard runs on live dials.
	attributionMu.RLock()
	cfg := dialGuardBase
	if scanCtxID != "" {
		if entry, ok := byScanCtx[scanCtxID]; ok {
			cfg = entry.guard
		}
	}
	attributionMu.RUnlock()
	if scopeguard.IsLocalOrListener(cfg, address) {
		return fmt.Errorf("trafficproxy: %s is the operator's machine or the dashboard's listener", address)
	}
	return nil
}

// extractAttribution reads the run token from Proxy-Authorization. It MUST
// run before removeHopByHopHeaders strips the header, and the token value is
// never copied anywhere else (header values are masked out of observations).
func extractAttribution(r *http.Request) (Attribution, bool) {
	auth := r.Header.Get("Proxy-Authorization")
	bearer, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		return Attribution{}, false
	}
	return resolveToken(strings.TrimSpace(bearer))
}

var errRequiredNoUpstream = errors.New("trafficproxy: proxy-required mode has no upstream")
