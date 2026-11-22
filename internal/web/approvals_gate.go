package web

// approvals_gate.go — the HITL approval gate service (REDESIGN §5 / D5, R2a).
//
// Production scans enter through the Eino interrupt/resume broker: the tool
// wrapper creates a pending row, returns an ADK interrupt, and Runner.Resume
// delivers the decision. This file owns everything that needs the web layer:
// the platform store, per-instance slot accounting, WS broadcasting and the
// deadline/cancellation bookkeeping. The lower-level tools.Registry gate
// remains available to direct registry consumers but is not installed by
// scan_session.
//
// Lifecycle of one regulated call:
//
//	Eino ApprovalTool → broker.RequestApproval
//	  → Policy.Evaluate (internal/approval; mode=off short-circuits before
//	    any classification and the gate is not even installed)
//	  → Require → Submit: wait for a pending slot (max_pending per run,
//	    overflow queues, REDESIGN §5.3) → create the approval row
//	    (payload masked per API_CONTRACT §3: header-name masking + by-value
//	    redaction with the agent's secret registry + URL userinfo masking)
//	    → broadcast approval_requested
//	  → select { verdict | deadline timer | scanctx.Done }
//	  → finalize (single exit): persist terminal status, wake the tool call,
//	    release the slot, broadcast approval_decided.
//
// Timeout = rejection (D5): the deadline timer drives the same finalize path
// as an operator decision, with decided_by="timeout". A scan cancellation /
// run stop flips the row to cancelled (batch path: cancelPendingApprovalsFor-
// Instance, wired at every instance stop/pause/delete/shutdown flip point).
// Orphaned rows from a previous process (crash between create and verdict)
// are closed once at startup by ReconcilePending.
//
// Store calls carry a short deadline (approvalStoreCallTimeout): a wedged
// PostgreSQL must fail the gate open (charter §6.1 — scanning never depends
// on the platform), not park the tool call.
//
// Known interaction with the runtime's own tool ceilings
// (RegistryTool hardTimeoutFor(tool)+30s): the configured approval
// timeout must stay below that ceiling or the tool hard-timeout fires first
// and the approval verdict arrives too late to matter. newApprovalService
// warns at startup; the row is still closed by its own deadline timer, so
// nothing leaks — the tool result is simply abandoned.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/approval"
	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

// maxApprovalBodyBytes caps the stored body excerpt (API_CONTRACT §3
// body_excerpt ≤ 2KB); maxApprovalCommandBytes caps the command text at the
// ledger's 4096-char precedent so a giant python code block cannot bloat the
// approval row. Truncation is display-only — the tool still executes the
// original args.
const (
	maxApprovalBodyBytes    = 2048
	maxApprovalCommandBytes = 4096
)

// approvalStoreCallTimeout bounds every platform-store call the gate makes:
// a wedged PostgreSQL must surface as the gate's fail-open path within
// seconds, never park a tool call. Var so tests can shorten it.
var approvalStoreCallTimeout = 3 * time.Second

// redactionMarker matches the agent's telemetry redaction output
// (agent.redactSecrets) so approval payloads carry the same marker the rest
// of the audit trail shows.
const redactionMarker = "***REDACTED***"

// secretHeaderNames are masked with the shared redaction policy before an
// approval row is persisted (masks.go — the API never returns credential
// material). Superset of har.go authHeaderNames (authorization, cookie,
// x-api-key, x-auth-token, x-access-token, x-csrf-token, x-xsrf-token,
// authentication, proxy-authorization) plus approval-only shapes.
// Keys are matched case-insensitively. NOTE: name-based masking alone is NOT
// sufficient — buildApprovalPayload also applies by-value redaction with the
// agent's secret registry to every text field (HIGH-1).
var secretHeaderNames = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"authentication":      {},
	"x-api-key":           {},
	"api-key":             {},
	"apikey":              {},
	"x-auth-token":        {},
	"x-access-token":      {},
	"x-csrf-token":        {},
	"x-xsrf-token":        {},
	"x-secret":            {},
	"private-token":       {},
}

