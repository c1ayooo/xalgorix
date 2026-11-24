// Package scanctx provides per-scan-session state isolation.
// Each ScanContext encapsulates the mutable state (vulnerabilities, notes,
// terminal processes, browser instance) that was previously stored in
// package-level globals. This allows multiple concurrent scan sessions
// without data corruption.
package scanctx

import (
	"context"
	"fmt"
	"log"
	"math"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/safe"
)

// RequestRatePolicy is the per-scan outbound request budget. MaxRPS is kept as
// a float so operator settings like 2.5 rps can be represented, while command
// adapters can derive conservative integer flags from it.
type RequestRatePolicy struct {
	MaxRPS float64
	Source string
}

// NormalizeRequestRatePolicy applies the minimum enforceable request policy.
// Scanner CLIs generally accept whole-number rates, so sub-1 RPS instructions
// are raised to the lowest supported cap instead of being silently represented
// as a fractional policy that most tools cannot honor.
func NormalizeRequestRatePolicy(policy RequestRatePolicy) RequestRatePolicy {
	if policy.MaxRPS > 0 && policy.MaxRPS < 1 {
		policy.MaxRPS = 1
		if policy.Source != "" {
			policy.Source += "; minimum supported 1 rps"
		} else {
			policy.Source = "minimum supported 1 rps"
		}
	}
	return policy
}

// Enabled reports whether a request-rate policy should be enforced.
func (p RequestRatePolicy) Enabled() bool {
	return p.MaxRPS > 0
}

// CommandRPS returns the highest whole-number rate that does not exceed MaxRPS.
func (p RequestRatePolicy) CommandRPS() int {
	if p.MaxRPS <= 0 {
		return 0
	}
	n := int(math.Floor(p.MaxRPS))
	if n < 1 {
		return 1
	}
	return n
}

// Delay returns the minimum gap between requests needed to stay under MaxRPS.
func (p RequestRatePolicy) Delay() time.Duration {
	if p.MaxRPS <= 0 {
		return 0
	}
	ms := int(math.Ceil(1000 / p.MaxRPS))
	if ms < 1 {
		ms = 1
	}
	return time.Duration(ms) * time.Millisecond
}

