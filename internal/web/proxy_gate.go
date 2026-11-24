package web

// proxy_gate.go — web adapter for the traffic proxy's high-risk gate (R2b
// approval closed loop, REDESIGN §5 × §4).
//
// The loopback MITM proxy sees subprocess traffic (curl/wget/python) that
// never met the registry-level approval gate: those children carry the run
// token as proxy credentials (env userinfo / .curlrc / .wgetrc) and the
// proxy attributes them via Proxy-Authorization: Basic. When such a request
// is high-risk (approval.AssessHTTP pends a write method), the proxy calls
// this adapter synchronously — installed as trafficproxy.SetHighRiskGate in
// NewServer, enforce mode only — and 403s the request on denial.
//
// Everything operator-facing is the SAME machinery the registry gate uses:
// submit() creates the row through the platform store (project/task/run
// ownership resolved per instance), claims a max_pending slot (FIFO
// overflow), broadcasts approval_requested/approval_decided through the
// normal WS path, and waits select { verdict | deadline | scanctx.Done } —
// timeout = rejection (D5), run stop = batch-cancelled. The payload goes
// through buildApprovalPayload (by-value redaction with the run's agent
// secret registry + header-name masking + URL userinfo masking) — no
// side-channel assembly, the red lines hold by construction.
//
// Fail-open posture (charter §6.6): no platform store → submit approves
// without a row; no live ScanContext (attribution raced teardown) → the
// adapter approves without ever parking the request. The proxy additionally
// fails open on adapter errors. Scanning never depends on the platform.
//
// Channel discipline: only the subprocess channel can reach this adapter
// (the proxy gates on it). The re-check below is defense in depth — Bearer
// traffic is the tool gate's jurisdiction (the verifier's transport-level
// retests ride it; its curl retests ride THIS channel and pend-to-timeout
// under enforce — accepted degradation, REDESIGN §5.2), unattributed traffic
// is system-level.

import (
	"log"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// proxyHighRiskGate adapts ApprovalService to the proxy hook.
type proxyHighRiskGate struct {
	svc *ApprovalService
}

// Adjudicate implements trafficproxy.HighRiskGate. It blocks until a verdict
// exists; on denial it returns the same operator-facing text the registry
// gate hands the model (status + approval id + reason + alternative path).
func (g *proxyHighRiskGate) Adjudicate(attr trafficproxy.Attribution, verdict approval.Verdict) (bool, string, error) {
	if attr.Channel != trafficproxy.ChannelSubprocess {
		// Bearer = already adjudicated at the tool gate; unattributed =
		// system-level. Never create rows for either.
		return true, "", nil
	}
	if g.svc == nil {
		return true, "", nil
	}
	sctx := scanctx.Get(attr.ScanCtxID)
	if sctx == nil {
		// The scan context is gone (finished/teared down between the proxy's
		// attribution and the verdict) — nothing to attach the row to, and
		// submit needs the context's cancellation channel. Fail open.
		log.Printf("[approval] proxy gate: scan context %s gone — failing open", attr.ScanCtxID)
		return true, "", nil
	}
	// Per-run posture: the task's audit switch (snapshotted on the context)
	// decides, falling back to the process default for manual/CLI runs.
	if g.svc.srv.runAuditMode(sctx) != approval.ModeEnforce {
		return true, "", nil
	}
	// Project fence first (scope_fence.go): the wire layer is part of the same
	// authorization boundary, and a remembered rule never widens it. Denial
	// here is a 403 with the fence reason — no operator decision is possible.
	if row, denied := g.svc.fenceDenial(attr.InstanceID, sctx, verdict, g.svc.srv.secretValuesForInstance(attr.InstanceID)); denied {
		return false, fenceDenialMessage(row), nil
	}
	// Scope observer (v4.6): the wire layer is adjudicated by the same
	// rules + built-in baseline as the command gate, so one decision covers
	// the same intent at either granularity. deny auto-rejects with a
	// terminal row; allow exempts without a row.
	switch obs := g.svc.observeScope(attr.InstanceID, verdict); obs.Decision {
	case scopeAllow:
		return true, "", nil
	case scopeDeny:
		row, err := g.svc.submitScopeDenial(attr.InstanceID, verdict, obs.Reason, g.svc.srv.secretValuesForInstance(attr.InstanceID))
		if err != nil {
			log.Printf("[approval] proxy gate: scope observer denial for run %s could not be recorded: %v", attr.InstanceID, err)
			return false, "⛔ SCOPE DENIAL — the request matches the project's scope-observer baseline and was NOT sent.", nil
		}
		return false, approvalDenialMessage(submitOutcome{Status: row.Status, Row: row}), nil
	}
	outcome := g.svc.submit(attr.InstanceID, sctx, verdict, g.svc.srv.secretValuesForInstance(attr.InstanceID))
	if outcome.Approved {
		return true, "", nil
	}
	return false, approvalDenialMessage(outcome), nil
}

// secretValuesForInstance returns the live agent's operator-credential
// registry for by-value payload redaction (HIGH-1 — targetAuthB credentials
// may ride subprocess bodies verbatim), or nil when no agent is bound (the
// proxy adapter can fire outside the agent's lifetime; name-based masking
// still applies inside buildApprovalPayload).
func (s *Server) secretValuesForInstance(instanceID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ag := s.currentAgents[instanceID]
	if ag == nil {
		return nil
	}
	return ag.SecretValues()
}

// installProxyHighRiskGate wires the adapter whenever an approval service
// exists. The adapter itself resolves the PER-RUN posture (task audit switch,
// falling back to the process default), so a task-level enforce switch works
// even when XALGORIX_APPROVAL_MODE is off. No service → nil, which keeps the
// proxy observe-only. Called from NewServer / SetApprovalService so every
// server instance (tests included) leaves the hook in a deterministic state.
func (s *Server) installProxyHighRiskGate() {
	if s.approvalSvc == nil {
		trafficproxy.SetHighRiskGate(nil)
		return
	}
	trafficproxy.SetHighRiskGate(&proxyHighRiskGate{svc: s.approvalSvc})
}