// ApprovalService implements the web-layer half of the approval gate. One
// instance per Server; store access goes through currentPlatformStore so the
// service is usable in both PG and file mode (and returns fail-open when no
// store is wired — scanning never depends on the platform, charter §6.6).
type ApprovalService struct {
	srv    *Server
	policy approval.Policy

	mu sync.Mutex
	// pendingCount tracks in-flight submits holding a slot, keyed by
	// instance. In-memory (not a store count) so the max_pending check is
	// atomic with waiter registration under mu (MEDIUM-2: no check-then-act
	// window) and a wedged store cannot deadlock the accounting.
	pendingCount map[string]int
	slotWaiters  map[string][]chan struct{}        // instanceID → FIFO slot queue
	verdicts     map[string]chan platform.Approval // approvalID → waiter
	// reconciled guards the one-shot startup reconciliation (MEDIUM-1).
	reconciled bool
}

func newApprovalService(srv *Server) *ApprovalService {
	mode := approval.ModeOff
	if srv.cfg != nil && strings.EqualFold(strings.TrimSpace(srv.cfg.ApprovalMode), "enforce") {
		mode = approval.ModeEnforce
	}
	timeout := 600 * time.Second
	maxPending := 1
	var allowlist []string
	if srv.cfg != nil {
		if srv.cfg.ApprovalTimeoutSeconds > 0 {
			timeout = time.Duration(srv.cfg.ApprovalTimeoutSeconds) * time.Second
		}
		if srv.cfg.ApprovalMaxPending > 0 {
			maxPending = srv.cfg.ApprovalMaxPending
		}
		allowlist = splitAllowlist(srv.cfg.ApprovalAllowlist)
	}
	// Timeout must fit inside the smallest gated tool's hard ceiling
	// (browser_action: 10m + 30s grace, agent.go toolHardTimeout) or the
	// tool timeout wins the race and the verdict is dead on arrival.
	if mode == approval.ModeEnforce && timeout > 10*time.Minute+30*time.Second {
		log.Printf("[approval] WARNING: XALGORIX_APPROVAL_TIMEOUT=%s exceeds the smallest tool hard ceiling (browser_action 10m + 30s grace). The agent's own tool timeout will fire first and abandon the tool call; the approval row is still closed by this deadline, but the verdict reaches no one. Lower the timeout or raise the tool ceiling.", timeout)
	}
	return &ApprovalService{
		srv: srv,
		policy: approval.Policy{
			Mode:       mode,
			Timeout:    timeout,
			Allowlist:  allowlist,
			MaxPending: maxPending,
		},
		pendingCount: make(map[string]int),
		slotWaiters:  make(map[string][]chan struct{}),
		verdicts:     make(map[string]chan platform.Approval),
	}
}

// storeCtx returns a short-deadline context for one platform-store call: a
// wedged PostgreSQL fails the gate's fail-open paths in seconds instead of
// parking a tool call (charter §6.1).
func storeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), approvalStoreCallTimeout)
}