// ScanContext holds all mutable state for a single scan session.
// Create one per scan instance via New(), and thread it through
// the tool registry so tools read/write their own session's state.
type ScanContext struct {
	ID      string
	ScanDir string
	// InstanceID is the D11 run identity stamped by the session wiring. The
	// authoritative dual graph is keyed by it so a re-dispatch/sibling
	// session shares the same plan. It is set once before the scan runs and
	// only read afterwards.
	InstanceID string
	// ProjectID / TaskID are the situational coordinates of this run
	// (charter §3.1). The asset graph is project-scoped; task links carry the
	// run provenance. Empty is allowed for CLI/offline runs.
	ProjectID string
	TaskID    string
	// AuditMode is the effective HITL/audit posture snapshotted from the
	// task's audit_mode at session start: "off" (observe-only) or "enforce"
	// (approval rows block the call). Empty = follow the process default
	// (XALGORIX_APPROVAL_MODE). The agent's tool gate and the traffic-proxy
	// wire gate read this single field, so a task switch cannot leave the two
	// layers disagreeing.
	AuditMode string
	// RequireEvidenceRefs enables the report gate's wire-evidence requirement:
	// a non-info finding must name a resolvable traffic_refs/evidence_outputs
	// entry (engine-authoritative proof excepted). Production sessions set it;
	// unit-test contexts leave it false so synthetic reports stay constructible.
	RequireEvidenceRefs bool

	Vulns    *VulnStore
	Notes    *NoteStore
	Terminal *TerminalState
	Browser  *BrowserState
	// Ledger is the durable, scan-shared hypothesis/evidence graph. It is the
	// one place the coordinator and every delegated specialist record and read
	// attack hypotheses, evidence, and status, and it persists to
	// <ScanDir>/ledger.json so it survives restart/resume.
	Ledger *LedgerStore
	// Coverage is the live scan-shared endpoint × class matrix. It lets the
	// coordinator reconcile work executed by parallel delegated specialists.
	Coverage *CoverageStore
	// Progress is the in-memory, scan-shared progress ledger projection
	// (PLANNER_OBSERVATION_AND_CORRECTION §3). It records which worker produced
	// information-layer increments and when, so kill validation is a fact
	// check rather than an LLM judgment. It is never persisted and never a
	// state authority.
	Progress *ProgressStore
	// SubmitGuard is the scan-shared platform submission discipline ledger
	// (BENCHMARK_ROUND5_ANALYSIS §7 P0-2): observed output tail, challenge
	// catalog and submission budget. It is scratch state for a code-level
	// guard, never a state authority, and it is never persisted.
	SubmitGuard *SubmitGuard

	// Tokens tracks per-request token attribution and post-scan efficiency metrics.
	Tokens *TokenTracker
	// ToolOutputs archives the complete raw output of every tool result so
	// aged tool-result messages can be replaced by retrieval stubs without
	// losing any information (bounded working context backing store).
	ToolOutputs *ToolArchive
	// Traffic archives the raw proxied HTTP exchanges (request/response
	// packets, unredacted per the 2026-11-22 operator decision) the traffic
	// proxy observed for this scan, so findings can display the exact wire
	// packets. The trafficproxy sink appends; the web layer reads the files
	// back after the context is gone (ReadTrafficExchanges).
	Traffic *TrafficArchive

	// ctx/cancel for the scan's lifecycle
	Ctx    context.Context
	Cancel context.CancelFunc

	policyMu          sync.RWMutex
	requestRatePolicy RequestRatePolicy
	targetsMu         sync.RWMutex
	targets           []string
	// targetBackoff is the target's own Retry-After cooldown (R1). It is set
	// when the target answers 429 with a Retry-After and read by every
	// target-facing tool before it sends anything. It is a run-scoped
	// instruction from the target, not engine state authority.
	backoffMu     sync.RWMutex
	backoffUntil  time.Time
	backoffReason string
	// egress is the Engagement-wide rate/concurrency/budget policy (R4). One
	// budget per ScanContext is shared by the root agent and every worker.
	egressMu sync.RWMutex
	egress   *EgressBudget

	// assetSeen dedupes observed-asset ingestion within one session. It is
	// per-scan scratch state, not an authority: the asset graph itself owns
	// identity. Bounded so a pathological target cannot grow it without limit.
	assetSeenMu sync.Mutex
	assetSeen   map[string]struct{}

	// submitGuardMu lazily materializes SubmitGuard on contexts that were not
	// built by New (test/legacy literals).
	submitGuardMu sync.Mutex
}

// EnsureSubmitGuard returns the scan's submission discipline ledger, creating
// it on demand. Nil for a nil context.
func (sc *ScanContext) EnsureSubmitGuard() *SubmitGuard {
	if sc == nil {
		return nil
	}
	sc.submitGuardMu.Lock()
	defer sc.submitGuardMu.Unlock()
	if sc.SubmitGuard == nil {
		sc.SubmitGuard = NewSubmitGuard()
	}
	return sc.SubmitGuard
}

// maxAssetSeen bounds the per-session observed-asset dedupe set. Once full,
// new identities are skipped for the rest of the session rather than growing
// unbounded.
const maxAssetSeen = 4096

// MarkAssetSeen reports whether (kind, key) is new for this session. False for
// a repeat, a nil context, or a full dedupe set.
func (sc *ScanContext) MarkAssetSeen(kind, key string) bool {
	if sc == nil {
		return false
	}
	k := strings.ToLower(strings.TrimSpace(kind)) + "\x00" + strings.ToLower(strings.TrimSpace(key))
	if k == "\x00" {
		return false
	}
	sc.assetSeenMu.Lock()
	defer sc.assetSeenMu.Unlock()
	if sc.assetSeen == nil {
		sc.assetSeen = make(map[string]struct{})
	}
	if _, ok := sc.assetSeen[k]; ok {
		return false
	}
	if len(sc.assetSeen) >= maxAssetSeen {
		return false
	}
	sc.assetSeen[k] = struct{}{}
	return true
}

// SetInstanceID stamps the D11 run identity. The authoritative dual graph is
// keyed by it; a re-dispatch/sibling session therefore shares the same plan.
func (sc *ScanContext) SetInstanceID(id string) {
	if sc != nil {
		sc.InstanceID = strings.TrimSpace(id)
	}
}

