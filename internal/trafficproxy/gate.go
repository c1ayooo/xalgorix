package trafficproxy

// High-risk approval gate hook (R2b closed loop, REDESIGN §5 × §4).
//
// Dependency direction (red line): trafficproxy must not import internal/web.
// The proxy therefore exposes a package-level hook; the web layer installs an
// adapter over ApprovalService at NewServer time when the approval pipeline
// runs in enforce mode. The hook is nil the rest of the time (mode off, bare
// proxy in tests) and the proxy side then behaves exactly as before —
// observe-only.
//
// What is adjudicated here, and what deliberately is not:
//
//   - ONLY the subprocess channel (Basic credentials minted to
//     curl/wget/python). Bearer traffic is 100% produced by the Go tool
//     transports whose every request already met the registry-level approval
//     gate — re-adjudicating it would double-charge one intent and park the
//     verifier's transport-level retests (which ride Bearer) on the operator
//     queue, colliding with the verifier timing invariant. Unattributed
//     traffic never gates either: system-level red line (observation.go) —
//     no attribution, no approval row, no InfoItem.
//   - ONLY high-risk verdicts (approval.AssessHTTP = Pend on a write method).
//     Read methods pass by construction.
//   - Upgrade requests are assessed on the VISIBLE method+URL before the
//     hijack (a client-supplied Upgrade header must not become a gate
//     bypass); after the hijack and on the blind-CONNECT leg there is no
//     body/plaintext visibility, so those raw-byte relays stay ungated —
//     documented boundary (REDESIGN §4).
//
// Liveness: Adjudicate may block synchronously — forward() runs on the
// per-connection goroutine, so a tunnel's subsequent requests queue behind
// the pending verdict (keep-alive serializes; this bounds approval storms to
// one in-flight row per tunnel). The adapter owns the deadline (timeout =
// rejection, D5) and the scan-cancellation exit; a client that disconnects
// mid-wait does not panic the writer — the response goes to a dead
// ResponseWriter and the error is dropped.

import (
	"sync"

	"github.com/xalgord/xalgorix/v4/internal/approval"
)

// HighRiskGate adjudicates one subprocess-channel high-risk request.
// Implementations block until a verdict exists (operator decision, deadline,
// run cancellation) and return the 403 body text on denial. err is reserved
// for infrastructure failures; the proxy fails OPEN on it (an approval
// pipeline outage must never wedge the proxy).
type HighRiskGate interface {
	Adjudicate(attr Attribution, verdict approval.Verdict) (approved bool, denialText string, err error)
}

var (
	highRiskGateMu sync.RWMutex
	highRiskGate   HighRiskGate
)

// SetHighRiskGate installs the web-layer adapter (NewServer, enforce mode
// only) or clears it with nil. Tests install stubs. The gate is process-wide
// because the loopback proxy is process-wide.
func SetHighRiskGate(g HighRiskGate) {
	highRiskGateMu.Lock()
	defer highRiskGateMu.Unlock()
	highRiskGate = g
}

// adjudicateHighRisk consults the installed hook. nil gate or hook error →
// approved (fail-open); everything else is the hook's verdict.
func adjudicateHighRisk(attr Attribution, verdict approval.Verdict) (bool, string, error) {
	highRiskGateMu.RLock()
	gate := highRiskGate
	highRiskGateMu.RUnlock()
	if gate == nil {
		return true, "", nil
	}
	return gate.Adjudicate(attr, verdict)
}

// highRiskVerdict assembles the gate payload from an observation. The URL is
// already redacted (redactURL) and the body is the bounded preview; header
// VALUES are never available here (only names are recorded), so Headers
// stays nil — the approval payload cannot leak what the proxy never saw.
func highRiskVerdict(obs *TrafficObservation, bodyPreview string) approval.Verdict {
	return approval.Verdict{
		Decision: approval.Require,
		Kind:     approval.KindHTTP,
		Reason:   obs.HighRisk,
		Method:   obs.Method,
		URL:      obs.URL,
		Body:     bodyPreview,
	}
}

// adjudicateSubprocessHighRisk is the single gate entry for both relay paths
// (transport forward and upgrade passthrough). A request proceeds when the
// verdict is not high-risk, the channel is not subprocess, the hook approves,
// or the hook errors (fail-open). On denial it returns the 403 body text;
// callers record obs.Status = 403 and never contact the upstream.
func adjudicateSubprocessHighRisk(attr Attribution, obs *TrafficObservation, bodyPreview string) (approved bool, denialText string) {
	if obs.HighRisk == "" || attr.Channel != ChannelSubprocess {
		return true, ""
	}
	approved, denialText, err := adjudicateHighRisk(attr, highRiskVerdict(obs, bodyPreview))
	if err != nil {
		// Fail-open: an approval-pipeline outage must not wedge the proxy.
		approved = true
	}
	return approved, denialText
}

// HighRiskGateInstalled reports whether a high-risk gate hook is currently
// installed. Test-only seam (exported for white-box wiring assertions like
// TunnelCount; not used on the request path).
func HighRiskGateInstalled() bool {
	highRiskGateMu.RLock()
	defer highRiskGateMu.RUnlock()
	return highRiskGate != nil
}