func splitAllowlist(raw string) []string {
	out := []string{}
	for _, entry := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (s *Server) approvalService() *ApprovalService {
	s.approvalSvcMu.RLock()
	defer s.approvalSvcMu.RUnlock()
	return s.approvalSvc
}

// SetApprovalService wires the gate service (constructed by NewServer; tests
// may replace it). nil disables the pipeline. The trafficproxy hook is
// re-installed so it always wraps the CURRENT service — a mode flip between
// enforce/off must never leave a stale adapter pointing at a retired
// service.
func (s *Server) SetApprovalService(svc *ApprovalService) {
	s.approvalSvcMu.Lock()
	s.approvalSvc = svc
	s.approvalSvcMu.Unlock()
	s.installProxyHighRiskGate()
}

// submitOutcome is the gate service's answer to one regulated call.
type submitOutcome struct {
	Approved bool // approved → the tool executes
	Status   string
	Row      *platform.Approval
}

// createPendingRow creates the approval row and broadcasts approval_requested
// without waiting. Eino's interrupt/resume path uses it; the legacy registry
// gate keeps using submit (create + wait) below. secrets carries the agent's
// credential registry for by-value payload redaction (HIGH-1).
func (s *ApprovalService) createPendingRow(instanceID string, sctx *scanctx.ScanContext, verdict approval.Verdict, secrets []string) (*platform.Approval, error) {
	store := s.srv.currentPlatformStore()
	if store == nil {
		return nil, nil // fail-open: scanning never depends on the platform
	}
	ctx, cancel := storeCtx()
	defer cancel()
	if err := s.waitForSlot(instanceID, sctx); err != nil {
		return nil, err
	}
	projectID, taskID, runID := s.resolveOwnership(ctx, store, instanceID)
	row, err := store.CreateApproval(ctx, platform.ApprovalInput{
		ProjectID:  projectID,
		TaskID:     taskID,
		RunID:      runID,
		InstanceID: instanceID,
		Kind:       verdict.Kind,
		Payload:    buildApprovalPayload(verdict, secrets),
		DeadlineAt: time.Now().Add(s.policy.Timeout),
	})
	if err != nil {
		s.releaseSlot(instanceID) // the reserved slot was never bound to a row
		return nil, fmt.Errorf("create approval: %w", err)
	}
	requested := WSEvent{
		Type:     "approval_requested",
		Content:  fmt.Sprintf("Approval required: %s", verdict.Reason),
		Approval: row,
	}
	s.srv.broadcastToInstance(instanceID, requested)
	s.srv.appendConversationEvent(instanceID, requested)
	return row, nil
}

// waitApproval polls the approval row until it leaves pending, the deadline
// expires, or the scan is cancelled. The R2 REST/WS layer remains the only
// writer of operator decisions; timeout/cancel go through the same finalize
// exit (slot release + approval_decided broadcast).
func (s *ApprovalService) waitApproval(ctx context.Context, approvalID string) (platform.Approval, error) {
	store := s.srv.currentPlatformStore()
	if store == nil {
		return platform.Approval{Status: platform.ApprovalApproved}, nil
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		row, err := store.GetApproval(ctx, approvalID)
		if err != nil {
			return platform.Approval{}, err
		}
		if row.Status != platform.ApprovalPending {
			return *row, nil
		}
		if !time.Now().Before(row.DeadlineAt) {
			final, ferr := s.finalize(row, platform.ApprovalDecision{
				Decision:  platform.ApprovalTimeout,
				DecidedBy: "timeout",
				Reason:    fmt.Sprintf("approval deadline (%s) elapsed — timeout = rejection (REDESIGN D5)", s.policy.Timeout),
			})
			if ferr != nil {
				final, ferr = s.adoptConflict(row, ferr)
				if ferr != nil {
					return platform.Approval{}, ferr
				}
			}
			return *final, nil
		}
		select {
		case <-ctx.Done():
			final, ferr := s.finalize(row, platform.ApprovalDecision{
				Decision:  platform.ApprovalCancelled,
				DecidedBy: "system",
				Reason:    "scan cancelled while approval was pending",
			})
			if ferr != nil {
				final, ferr = s.adoptConflict(row, ferr)
				if ferr != nil {
					return platform.Approval{}, ferr
				}
			}
			return *final, nil
		case <-ticker.C:
		}
	}
}

// submit runs the pending-row lifecycle for one Require verdict. secrets is
// the agent's operator-credential registry (HIGH-1): applied to every payload
// text field before persistence/broadcast.
func (s *ApprovalService) submit(instanceID string, sctx *scanctx.ScanContext, verdict approval.Verdict, secrets []string) submitOutcome {
	store := s.srv.currentPlatformStore()
	if store == nil {
		// No platform store: the approval pipeline is a cold-path capability
		// and scanning never depends on it (charter §6.6). Fail-open with a
		// loud log — silently blocking every tool call on a missing store
		// would break the no-DSN degradation red line.
		log.Printf("[approval] store unavailable — failing open for %s call in run %s", verdict.Kind, instanceID)
		return submitOutcome{Approved: true, Status: "store_unavailable"}
	}

	// Slot accounting (max_pending per run, REDESIGN §5.3): overflow queues
	// for a free slot instead of failing — an approval storm must not turn
	// into a rejection storm. Atomic check/register under s.mu (MEDIUM-2);
	// the counter is in-memory so a wedged store cannot deadlock it.
	if err := s.waitForSlot(instanceID, sctx); err != nil {
		return submitOutcome{Approved: false, Status: platform.ApprovalCancelled}
	}

	ctx, cancel := storeCtx()
	defer cancel()
	projectID, taskID, runID := s.resolveOwnership(ctx, store, instanceID)
	row, err := store.CreateApproval(ctx, platform.ApprovalInput{
		ProjectID:  projectID,
		TaskID:     taskID,
		RunID:      runID,
		InstanceID: instanceID,
		Kind:       verdict.Kind,
		Payload:    buildApprovalPayload(verdict, secrets),
		DeadlineAt: time.Now().Add(s.policy.Timeout),
	})
	if err != nil {
		s.releaseSlot(instanceID) // the reserved slot was never bound to a row
		log.Printf("[approval] create approval failed for run %s: %v — failing open", instanceID, err)
		return submitOutcome{Approved: true, Status: "store_unavailable"}
	}

	ch := make(chan platform.Approval, 1)
	s.mu.Lock()
	s.verdicts[row.ID] = ch
	s.mu.Unlock()
	// Every terminal exit delivers through finalize (which deletes the
	// entry); the defer only covers the pathological paths (finalize error
	// that is neither conflict nor not-found, panics) so the map cannot leak.
	defer func() {
		s.mu.Lock()
		delete(s.verdicts, row.ID)
		s.mu.Unlock()
	}()

	requested := WSEvent{
		Type:     "approval_requested",
		Content:  fmt.Sprintf("Approval required: %s", verdict.Reason),
		Approval: row,
	}
	s.srv.broadcastToInstance(instanceID, requested)
	s.srv.appendConversationEvent(instanceID, requested)

	wait := time.Until(row.DeadlineAt)
	if wait <= 0 {
		wait = time.Nanosecond
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case decided := <-ch:
		return outcomeFromRow(decided)

	case <-timer.C:
		final, err := s.finalize(row, platform.ApprovalDecision{
			Decision:  platform.ApprovalTimeout,
			DecidedBy: "timeout",
			Reason:    fmt.Sprintf("approval deadline (%s) elapsed — timeout = rejection (REDESIGN D5)", s.policy.Timeout),
		})
		if err != nil {
			// Raced with an operator decision / run-stop batch cancel: adopt
			// the winning state — the caller still needs a verdict.
			final, err = s.adoptConflict(row, err)
			if err != nil {
				log.Printf("[approval] timeout finalize for %s: %v", row.ID, err)
				return submitOutcome{Approved: false, Status: platform.ApprovalTimeout, Row: row}
			}
		}
		return outcomeFromRow(*final)

	case <-sctx.Ctx.Done():
		final, err := s.finalize(row, platform.ApprovalDecision{
			Decision:  platform.ApprovalCancelled,
			DecidedBy: "system",
			Reason:    "scan cancelled while approval was pending",
		})
		if err != nil {
			final, err = s.adoptConflict(row, err)
			if err != nil {
				log.Printf("[approval] cancel finalize for %s: %v", row.ID, err)
				return submitOutcome{Approved: false, Status: platform.ApprovalCancelled, Row: row}
			}
		}
		return outcomeFromRow(*final)
	}
}

// adoptConflict resolves a lost optimistic-lock race for the gate-waiter
// path: another exit path (operator decision, deadline sweep, batch cancel)
// won, so the waiter adopts the winning state. The operator REST path must
// NOT adopt — a second decision surfaces ErrConflict as 409 (Decide).
func (s *ApprovalService) adoptConflict(row *platform.Approval, err error) (*platform.Approval, error) {
	if !errors.Is(err, platform.ErrConflict) && !errors.Is(err, platform.ErrNotFound) {
		return nil, err
	}
	store := s.srv.currentPlatformStore()
	if store == nil {
		return nil, err
	}
	ctx, cancel := storeCtx()
	defer cancel()
	return store.GetApproval(ctx, row.ID)
}

// finalize is the single terminal transition: persist (optimistic lock —
// only one of operator/timeout/cancel can win), wake the waiting tool call,
// release the pending slot, and broadcast approval_decided. Strict: a lost
// race propagates ErrConflict to the caller, which decides whether to adopt
// (gate waiters) or surface it (operator REST → 409).
func (s *ApprovalService) finalize(row *platform.Approval, dec platform.ApprovalDecision) (*platform.Approval, error) {
	store := s.srv.currentPlatformStore()
	if store == nil {
		return nil, errors.New("approval store unavailable")
	}
	ctx, cancel := storeCtx()
	defer cancel()
	final, err := store.DecideApproval(ctx, row.ID, dec)
	if err != nil {
		return nil, err
	}
	s.releaseSlot(final.InstanceID)
	s.deliverVerdict(final.ID, *final)
	decided := WSEvent{
		Type:     "approval_decided",
		Content:  fmt.Sprintf("Approval %s: %s", final.Status, final.Kind),
		Approval: final,
	}
	s.srv.broadcastToInstance(final.InstanceID, decided)
	s.srv.appendConversationEvent(final.InstanceID, decided)
	return final, nil
}

// Decide is the operator (REST) entry point: persist via the optimistic lock,
// then wake + broadcast through the same exit path as timeout/cancel. The
// handler maps ErrNotFound/ErrConflict onto 404/409.
func (s *ApprovalService) Decide(ctx context.Context, id, decision, decidedBy, reason string) (*platform.Approval, error) {
	store := s.srv.currentPlatformStore()
	if store == nil {
		return nil, errors.New("approval store unavailable")
	}
	row, err := store.GetApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.finalize(row, platform.ApprovalDecision{
		Decision:  decision,
		DecidedBy: decidedBy,
		Reason:    reason,
	})
}

// CancelPendingForInstance closes every pending approval of a run (stop /
// pause / delete / shutdown flip points, REDESIGN §5.3) through the same
// exit path so waiting tool calls wake immediately.
func (s *ApprovalService) CancelPendingForInstance(ctx context.Context, instanceID, reason string) []platform.Approval {
	store := s.srv.currentPlatformStore()
	if store == nil {
		return nil
	}
	rows, err := store.CancelPendingByInstance(ctx, instanceID, reason)
	if err != nil {
		log.Printf("[approval] batch cancel for run %s failed: %v", instanceID, err)
		return nil
	}
	for _, row := range rows {
		s.releaseSlot(instanceID)
		s.deliverVerdict(row.ID, row)
		s.srv.broadcastToInstance(instanceID, WSEvent{
			Type:     "approval_decided",
			Content:  fmt.Sprintf("Approval cancelled: %s", reason),
			Approval: &row,
		})
	}
	return rows
}

// ReconcilePending closes orphaned pending approvals once at startup
// (MEDIUM-1): a row whose deadline already elapsed flips to timeout
// (decided_by=timeout — the truthful record of what would have happened);
// every other pending row belongs to a waiter goroutine that cannot survive
// a process restart, so it flips to cancelled (decided_by=system, reason
// "startup reconciliation"). Fixes both ghost slot reservations and
// pending_total pollution. Runs at most once per service instance (guard
// under mu — SetPlatformStore is also called repeatedly by tests).
func (s *ApprovalService) ReconcilePending() {
	store := s.srv.currentPlatformStore()
	if store == nil {
		return
	}
	s.mu.Lock()
	if s.reconciled {
		s.mu.Unlock()
		return
	}
	s.reconciled = true
	s.mu.Unlock()

	ctx, cancel := storeCtx()
	defer cancel()
	rows, err := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending"})
	if err != nil {
		log.Printf("[approval] startup reconciliation list failed: %v", err)
		return
	}
	now := time.Now()
	for i := range rows {
		row := &rows[i]
		var dec platform.ApprovalDecision
		switch {
		case !now.Before(row.DeadlineAt):
			dec = platform.ApprovalDecision{
				Decision:  platform.ApprovalTimeout,
				DecidedBy: "timeout",
				Reason:    "approval deadline elapsed before startup reconciliation (timeout = rejection)",
			}
		default:
			dec = platform.ApprovalDecision{
				Decision:  platform.ApprovalCancelled,
				DecidedBy: "system",
				Reason:    "startup reconciliation",
			}
		}
		final, err := s.finalize(row, dec)
		if err != nil {
			log.Printf("[approval] startup reconciliation of %s failed: %v", row.ID, err)
		} else {
			log.Printf("[approval] startup reconciliation: %s → %s (instance %s)", final.ID, final.Status, final.InstanceID)
		}
	}
}

// cancelPendingApprovalsForInstance closes every pending approval of a run
// (REDESIGN §5.3: run stop ⇒ pending batch cancelled). Called beside every
// instance status flip — Stop All, per-instance stop, pause, delete halt,
// SIGTERM shutdown, orchestrator finalize — so no approval outlives its run.
// Never touches the global stopReq flag (scan_lifecycle_test.go contract).
func (s *Server) cancelPendingApprovalsForInstance(instanceID, reason string) {
	if svc := s.approvalService(); svc != nil && instanceID != "" {
		svc.CancelPendingForInstance(context.Background(), instanceID, reason)
	}
}

// resolveOwnership maps an instance onto its project/task/run mount records.
// An instance without a mounted run (legacy direct dispatch, failed mount)
// belongs to the implicit default project (R2a decision).
func (s *ApprovalService) resolveOwnership(ctx context.Context, store platform.Repository, instanceID string) (projectID, taskID, runID string) {
	runs, err := store.RunsByInstanceID(ctx, instanceID)
	if err == nil && len(runs) > 0 {
		run := runs[0]
		taskID, runID = run.TaskID, run.ID
		if task, terr := store.GetTask(ctx, taskID); terr == nil {
			return task.ProjectID, taskID, runID
		}
	}
	if project, perr := store.EnsureDefaultProject(ctx); perr == nil {
		return project.ID, taskID, runID
	}
	return "", taskID, runID
}

// ── operator allow rules ("approve & remember", REDESIGN §5.2, contract §3.2) ─

// matchRule consults the operator-authored allow rules for a Require verdict.
// Both gate layers call it (einoApprovalBroker for the command gate,
// proxyHighRiskGate for the wire gate), so one remembered decision exempts
// the same intent at either layer. Failure posture: nil store, empty project,
// and store errors all return nil — a failed rule read must NEVER auto-allow,
// it only falls back to the normal approval flow. Expired rules do not match
// (expiry is match-time; the row stays listed for the operator to see).
// A hit increments the rule's counters (best effort) and broadcasts
// approval_rule_hit — the audit projection of "why no approval row exists
// for this call"; the approvals table remains the state authority for rows
// that DO pend.
func (s *ApprovalService) matchRule(instanceID string, verdict approval.Verdict) *platform.ApprovalRule {
	store := s.srv.currentPlatformStore()
	if store == nil {
		return nil
	}
	if verdict.Kind != approval.KindCommand && verdict.Kind != approval.KindHTTP {
		return nil
	}
	ctx, cancel := storeCtx()
	defer cancel()
	projectID, _, _ := s.resolveOwnership(ctx, store, instanceID)
	if projectID == "" {
		return nil
	}
	rules, err := store.ListApprovalRules(ctx, platform.ApprovalRuleFilter{ProjectID: projectID, Kind: verdict.Kind})
	if err != nil {
		log.Printf("[approval] rule consult for run %s failed: %v — falling back to approval", instanceID, err)
		return nil
	}
	now := time.Now()
	for i := range rules {
		r := &rules[i]
		if r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
			continue
		}
		switch verdict.Kind {
		case approval.KindCommand:
			if !s.policy.MatchCommandRule(verdict.Command, r.Match.Program, r.Match.Contains) {
				continue
			}
		case approval.KindHTTP:
			if !approval.MatchHTTPRule(verdict.Method, verdict.URL, verdict.Body, r.Match.Method, r.Match.Host, r.Match.PathPrefix, r.Match.Contains) {
				continue
			}
		}
		if err := store.RecordApprovalRuleHit(ctx, r.ID); err != nil {
			log.Printf("[approval] rule hit bookkeeping for %s: %v", r.ID, err)
		}
		s.srv.broadcastToInstance(instanceID, WSEvent{
			Type:    "approval_rule_hit",
			Content: fmt.Sprintf("Approval rule matched — %s allowed without an approval row (%s)", ruleDisplayText(r), r.ID),
			Rule:    r,
		})
		return r
	}
	return nil
}