// New creates a fresh ScanContext for an isolated scan session.
func New(id, scanDir string) *ScanContext {
	ctx, cancel := context.WithCancel(context.Background())
	tokens := NewTokenTracker()
	if scanDir != "" {
		// Compact per-request token records + aggregate summary persist under
		// the scan directory so diagnostics survive completion and restarts.
		tokens.SetPersistDir(scanDir)
	}
	ledger := NewLedgerStore()
	ledger.SetScanID(id)
	if scanDir != "" {
		// Domain audit is separate from best-effort telemetry. Failure here
		// disables the journal for this process but must not prevent scanning;
		// JournalLastSeq/health surface the absence explicitly.
		_ = ledger.EnableJournal(scanDir)
	}
	// R6.1: the bounded state documents (notes, coverage) carry the same scan
	// identity as the ledger — their run_state_files rows are keyed by it and
	// the D11 continuity reads need it. Instance ids are stamped by the
	// session wiring (the ledger precedent, scan_session.SetInstanceID).
	notes := NewNoteStore()
	notes.SetScanID(id)
	coverage := NewCoverageStore()
	coverage.SetScanID(id)
	return &ScanContext{
		ID:          id,
		ScanDir:     scanDir,
		Vulns:       NewVulnStore(),
		Notes:       notes,
		Terminal:    NewTerminalState(),
		Browser:     NewBrowserState(),
		Ledger:      ledger,
		Coverage:    coverage,
		Progress:    NewProgressStore(),
		SubmitGuard: NewSubmitGuard(),
		Tokens:      tokens,
		ToolOutputs: NewToolArchive(scanDir),
		Traffic:     NewTrafficArchive(scanDir),
		Ctx:         ctx,
		Cancel:      cancel,
	}
}

// Close tears down all resources owned by this scan context.
// Safe to call multiple times.
//
// Each sub-step (cancel, Terminal.KillAll, Browser.Close) runs inside its
// own recovered closure so a panic in one step never prevents the others
// from running. The outer defer is a final safety net for anything not
// covered by the inner closures.
//
// Lease-conservation under panic (Task 13.3, Requirements 4.1 and 4.5):
// every Tool_Lease cached on this Scan_Context is released exactly once
// during Close, regardless of whether any earlier sub-step panics. The
// recovered-closure structure above is what carries this guarantee:
//
//   - The Cancel sub-step releases the scan's root context.WithCancel
//     funds — it never holds a Tool_Lease, but canceling here lets the
//     downstream subprocesses tracked by Terminal observe their context's
//     Done channel and exit cleanly before KillAll runs.
//
//   - The Terminal.KillAll sub-step is responsible for releasing every
//     per-process Tool_Lease acquired by terminal_execute and python_action
//     invocations. Each tool acquires its lease via
//     resources.AcquireToolLeaseContext and releases it in its own defer
//     when the subprocess exits or is killed, so KillAll's role here is to
//     trigger that exit path for any still-running child.
//
//   - The Browser.Close sub-step releases the browser Tool_Lease cached on
//     BrowserState. The release is idempotent (sync.Once on the browser
//     side), so a duplicate Close call from another shutdown path will
//     not double-release the lease.
//
// Because each sub-step lives in its own deferred-recover closure, a panic
// inside one step (for example, a corrupted cmd handle inside KillAll)
// produces a single recovery log line and lets the remaining steps run.
// That preserves the lease-conservation invariant: no cached lease is
// leaked, and none is released more than once, even when a panic occurs
// mid-shutdown.
func (sc *ScanContext) Close() {
	defer safe.Recover("scanctx.close", sc.ID)

	func() {
		defer safe.Recover("scanctx.close.cancel", sc.ID)
		if sc.Cancel != nil {
			sc.Cancel()
		}
	}()

	func() {
		defer safe.Recover("scanctx.close.terminal", sc.ID)
		if sc.Terminal != nil {
			sc.Terminal.KillAll()
		}
	}()

	func() {
		defer safe.Recover("scanctx.close.browser", sc.ID)
		if sc.Browser != nil {
			sc.Browser.Close()
		}
	}()
}