// ruleDisplayText renders a rule's matching coordinates for the event feed
// (machine data stays mono in the UI).
func ruleDisplayText(r *platform.ApprovalRule) string {
	if r.Kind == platform.ApprovalKindHTTP {
		path := strings.Trim(r.Match.PathPrefix, "/")
		if path != "" {
			path = "/" + path
		}
		return fmt.Sprintf("%s %s%s", strings.ToUpper(r.Match.Method), r.Match.Host, path)
	}
	if r.Match.Contains != "" {
		return fmt.Sprintf("%s ~%s", r.Match.Program, r.Match.Contains)
	}
	return r.Match.Program
}

// ── slot accounting ──────────────────────────────────────────────────────────
//
// MEDIUM-2: the max_pending check and the waiter registration happen under
// ONE mutex hold, eliminating the check-then-act window (a release landing
// between "count ≥ max" and "register waiter" could previously wake nobody
// and park the submit until the next release). The counter is in-memory:
// waitForSlot claims (increment) happens under the same lock, releaseSlot
// decrements and hands the freed slot directly to the FIFO head — the woken
// submit becomes the slot holder WITHOUT re-incrementing, so a concurrent
// newcomer can never overshoot max_pending. Row creation outside the service
// (tests seeding rows) does not consume service slots; the store count stays
// advisory (OpsPulse/list views).

func (s *ApprovalService) waitForSlot(instanceID string, sctx *scanctx.ScanContext) error {
	s.mu.Lock()
	if s.pendingCount[instanceID] < s.policy.MaxPending {
		s.pendingCount[instanceID]++
		s.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	s.slotWaiters[instanceID] = append(s.slotWaiters[instanceID], ch)
	s.mu.Unlock()
	select {
	case <-ch: // slot handed over by releaseSlot (net-zero count change)
		return nil
	case <-sctx.Ctx.Done():
		s.removeSlotWaiter(instanceID, ch)
		return fmt.Errorf("cancelled while waiting for an approval slot")
	}
}

// releaseSlot closes out one claimed slot after a terminal transition: the
// in-memory count drops, and the freed slot is handed directly to the FIFO
// head waiter (who proceeds without re-incrementing). Called once per
// terminal transition of one approval row — exactly one release per claimed
// slot (the optimistic lock guarantees a single finalize winner).
func (s *ApprovalService) releaseSlot(instanceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingCount[instanceID] > 0 {
		s.pendingCount[instanceID]--
	}
	if s.pendingCount[instanceID] <= 0 {
		delete(s.pendingCount, instanceID)
	}
	if waiters := s.slotWaiters[instanceID]; len(waiters) > 0 {
		head := waiters[0]
		rest := append([]chan struct{}(nil), waiters[1:]...)
		if len(rest) == 0 {
			delete(s.slotWaiters, instanceID)
		} else {
			s.slotWaiters[instanceID] = rest
		}
		close(head)
	}
}

func (s *ApprovalService) removeSlotWaiter(instanceID string, ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.slotWaiters[instanceID]
	for i, w := range waiters {
		if w == ch {
			rest := append(waiters[:i:i], waiters[i+1:]...)
			if len(rest) == 0 {
				delete(s.slotWaiters, instanceID)
			} else {
				s.slotWaiters[instanceID] = rest
			}
			return
		}
	}
}

// deliverVerdict wakes the blocked registry.Execute. Buffered 1 + non-blocking
// send: a decision racing the waiter's teardown (deadline vs operator) drops
// instead of blocking the REST handler.
func (s *ApprovalService) deliverVerdict(id string, row platform.Approval) {
	s.mu.Lock()
	ch := s.verdicts[id]
	delete(s.verdicts, id)
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- row:
		default:
		}
	}
}