// SetRequestRatePolicy stores the scan-specific outbound request budget.
func (sc *ScanContext) SetRequestRatePolicy(policy RequestRatePolicy) {
	policy = NormalizeRequestRatePolicy(policy)
	sc.policyMu.Lock()
	defer sc.policyMu.Unlock()
	if current := sc.requestRatePolicy; current.Enabled() {
		if !policy.Enabled() || current.MaxRPS <= policy.MaxRPS {
			return
		}
	}
	sc.requestRatePolicy = policy
}

// RequestRatePolicy returns the current scan-specific outbound request budget.
func (sc *ScanContext) RequestRatePolicy() RequestRatePolicy {
	sc.policyMu.RLock()
	defer sc.policyMu.RUnlock()
	return sc.requestRatePolicy
}

// SetEgressPolicy installs (or replaces) the Engagement-wide egress budget.
// Workers share the ScanContext, so this is the single policy all execution
// faces consult. Policy changes are audited; existing counters reset.
func (sc *ScanContext) SetEgressPolicy(policy EgressPolicy) {
	if sc == nil || !policy.Enabled() {
		return
	}
	b := NewEgressBudget(policy, func(operation, summary string) {
		if sc.Ledger != nil {
			_ = sc.Ledger.AppendEngagementEvent(operation, summary)
		}
	})
	sc.egressMu.Lock()
	sc.egress = b
	sc.egressMu.Unlock()
}

// Egress returns the scan's shared budget (nil when no policy is installed).
func (sc *ScanContext) Egress() *EgressBudget {
	if sc == nil {
		return nil
	}
	sc.egressMu.RLock()
	defer sc.egressMu.RUnlock()
	return sc.egress
}

// SetTargetBackoff records the target's Retry-After cooldown. Until it expires,
// target-facing tools refuse to send (they return a soft output, not an error,
// so the tool circuit breaker is untouched).
func (sc *ScanContext) SetTargetBackoff(until time.Time, reason string) {
	if sc == nil {
		return
	}
	if until.IsZero() {
		return
	}
	sc.backoffMu.Lock()
	defer sc.backoffMu.Unlock()
	if until.After(sc.backoffUntil) {
		sc.backoffUntil = until
		sc.backoffReason = reason
	}
}

// ClearTargetBackoff ends the cooldown early (a real target success proves the
// target accepted traffic again).
func (sc *ScanContext) ClearTargetBackoff() {
	if sc == nil {
		return
	}
	sc.backoffMu.Lock()
	defer sc.backoffMu.Unlock()
	sc.backoffUntil = time.Time{}
	sc.backoffReason = ""
}

// TargetBackoff reports the remaining cooldown and its reason. active is false
// once the deadline has passed.
func (sc *ScanContext) TargetBackoff() (remaining time.Duration, reason string, active bool) {
	if sc == nil {
		return 0, "", false
	}
	sc.backoffMu.RLock()
	until, why := sc.backoffUntil, sc.backoffReason
	sc.backoffMu.RUnlock()
	if until.IsZero() {
		return 0, "", false
	}
	remaining = time.Until(until)
	if remaining <= 0 {
		return 0, why, false
	}
	return remaining, why, true
}

// TargetBackoffMessage returns the operator/model-facing rejection line for an
// active cooldown, or "" when traffic may proceed.
func (sc *ScanContext) TargetBackoffMessage() string {
	remaining, reason, active := sc.TargetBackoff()
	if !active {
		return ""
	}
	if reason == "" {
		reason = "target asked for a pause"
	}
	return fmt.Sprintf("[TARGET BACKOFF] %s; retry in %s. Target-rate limiting is respected: do not bypass it with a different tool, proxy or identity.",
		reason, remaining.Round(time.Second))
}

// SetTargets stores the root coordinator's declared targets for tools that
// need to salvage an omitted target field. Delegated agents share this context
// and must not replace the root target set with their narrower assignment.
func (sc *ScanContext) SetTargets(targets []string) {
	if sc == nil {
		return
	}
	clean := make([]string, 0, len(targets))
	for _, target := range targets {
		if target = strings.TrimSpace(target); target != "" {
			clean = append(clean, target)
		}
	}
	sc.targetsMu.Lock()
	sc.targets = clean
	sc.targetsMu.Unlock()
}

// Targets returns a defensive copy of the root coordinator's target set.
func (sc *ScanContext) Targets() []string {
	if sc == nil {
		return nil
	}
	sc.targetsMu.RLock()
	defer sc.targetsMu.RUnlock()
	return append([]string(nil), sc.targets...)
}

// ──────────────────────────────────────────────────────────
// Active context registry — allows tool packages to access
// their session's ScanContext without changing Execute signatures.
// ──────────────────────────────────────────────────────────

var (
	activeMu   sync.RWMutex
	activeCtxs = make(map[string]*ScanContext) // instanceID → ScanContext
	defaultCtx *ScanContext                    // fallback for CLI mode (single scan)
)

// Activate registers a ScanContext as the active context for its ID.
// Also sets it as the default if no default exists (CLI mode compat).
func Activate(sc *ScanContext) {
	activeMu.Lock()
	defer activeMu.Unlock()
	activeCtxs[sc.ID] = sc
	if defaultCtx == nil {
		defaultCtx = sc
	}
}

// Deactivate removes a ScanContext from the active registry.
func Deactivate(id string) {
	activeMu.Lock()
	defer activeMu.Unlock()
	delete(activeCtxs, id)
	if defaultCtx != nil && defaultCtx.ID == id {
		defaultCtx = nil
		// Promote any remaining context as default
		for _, sc := range activeCtxs {
			defaultCtx = sc
			break
		}
	}
}

// Get returns the ScanContext for a given instance ID.
func Get(id string) *ScanContext {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return activeCtxs[id]
}

// Default returns the default (CLI-mode) ScanContext.
// If no context is active, creates and returns a temporary one.
//
// IMPORTANT: From the web server, callers MUST resolve the context via
// the agent/session that owns the request (registry.GetScanContextID,
// session.sctx, etc). Reaching Default() from a web-mode goroutine
// indicates a wiring bug — a tool would land in the shared CLI bucket
// where state from one scan would leak into another. The fallback creation
// log line is *the* signal that this regression has happened; grep for
// "[scanctx] Created fallback CLI context" in logs after a deploy.
func Default() *ScanContext {
	activeMu.RLock()
	if defaultCtx != nil {
		defer activeMu.RUnlock()
		return defaultCtx
	}
	activeMu.RUnlock()

	// Create a fallback for CLI mode. Capture a short caller stack so a bug
	// where web code accidentally drops the contextID is easy to root-cause.
	var callerInfo string
	pcs := make([]uintptr, 8)
	if n := runtime.Callers(2, pcs); n > 0 {
		frames := runtime.CallersFrames(pcs[:n])
		var b strings.Builder
		for {
			f, more := frames.Next()
			fmt.Fprintf(&b, "\n  %s\n    %s:%d", f.Function, f.File, f.Line)
			if !more {
				break
			}
		}
		callerInfo = b.String()
	}

	activeMu.Lock()
	defer activeMu.Unlock()
	if defaultCtx == nil {
		defaultCtx = New("cli-default", "")
		activeCtxs[defaultCtx.ID] = defaultCtx
		log.Printf("[scanctx] Created fallback CLI context (legitimate in CLI mode; in web mode this is a wiring bug):%s", callerInfo)
	}
	return defaultCtx
}

// ActiveCount returns the number of active scan contexts.
func ActiveCount() int {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return len(activeCtxs)
}

// ──────────────────────────────────────────────────────────
// Summary / debug
// ──────────────────────────────────────────────────────────

// Summary returns a human-readable summary of all active scan contexts.
func Summary() string {
	activeMu.RLock()
	defer activeMu.RUnlock()
	if len(activeCtxs) == 0 {
		return "No active scan contexts"
	}
	s := fmt.Sprintf("%d active scan context(s):\n", len(activeCtxs))
	for id, sc := range activeCtxs {
		vulnCount := sc.Vulns.Count()
		noteCount := sc.Notes.Count()
		procCount := sc.Terminal.ActiveProcessCount()
		s += fmt.Sprintf("  [%s] dir=%s vulns=%d notes=%d procs=%d\n",
			id, sc.ScanDir, vulnCount, noteCount, procCount)
	}
	return s
}

// ResetAll tears down all active contexts. Used for testing.
func ResetAll() {
	activeMu.Lock()
	defer activeMu.Unlock()
	for _, sc := range activeCtxs {
		sc.Close()
	}
	activeCtxs = make(map[string]*ScanContext)
	defaultCtx = nil
}