// ── payload assembly (API_CONTRACT §3; masks.go redaction policy) ────────────
//
// Two redaction layers, both applied BEFORE persistence/broadcast (HIGH-1):
//   1. by-value (redactByValue): the agent's secretValues registry — the same
//      operator credentials redacted from tool_call/tool_result events
//      (agent.go emit). This is the layer that protects targetAuthB traffic:
//      the model sends those credentials manually in bodies/commands, exactly
//      what approval payloads display.
//   2. by-name shape (maskSecretHeaders + maskURLUserInfo): structural
//      masking for credentials the registry cannot know (target-side session
//      cookies the scan captured, embedded userinfo).
// Neither layer alone is sufficient; the payload must pass both.

func buildApprovalPayload(verdict approval.Verdict, secrets []string) platform.ApprovalPayload {
	payload := platform.ApprovalPayload{
		Reason:  verdict.Reason,
		Command: maskURLUserInfo(redactByValue(verdict.Command, secrets)),
		Workdir: verdict.Workdir,
		Method:  verdict.Method,
		URL:     maskURLUserInfo(redactByValue(verdict.URL, secrets)),
	}
	payload.Headers = maskSecretHeaders(verdict.Headers, secrets)
	payload.BodyExcerpt = maskURLUserInfo(redactByValue(verdict.Body, secrets))
	// Truncation LAST: the redaction marker shrinks text, the ceiling is the
	// storage bound.
	payload.Command = truncateRunes(payload.Command, maxApprovalCommandBytes)
	payload.BodyExcerpt = truncateRunes(payload.BodyExcerpt, maxApprovalBodyBytes)
	return payload
}

// redactByValue replaces operator-supplied secret values with the shared
// redaction marker — the same replacement agent.redactSecrets applies to
// emitted telemetry. Applied to command, body excerpt, every header value
// and the URL before an approval row exists anywhere (store, WS, API).
func redactByValue(s string, secrets []string) string {
	if s == "" || len(secrets) == 0 {
		return s
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, redactionMarker)
		}
	}
	return s
}

// maskURLUserInfo structurally masks embedded URL credentials
// (https://user:pass@host → https://***REDACTED***@host) regardless of the
// registry: userinfo in a URL is a credential by definition. The '@' only
// counts as userinfo when it appears before the first path/query slash.
func maskURLUserInfo(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return raw
	}
	rest := raw[i+3:]
	slash := strings.IndexByte(rest, '/')
	at := strings.IndexByte(rest, '@')
	if at <= 0 || (slash >= 0 && at > slash) {
		return raw
	}
	return raw[:i+3] + redactionMarker + "@" + rest[at+1:]
}

// maskSecretHeaders applies BOTH layers to every header value: by-value
// redaction first (the agent's registry), then maskAuthCredential shape
// masking for secret-class header names. Non-secret headers keep their
// (redacted) values so the operator can actually judge the request.
func maskSecretHeaders(headers map[string]string, secrets []string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		v = redactByValue(v, secrets)
		if _, secret := secretHeaderNames[strings.ToLower(k)]; secret {
			out[k] = maskAuthCredential(v)
		} else {
			out[k] = v
		}
	}
	return out
}

// truncateRunes cuts s to at most max BYTES with rune (code point) safety:
// the byte ceiling is the contract's real storage bound (API_CONTRACT §3
// body_excerpt ≤ 2KB), while cutting mid-rune would corrupt CJK text. CJK
// input therefore ends up ≤ max bytes (fewer code points), never ~3x the
// ceiling.
func truncateRunes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	suffix := "..."
	ceil := max
	if ceil > len(suffix) {
		ceil -= len(suffix)
	} else {
		suffix = ""
		ceil = max
	}
	runes := []rune(s)
	b := 0
	cut := 0
	for cut < len(runes) {
		size := len(string(runes[cut]))
		if b+size > ceil {
			break
		}
		b += size
		cut++
	}
	return string(runes[:cut]) + suffix
}

// outcomeFromRow translates a terminal row into the gate outcome.
func outcomeFromRow(row platform.Approval) submitOutcome {
	out := submitOutcome{Status: row.Status, Row: &row}
	switch row.Status {
	case platform.ApprovalApproved:
		out.Approved = true
	case platform.ApprovalPending:
		// Impossible from a finalize exit; treat defensively as not approved.
		out.Approved = false
	default:
		out.Approved = false
	}
	return out
}

// approvalDenialMessage is the tool-result text the model sees on a denial —
// styled after the existing ⛔ block messages (shouldBlockForOutOfScope):
// status + reason + the suggested alternative path, so the agent redirects
// instead of retrying the same call.
func approvalDenialMessage(outcome submitOutcome) string {
	kind := "request"
	allowHint := "pick a non-destructive probe that stays on the read-only allowlist (subfinder/httpx/dnsx/amass, nmap -sV, curl -I/GET) and continue allowed work"
	if outcome.Row != nil && outcome.Row.Kind == platform.ApprovalKindHTTP {
		kind = "HTTP request"
		allowHint = "continue with read-only HTTP reconnaissance (GET/HEAD/OPTIONS) and note the blocked write for the operator"
	}
	status := outcome.Status
	if status == "" {
		status = "rejected"
	}
	detail := ""
	if outcome.Row != nil && outcome.Row.Reason != "" {
		detail = "\nReason: " + outcome.Row.Reason
	}
	switch status {
	case platform.ApprovalTimeout:
		return fmt.Sprintf("⛔ APPROVAL TIMEOUT — the operator did not respond before the deadline, so the %s was NOT executed (timeout = rejection, approval %s).%s\nDo NOT retry the same call: the deadline applies to every attempt. %s, and record a typed disposition for the blocked lane.",
			kind, approvalRef(outcome), detail, allowHint)
	case platform.ApprovalCancelled:
		return fmt.Sprintf("⛔ APPROVAL CANCELLED — the scan stopped while this %s awaited approval, so it was NOT executed (approval %s).%s\nDo NOT retry: the run is stopping. Wrap up the current lane and finish.",
			kind, approvalRef(outcome), detail)
	default:
		return fmt.Sprintf("⛔ APPROVAL REJECTED — the operator declined this %s (approval %s).%s\nDo NOT retry the same call with cosmetic changes: take the alternative path instead. %s.",
			kind, approvalRef(outcome), detail, allowHint)
	}
}

func approvalRef(outcome submitOutcome) string {
	if outcome.Row == nil {
		return "n/a"
	}
	return outcome.Row.ID
}
