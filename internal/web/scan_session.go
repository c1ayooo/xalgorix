package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/egress"
	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/orchestrator"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/notes"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// workerKillWithReason is an optional root-agent capability. The planner's
// kill_work path prefers it so the validated reason code reaches the worker
// audit trail; roots without it fall back to KillWorker.
type workerKillWithReason interface {
	KillWorkerWithReason(unitID, reasonCode, reason string) error
}

// vulnNotificationDetails renders the Markdown body pushed to Telegram/Discord
// for a reported vulnerability. It includes the concrete exploitation evidence
// (proof + verification method + verified/manual-review status) that
// report_vulnerability's gates guarantee for actionable findings — without it
// every alert reads like an unsubstantiated claim and gets treated as a false
// positive (issue #429). Fields are written only when present, mirroring the
// PDF report's evidence section.
// vulnDigestList renders a compact, severity-ranked findings digest for
// summary notifications (scan-finished cards). Per-finding alerts carry the
// full detail; the summary must still name WHAT was found — a count alone
// tells the operator nothing (operator feedback 2026-11-23: "实际发给飞书
// 的内容不带漏洞信息").
func vulnDigestList(vulns []VulnSummary, limit int) string {
	if len(vulns) == 0 {
		return ""
	}
	if limit <= 0 || limit > len(vulns) {
		limit = len(vulns)
	}
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "info": 4}
	icon := map[string]string{"critical": "🔴", "high": "🟠", "medium": "🟡", "low": "🔵", "info": "⚪"}
	sorted := append([]VulnSummary(nil), vulns...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, riOK := rank[strings.ToLower(sorted[i].Severity)]
		rj, rjOK := rank[strings.ToLower(sorted[j].Severity)]
		if !riOK {
			ri = 9
		}
		if !rjOK {
			rj = 9
		}
		if ri != rj {
			return ri < rj
		}
		return sorted[i].Title < sorted[j].Title
	})
	var b strings.Builder
	b.WriteString("**Findings:**\n")
	for _, v := range sorted[:limit] {
		sev := strings.ToLower(v.Severity)
		em := icon[sev]
		if em == "" {
			em = "•"
		}
		b.WriteString(fmt.Sprintf("- %s [%s] %s", em, strings.ToUpper(v.Severity), v.Title))
		if v.Endpoint != "" {
			b.WriteString(fmt.Sprintf(" — `%s`", v.Endpoint))
		}
		if v.CVSS > 0 {
			b.WriteString(fmt.Sprintf(" (CVSS %.1f)", v.CVSS))
		}
		b.WriteString("\n")
	}
	if rest := len(sorted) - limit; rest > 0 {
		b.WriteString(fmt.Sprintf("- … and %d more (see the dashboard report)\n", rest))
	}
	return b.String()
}

func vulnNotificationDetails(vs VulnSummary) string {
	var details strings.Builder
	details.WriteString(fmt.Sprintf("**%s**\n\n", vs.Title))
	if vs.Description != "" {
		details.WriteString(fmt.Sprintf("📝 **Description:**\n%s\n\n", vs.Description))
	}
	if vs.Endpoint != "" {
		details.WriteString(fmt.Sprintf("🔗 **Endpoint:** `%s`\n", vs.Endpoint))
	}
	if vs.Method != "" {
		details.WriteString(fmt.Sprintf("📡 **Method:** `%s`\n", vs.Method))
	}
	if vs.CVE != "" {
		details.WriteString(fmt.Sprintf("🏷️ **CVE:** `%s`\n", vs.CVE))
	}
	details.WriteString(fmt.Sprintf("📊 **CVSS:** `%.1f` | **Severity:** `%s`\n\n", vs.CVSS, strings.ToUpper(vs.Severity)))
	if vs.Impact != "" {
		details.WriteString(fmt.Sprintf("💥 **Impact:**\n%s\n\n", vs.Impact))
	}
	if vs.TechnicalAnalysis != "" {
		details.WriteString(fmt.Sprintf("🔬 **Technical Analysis:**\n%s\n\n", vs.TechnicalAnalysis))
	}
	if vs.PoCDescription != "" {
		details.WriteString(fmt.Sprintf("🧪 **PoC:**\n%s\n", vs.PoCDescription))
	}
	if vs.PoCScript != "" {
		details.WriteString(fmt.Sprintf("```\n%s\n```\n\n", truncateForNotification(vs.PoCScript, 800)))
	}
	// Exploitation evidence — the concrete proof (extracted data, command
	// output like `uid=0(root)`, reflected payload) that distinguishes a real
	// finding from an unsubstantiated claim. This is the fix for issue #429.
	if vs.ExploitationProof != "" {
		details.WriteString(fmt.Sprintf("🔓 **Exploitation Proof:**\n```\n%s\n```\n\n", truncateForNotification(vs.ExploitationProof, 800)))
	}
	if vs.VerificationMethod != "" {
		status := "unverified — needs manual review"
		if vs.Verified {
			status = "verified — independently reproduced"
		}
		details.WriteString(fmt.Sprintf("✅ **Verified via:** `%s` (%s)\n\n", vs.VerificationMethod, status))
	}
	if vs.Remediation != "" {
		details.WriteString(fmt.Sprintf("🛡️ **Remediation:**\n%s", vs.Remediation))
	}
	return details.String()
}

// truncateForNotification caps a code/proof block at maxBytes without splitting
// a multi-byte UTF-8 rune (so the notification never carries invalid bytes),
// appending a truncation marker when it trims.
func truncateForNotification(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	// Back up to a rune boundary.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n... (truncated)"
}

// shouldFailInstanceOnAbort reports whether an LLM-abort (no-tool / empty /
// repeated-error / rate-limit) in THIS session may mark the whole scan
// instance failed.
//
// Only a top-level single/DAST scan qualifies. A wildcard scan runs a discovery
// session plus one session per subdomain, and every one of them shares the
// parent instance ID: Phase 1 discovery normally ends with a no-tool abort
// after it has already enumerated the subdomains, and a single subdomain
// session can abort while hundreds more are still queued. Failing the instance
// from any wildcard sub-session marks a scan that is still actively running as
// "failed" (and wrongly refunds it). Wildcard parent status is decided by the
// orchestrator, so those sessions must fall through to the normal finalize.
func shouldFailInstanceOnAbort(scanMode, parentTarget string, discoveryMode bool) bool {
	if strings.EqualFold(strings.TrimSpace(scanMode), "wildcard") {
		return false
	}
	if discoveryMode {
		return false
	}
	if strings.TrimSpace(parentTarget) != "" {
		return false
	}
	return true
}

// registerSessionAgent is the single admission transition shared with exact
// stop. If registration wins, stop sees and stops this agent. If stop wins,
// status is already terminal and no session can be published or run afterward.
func (s *Server) registerSessionAgent(sess *scanSession, sctx *scanctx.ScanContext, agnt *agent.Agent) bool {
	if sess.instanceID != "" {
		// Exact stop and work admission share dispatchMu. If admission wins, its
		// positive evidence is durable before any target work starts. If stop
		// wins, the terminal status is visible before this session can publish.
		s.dispatchMu.Lock()
		s.instancesMu.RLock()
		inst := s.instances[sess.instanceID]
		if inst == nil {
			s.instancesMu.RUnlock()
			s.dispatchMu.Unlock()
			return false
		}
		inst.mu.Lock()
		canceled := false
		if sess.parentCtx != nil {
			select {
			case <-sess.parentCtx.Done():
				canceled = true
			default:
			}
		}
		if inst.Status != "running" || canceled {
			inst.mu.Unlock()
			s.instancesMu.RUnlock()
			s.dispatchMu.Unlock()
			return false
		}
		inst.sctx = sctx
		inst.agent = agnt
		inst.scanDir = sess.scanDir
		inst.WorkStarted = true
		inst.mu.Unlock()
		s.instancesMu.RUnlock()

		// Positive work admission must survive a process restart before any
		// source preparation or target activity begins. dispatchMu remains held,
		// so exact stop cannot be overwritten by this running snapshot.
		if err := s.persistExactInstanceSnapshotLocked(inst); err != nil {
			inst.mu.Lock()
			if inst.Status == "running" {
				inst.Status = "failed"
				inst.StopReason = "dispatch_evidence_persist_failed"
				inst.FinishedAt = time.Now().Format(time.RFC3339Nano)
				inst.WorkStarted = false
			}
			inst.mu.Unlock()
			s.dispatchMu.Unlock()
			log.Printf("[scan] refusing session %s: could not persist work admission: %v", sess.id, err)
			return false
		}
		s.dispatchMu.Unlock()
	}

	s.mu.Lock()
	s.currentScanDir = sess.scanDir
	s.currentScanID = sess.id
	s.currentAgents[sess.id] = agnt
	s.mu.Unlock()
	return true
}

// backfillSessionTaskCoordinates repairs a resumed session whose queue state
// predates task-coordinate persistence (or whose resume path did not carry
// them). It only fills missing values and only for runs mounted on a task, so
// ad-hoc /api/scan runs keep their empty project scope.
func (s *Server) backfillSessionTaskCoordinates(sess *scanSession, sctx *scanctx.ScanContext) {
	if sess == nil || sctx == nil || strings.TrimSpace(sctx.ProjectID) != "" {
		return
	}
	instanceID := strings.TrimSpace(sess.instanceID)
	store := s.currentPlatformStore()
	if instanceID == "" || store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runs, err := store.RunsByInstanceID(ctx, instanceID)
	if err != nil || len(runs) == 0 {
		return
	}
	task, err := store.GetTask(ctx, runs[0].TaskID)
	if err != nil || task == nil {
		return
	}
	if projectID := strings.TrimSpace(task.ProjectID); projectID != "" {
		sctx.ProjectID = projectID
		if strings.TrimSpace(sess.objectiveProjectID) == "" {
			sess.objectiveProjectID = projectID
		}
	}
	if sctx.TaskID == "" {
		sctx.TaskID = task.ID
	}
	if strings.TrimSpace(sess.objectiveTaskID) == "" {
		sess.objectiveTaskID = task.ID
	}
	if strings.TrimSpace(sess.auditMode) == "" {
		sess.auditMode = effectiveTaskAuditMode(task, s.cfg)
		sctx.AuditMode = strings.TrimSpace(sess.auditMode)
		// Task-level outbound proxy pin: snapshotted for the run like audit_mode.
		// The MITM upstream selector reads this per exchange, so a task can use a
		// different pool entry (or explicit direct egress) without changing the
		// process-wide default. An unresolvable entry was rejected at dispatch.
		if pin := strings.TrimSpace(sess.proxyPin); pin != "" {
			switch pin {
			case "direct":
				proxy.SetScanUpstream(sctx.ID, proxy.ScanUpstream{Direct: true})
			default:
				if idx, err := strconv.Atoi(pin); err == nil {
					if entry := proxy.EntryAt(idx); entry != nil {
						proxy.SetScanUpstream(sctx.ID, proxy.ScanUpstream{Entry: entry})
					}
				}
			}
		}
	}
	if sess.objective == nil && task.Objective != nil {
		sess.objective = task.Objective
	}
}

// executeScanSession runs a single scan in complete isolation.
// It NEVER panics upward — all panics are caught and logged.
func (s *Server) executeScanSession(sess *scanSession) {
	// IRONCLAD: This function NEVER panics upward.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[CRITICAL] scanSession %s panicked: %v\n%s", sess.id, r, debug.Stack())
			if sess.instanceID != "" {
				s.broadcastToInstance(sess.instanceID, WSEvent{Type: "error", Content: fmt.Sprintf("⛔ Scan %s crashed: %v — continuing", sess.target, r)})
			} else {
				s.broadcast(WSEvent{Type: "error", Content: fmt.Sprintf("⛔ Scan %s crashed: %v — continuing", sess.target, r)})
			}
		}
		// ALWAYS clean up, whether normal exit or panic
		sess.cleanup()
	}()

	// 0. Create and activate a per-session ScanContext for isolation.
	//    This must happen BEFORE any tool state is touched.
	sctx := scanctx.New(sess.id, sess.scanDir)
	// Production runs enforce the wire-evidence requirement: a non-info finding
	// must name the captured packet (or an archived tool output) that proves it.
	sctx.RequireEvidenceRefs = true
	// Stamp the run identity on the shared context. The authoritative dual
	// graph is keyed by this identity, so a re-dispatch/sibling session sees
	// the same plan instead of an empty per-session one (R7/M1).
	sctx.SetInstanceID(sess.instanceID)
	sctx.ProjectID = strings.TrimSpace(sess.objectiveProjectID)
	sctx.TaskID = strings.TrimSpace(sess.objectiveTaskID)
	// The task's HITL switch is snapshotted for the whole session: the agent's
	// tool gate and the MITM wire gate read this one field, so a task with
	// audit=enforce is gated end-to-end even when the process default is off.
	sctx.AuditMode = strings.TrimSpace(sess.auditMode)
	// Defensive resume repair: queue states written by older binaries (and
	// ad-hoc resume paths) may carry no project/task coordinates. Without them
	// the session is project-less, which silently disables project-scoped
	// features (asset ingestion, cross-scan cold-path whitelist, objective
	// projection) and drops the task's HITL posture. Fill from the run's task
	// when the run is mounted on one; ad-hoc /api/scan runs have no run row and
	// stay unchanged.
	s.backfillSessionTaskCoordinates(sess, sctx)
	// Stamp the run identity on the ledger (D11): with the PG backend the
	// snapshot carries it, and a re-dispatched session's first ledger load
	// inherits the previous sibling session's chain (charter v3.0 §6.3).
	sctx.Ledger.SetInstanceID(sess.instanceID)
	// 同一批 D11 连续性盖章覆盖 notes/coverage/intelligence 三族（R6.1 三快
	// 照片）：后端模式下快照带 instance，续跑首读继承上一会话的文档。
	sctx.Notes.SetInstanceID(sess.instanceID)
	sctx.Coverage.SetInstanceID(sess.instanceID)
	intelligence.SetInstanceIDForContext(sctx.ID, sess.instanceID)
	// A crashed/restarted session must not leave phantom claims behind. The
	// graph is the authority, so reset this run's running intents to open
	// before the agent loop reads the frontier.
	if repo := graph.Backend(); repo != nil {
		runID := strings.TrimSpace(sctx.InstanceID)
		if runID == "" {
			runID = sctx.ID
		}
		resetCtx, resetCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if n, err := repo.ResetRunning(resetCtx, runID); err != nil {
			log.Printf("[graph] reset stale running units for %s failed: %v", runID, err)
		} else if n > 0 {
			log.Printf("[graph] reset %d stale running units for %s", n, runID)
		}
		resetCancel()
	}
	// Link the platform run to the scan-data identity. Runs are mounted at
	// dispatch time, before this directory exists, so without this write
	// ProjectScanIDs never learns the id the domain journal and the InfoItem
	// replica are keyed by — and /api/info/* returns an empty region for a
	// project whose scans did produce information.
	if sess.instanceID != "" && sess.id != "" {
		if store := s.currentPlatformStore(); store != nil {
			linkCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := store.SetRunLegacyScanID(linkCtx, sess.instanceID, sess.id); err != nil {
				log.Printf("[platform] link run %s to scan %s failed: %v", sess.instanceID, sess.id, err)
			}
			cancel()
		}
	}
	cfg := s.configSnapshot()
	if cfg != nil {
		sctx.Ledger.SetVOIWeight(cfg.VOIWeight)
	}
	// Tools are registered against sctx.ID; the egress boundary must use the
	// same key or terminal/Python/browser lookups silently miss the enforce
	// status and skip credential drops / fail-closed checks.
	egressID := sctx.ID
	egressStatus, err := egress.PrepareScan(sctx.Ctx, egressID)
	if err != nil {
		log.Printf("[egress] refusing scan %s: %v", egressID, err)
		sess.abortReason = "egress_isolation_unavailable"
		return
	}
	defer egress.Forget(egressID)
	if !egressStatus.Enforced {
		log.Printf("[egress] scan %s: %s (OS-level isolation unmanaged)", egressID, egressStatus.Reason)
	}
	// Resume continuity: reload compact per-request token records persisted
	// before a restart so attribution aggregates continue across restarts.
	sctx.Tokens.LoadPersisted()
	scanctx.Activate(sctx)
	sess.sctx = sctx
	log.Printf("[scanctx] Activated context %s for target %s (dir=%s)", sctx.ID, sess.target, sess.scanDir)

	// R2b traffic-proxy attribution (REDESIGN §4/D4): mint a fresh 128-bit
	// run token for this session (never the session id — that embeds the
	// target name and is not a secret) and register it so the loopback proxy
	// can attribute observed traffic back to this Run. HTTP tools stamp the
	// token onto their requests; the proxy resolves it before stripping
	// hop-by-hop headers. Unregistered in sess.cleanup().
	if s.cfg != nil {
		sess.runToken = randomSlug()
		trafficproxy.RegisterRun(sess.runToken, sess.instanceID, sctx.ID, scopeguard.Config{
			BindAddr:           cfg.BindAddr,
			Port:               s.port,
			AllowLoopbackPorts: sess.allowLoopbackPorts,
			AllowLocalTargets:  cfg.AllowLocalTargets,
		})
	}

	// Panic-safe persistence: register the child→parent reporting mapping so
	// reporting.PromoteToParent runs incrementally on every report_vulnerability
	// call. CleanupContext clears the mapping on session teardown.
	if sess.parentReportingCtxID != "" {
		reporting.SetParentContext(sctx.ID, sess.parentReportingCtxID)
	}

	// Parent instance publication happens atomically with agent admission below;
	// publishing the context earlier would reopen a stop-before-agent race.

	// 1. Reset per-context state if requested (context-aware)
	if sess.resetState {
		func() {
			defer logRecover("session.resetContextState")
			reporting.ResetVulnerabilitiesForContext(sctx.ID)
			notes.ResetNotesForContext(sctx.ID)
			intelligence.ResetForContext(sctx.ID)
		}()
	}

	// 1b. Configure notes disk persistence → saves notes.json in scan directory
	notes.SetPersistPathForContext(sctx.ID, sess.scanDir)
	intelligence.SetPersistPathForContext(sctx.ID, sess.scanDir)
	if !sess.resetState {
		// Resume scenario: load previously saved notes from disk
		if _, err := notes.LoadFromDiskForContext(sctx.ID); err != nil {
			// A failed restore must be visible, not silently equal to an empty
			// note set: the resumed session would proceed without the durable
			// notes its predecessor saved. Absent snapshots (fresh context) and
			// corrupt snapshots are logged distinctly.
			if errors.Is(err, os.ErrNotExist) {
				log.Printf("[RESUME] %s: expected notes snapshot is absent (fresh context or cleared scan dir) — continuing without restored notes", sctx.ID)
			} else {
				log.Printf("[RESUME] %s: %v — continuing with in-memory notes only", sctx.ID, err)
			}
		}
		if _, err := intelligence.LoadFromDiskForContext(sctx.ID); err != nil {
			log.Printf("[RESUME] %s: %v — continuing with in-memory intelligence only", sctx.ID, err)
		}
	}

	// 1c. Configure hypothesis/evidence ledger persistence → ledger.json.
	// The ledger lives on the ScanContext and is shared by the coordinator and
	// every delegated specialist, so persisting it makes the whole hypothesis
	// graph durable across restart/resume (not just findings and notes).
	sctx.Ledger.SetPersistPath(sess.scanDir)
	if sess.resetState {
		sctx.Ledger.Reset()
	} else {
		// Resume scenario: reload the durable hypothesis/evidence graph.
		sctx.Ledger.LoadFromDisk()
	}

	// 1d. The operator objective enters the information layer as a
	// human-sourced observation (charter 2/3.4): operator intent is an
	// observed InfoItem, never a verdict. A missing journal must never
	// stop the scan - the objective stays in the prompt either way.
	if sess.objective != nil && sctx.Ledger != nil {
		item := scanctx.NewObjectiveInfoItem(scanctx.ObjectiveObservation{
			ID:        objectiveInfoItemID(sess),
			ProjectID: sess.objectiveProjectID,
			TaskID:    sess.objectiveTaskID,
			Statement: sess.objective.Statement,
			Criteria:  objectiveCriterionLines(sess.objective),
		})
		if err := sctx.Ledger.AppendObjectiveObservation(&item, "operator objective recorded"); err != nil {
			log.Printf("[objective] %s: journal append failed: %v", sess.id, err)
		}
	}

	// 2. Set working directory (context-aware)
	sctx.Terminal.SetWorkDir(sess.scanDir)
	sctx.Browser.SetSessionPath(sess.scanDir)

	// 3. Create agent with session's config AND ScanContext.
	// When the per-scan code path supplied a pre-resolved llm.Client
	// (B1: provider_profile-aware endpoint), thread it through
	// agent.WithLLMClient so the agent's outbound traffic actually
	// uses the operator's chosen credentials. A nil llmClient falls
	// back to the agent's default llm.NewClient(cfg) construction,
	// preserving the legacy behavior for tests / CLI / call sites
	// that have not opted in.
	events := make(chan agent.Event, 512)
	sess.events = events
	agentOpts := []any{sctx}
	if sess.llmClient != nil {
		agentOpts = append(agentOpts, agent.WithLLMClient(sess.llmClient))
	}
	// Operator-authored user skills ride into the per-scan skill state
	// (best-effort: no store / store error = corpus-only scan, charter §6.6).
	if userSkills := s.scanUserSkills(); len(userSkills) > 0 {
		agentOpts = append(agentOpts, agent.WithUserSkills(userSkills))
	}
	// Operator-authored delegation profiles merge behind the builtin
	// specialists (same best-effort posture).
	if profiles := s.scanAgentProfiles(); len(profiles) > 0 {
		agentOpts = append(agentOpts, agent.WithAgentProfiles(profiles))
	}
	// Operator-registered MCP servers bridge their tools into the registry
	// at Run start (connection failures skip the server, never the scan).
	if mcpSpecs := s.scanMcpServers(); len(mcpSpecs) > 0 {
		agentOpts = append(agentOpts, agent.WithMcpServers(mcpSpecs))
	}
	// Project engagement scope (REDESIGN D7): an explicitly enforced fence
	// narrows every network destination this run — and every delegated worker —
	// may touch. A project without an enforced fence, and every benchmark /
	// control-plane run, keeps the pre-scope behavior byte-for-byte; the option
	// is only installed when the fence opts in.
	var engagementScope *agent.EngagementScope
	if projectID := strings.TrimSpace(sctx.ProjectID); projectID != "" {
		if store := s.currentPlatformStore(); store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			project, err := store.GetProject(ctx, projectID)
			cancel()
			switch {
			case err != nil:
				log.Printf("[scope] %s: project %s unavailable — engagement scope not enforced: %v", sess.instanceID, projectID, err)
			case project != nil && project.Scope.Fence != nil && project.Scope.Fence.Enforce:
				engagementScope = &agent.EngagementScope{
					Targets:    append([]string(nil), project.Scope.Targets...),
					Deny:       append([]string(nil), project.Scope.Fence.DenyTargets...),
					AllowPorts: append([]int(nil), project.Scope.AllowPorts...),
				}
				log.Printf("[scope] %s: enforced project scope: %d target(s), %d denied, %d allowed port(s)",
					sess.instanceID, len(engagementScope.Targets), len(engagementScope.Deny), len(engagementScope.AllowPorts))
			}
		}
	}
	if engagementScope != nil {
		agentOpts = append(agentOpts, agent.WithEngagementScope(engagementScope.Targets, engagementScope.Deny, engagementScope.AllowPorts))
	}
	var scopeTargets, scopeDeny []string
	var scopePorts []int
	if engagementScope != nil {
		scopeTargets = engagementScope.Targets
		scopeDeny = engagementScope.Deny
		scopePorts = engagementScope.AllowPorts
	}
	// HITL approval is an execution-time obligation implemented by the Eino
	// interrupt/resume protocol. The broker binds the agent's secret registry
	// after construction (HIGH-1: by-value payload redaction before
	// persistence — secrets register during Run's prepareScanEnvironment).
	databaseURL := ""
	if cfg != nil {
		databaseURL = cfg.DatabaseURL
	}
	store, err := engine.OpenCheckpointStore(context.Background(), s.dataDir, databaseURL)
	if err != nil {
		// PG is a cold-path capability: a database outage must not disable
		// scanning. Fall back to the atomic file checkpoint, loudly.
		log.Printf("[eino] PostgreSQL checkpoint unavailable for %s: %v — using file fallback", sess.instanceID, err)
		store, err = engine.NewFileCheckpointStore(s.dataDir)
		if err != nil {
			log.Printf("[eino] file checkpoint unavailable for %s: %v", sess.instanceID, err)
			sess.abortReason = "eino_checkpoint_unavailable"
			return
		}
	}
	defer engine.CloseCheckpointStore(store)
	// The broker constructor returns a typed nil when approvals are off; assigning
	// it straight into engine.ApprovalBroker makes a non-nil interface holding a
	// nil pointer, which defeats ApprovalTool's `broker == nil` guard and panics
	// on the first approval-classified tool call. Keep the interface nil instead.
	var einoBroker engine.ApprovalBroker
	var brokerOpts *einoApprovalBroker
	if b := s.einoApprovalBroker(sess.instanceID, sctx); b != nil {
		einoBroker = b
		brokerOpts = b
	}
	agentOpts = append(agentOpts, agent.WithRuntime(store, einoBroker))
	// R7/M2: planner_worker is the default built-in topology (config.go
	// default). The root agent becomes the main observer/controller (no
	// attack tools); workers get the execution surface. Legacy is a hidden
	// emergency rollback only, not offered on the settings page.
	plannerWorker := false
	workerCount := 3
	if sess.cfg != nil {
		plannerWorker = strings.EqualFold(strings.TrimSpace(sess.cfg.Orchestrator), "planner_worker")
		workerCount = sess.cfg.WorkerConcurrency
	}
	// Hot setting: read the env file at scan start so a settings-page save or
	// an operator edit applies to the next scan without a server restart.
	workerCount = config.LiveWorkerConcurrency(workerCount)
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > 10 {
		workerCount = 10
	}
	// Without the graph authority the pool has nothing to claim; stay legacy.
	if graph.Backend() == nil {
		plannerWorker = false
	}
	if plannerWorker {
		agentOpts = append(agentOpts,
			agent.WithToolSurface(agent.SurfaceMain),
			agent.WithWorkerSurface(agent.SurfaceWorker),
			agent.WithWorkerPoolSize(workerCount),
			agent.WithPlannerWorkerMode(),
		)
	}
	agnt := agent.NewAgent(sess.cfg, "XalgorixAgent", events, scopeguard.Config{
		BindAddr:           cfg.BindAddr,
		Port:               s.port,
		AllowLoopbackPorts: sess.allowLoopbackPorts,
		AllowLocalTargets:  cfg.AllowLocalTargets,
	}, agentOpts...)
	// Bind the operator-secret registry into the approval adapter (HIGH-1):
	// approval payloads are by-value redacted with the same credentials the
	// event stream redacts, before they reach the store, WS stream, or API.
	if brokerOpts != nil {
		brokerOpts.BindSecrets(agnt.SecretValues)
	}
	agnt.SetPhaseRestrictions(sess.phases)
	// read_info_item cross-scan detail is model-controlled, so the tool asks
	// this resolver before reading another scan's cold-path item. Only scans
	// belonging to the same project (D7) are accepted.
	agnt.SetCrossScanResolver(func(target string) bool {
		target = strings.TrimSpace(target)
		if target == "" || target == sctx.ID || target == sctx.InstanceID {
			return true
		}
		store := s.currentPlatformStore()
		if store == nil || strings.TrimSpace(sctx.ProjectID) == "" {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ids, err := store.ProjectScanIDs(ctx, sctx.ProjectID)
		if err != nil {
			return false
		}
		for _, id := range ids {
			if id == target {
				return true
			}
		}
		return false
	})
	// The operator objective (REDESIGN 3.1) is intent for the loop: the
	// engine marks the plan tasks that serve a criterion, and the objective
	// statement travels in its own prompt section. It never changes what
	// counts as a finding. Since R-goal (REDESIGN D10) unmet required
	// criteria DO close the loop's exits: the finish gate refuses voluntary
	// finish and the plan-complete tail defers (internal/agent/goal_gate.go).
	if spec := agentObjectiveSpec(sess.objective); spec != nil {
		agnt.SetObjective(spec)
	}
	agnt.SetActivityPolicy(sess.reconMode, sess.scanIntensity, []string{sess.target, sess.parentTarget})
	if sess.discoveryMode || isReconReportOnlyPhaseSelection(sess.phases) {
		agnt.SetDiscoveryMode(true)
	}
	// A single-target scan is that host only; wildcard scans discover and
	// cover subdomains themselves.
	agnt.SetSingleHostScope(sess.scanMode == "single")
	// Per-scan authenticated scanning and whitebox source. Empty values are
	// no-ops; non-empty values override the agent's cfg-derived defaults so
	// each scan can carry its own credentials/source without leaking across
	// sessions. SetSourceRepo only records intent — the clone/open happens
	// lazily inside Run via prepareScanEnvironment.
	if sess.targetAuth != "" {
		agnt.SetTargetAuth(sess.targetAuth)
	}
	if sess.targetAuthB != "" {
		agnt.SetTargetAuthSecondary(sess.targetAuthB)
	}
	if sess.sourceRepo != "" {
		agnt.SetSourceRepo(sess.sourceRepo)
	}
	if len(sess.scanContextPaths) > 0 {
		agnt.SetScanContexts(sess.scanContextPaths)
	} else if sess.scanContext != "" {
		agnt.SetScanContext(sess.scanContext)
	}
	if sess.codeScanMode != agent.CodeScanNone {
		agnt.SetCodeScanMode(sess.codeScanMode)
	}
	sess.agent = agnt
	if !s.registerSessionAgent(sess, sctx, agnt) {
		// Exact stop, deletion, or parent cancellation won before admission.
		// Stop-before-Run is safe because Agent.Run checks the stopped flag before
		// preparing source or touching the target.
		agnt.Stop()
		return
	}
	// R7 main chat: a human turn must be answered, not silently folded into the
	// instruction. Guidance that arrived while the run was starting (the normal
	// "open a conversation" flow sends the first message right after dispatch)
	// is still queued in the platform store; deliver it to the main agent now
	// that the instance can receive messages. In legacy mode guidance keeps the
	// merge-into-briefing semantics.
	if plannerWorker {
		s.deliverQueuedGuidanceToMain(sess, agnt)
	}

	// 4. Initialize scan record. Resume paths preserve previously persisted
	// events, vulnerabilities, counters, and sub-scan progress.
	sess.record = s.scanRecordForSession(sess)
	if err := prepareScanEventJournal(sess.scanDir, sess.record, sess.resetState); err != nil {
		sess.abortReason = "checkpoint_write_failed"
		log.Printf("[checkpoint] event journal unavailable: %v", err)
		agnt.Stop()
		return
	}
	s.saveScanRecordTo(sess.record, sess.scanDir)

	if !sess.resetState && sess.record != nil {
		var restoredFindings []reporting.Vulnerability
		for _, summary := range sess.record.Vulns {
			restoredFindings = append(restoredFindings, vulnFromSummary(summary))
		}
		agnt.SetResumeFindings(restoredFindings)
		agnt.SetResumeBudget(sess.record.Iterations, sess.record.ToolCalls, sess.record.TotalTokens, sess.record.StartedAt)
		// Emitted budget totals now include the already-spent lifetime.
		sess.recordTokenOffset = 0
		if sess.record.DelegationDeferNoted {
			// The full-detail specialist-wave defer note already displayed
			// before the restart; resumed defer notes stay compact.
			agnt.SetDeferNoteDetailEmitted()
		}
		// Hot-resume ladder L2 (REDESIGN 2026-11-22): seed the session with a
		// bounded projection of the previous conversation. Prompt context
		// only — the state authority remains scan.json + notes + ledger
		// (charter §6). Falls back to the L3 briefing alone when the journal
		// is absent.
		replayDir := sess.redispatchFromDir
		if replayDir == "" {
			replayDir = sctx.ScanDir
		}
		// R6.2 (REDESIGN D12 ⑤): the replay reads the run's whole durable
		// sibling history (instance id), the session's legacy file otherwise.
		if transcript := conversationReplayTranscriptFromDir(replayDir, sess.instanceID); transcript != "" {
			agnt.SetConversationReplay(transcript)
		}
		// 1:1 re-open (REDESIGN D11): a finished record under an explicit
		// re-dispatch means the operator re-opened the run — suppress the
		// plan-complete auto-finalize the restored checkpoint would re-fire,
		// and re-open the record instead of instantly re-finishing.
		log.Printf("[REOPEN] record status=%q resetState=%v — re-open branch reached", sess.record.Status, sess.resetState)
		if sess.record.Status == "finished" {
			// The engine checkpoint for this instance (ID = scan dir basename)
			// was saved by the previous session's cancel — rt.Run with that
			// checkpoint ID would RESUME the already-finished graph and end
			// instantly (observed 18:06/18:13). A re-open deliberately starts
			// a fresh ADK run; continuity comes from the replay/briefing.
			dropStaleEngineCheckpoints(s.dataDir, sctx)
			agnt.SetForceContinuation()
			sess.record.Status = "running"
			sess.record.FinishedAt = ""
		}
		if briefing := formatResumeBriefing(sess.record, sctx.ID); briefing != "" {
			agnt.SetResumeBriefing(briefing)
		}
	}

	// Restore before selecting event baselines: the private ledger can be
	// newer than the periodically saved record or exact instance snapshot.
	if _, err := agnt.RestoreExecutionCheckpoint(); err == nil {
		s.initializeSessionCounters(sess)
		// The restore REPLACES the agent state, so the objective has to be
		// re-attached: the operator's current objective wins over the one
		// persisted in the checkpoint.
		if spec := agentObjectiveSpec(sess.objective); spec != nil {
			agnt.SetObjective(spec)
		}
	}
	s.syncSessionAdmissionClocks(sess)

	// 5. Event processing goroutine — drains events and broadcasts to WebSocket
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC] Event processor panicked: %v — continuing\n%s", r, debug.Stack())
			}
		}() // never let panic escape event processor
		for evt := range events {
			s.processEvent(evt, sess)
		}
	}()

	// 6. Build instruction with severity filter
	instruction := sess.instruction
	// The objective travels as its OWN section, never merged into the
	// execution instruction (a task goal and an execution instruction are
	// different things).
	if sess.objective != nil {
		instruction = buildObjectiveInstruction(sess.objective) + instruction
	}
	if len(sess.severityFilter) > 0 {
		instruction = buildSeverityPrefix(sess.severityFilter) + "\n\n" + instruction
	}
	// Operator guidance queued while no run was active (API_CONTRACT §3.1)
	// merges into this session's instruction as a [OPERATOR GUIDANCE]
	// section — human intent (source=human) entering the loop's field of
	// view before the first iteration. Session start only, never an
	// iteration hot path.
	// planner_worker is a chat topology: queued guidance is delivered as a
	// main-agent turn (deliverQueuedGuidanceToMain) instead of being absorbed
	// into the task instruction, which produced no reply at all.
	if !plannerWorker {
		instruction = s.mergeQueuedGuidance(sess, instruction)
	}
	if plannerWorker {
		instruction = "[MAIN AGENT MODE — R7]\n" +
			"You are the main observing/coordinating agent. Workers execute the plan; you do not run attack tools. " +
			"Observe the graph, ledger, coverage, and findings; add intents/hints; steer or kill workers; and call finish only when the graph frontier is drained " +
			"(no open/running/paused intents). The worker pool and planner own execution and intent generation.\n\n" +
			instruction
	}

	// 7. Run agent (blocks until finished or stopped). Recheck the parent after
	// record setup; a stop racing this point either makes this check fail or
	// observes the registered agent and sets its stopped flag before Run works.
	if status, _ := s.instanceRunStatus(sess.instanceID); sess.instanceID != "" && status != "running" {
		log.Printf("[RESUME-GATE] instance %s observed status %q before Run — stopping agent", sess.instanceID, status)
		agnt.Stop()
	}
	// R7/M2: the worker pool claims authoritative intents and dispatches them
	// through the root's graph while the main agent observes. The planner loop
	// adds/retires intents from the graph overview; a settled worker wakes both.
	if plannerWorker {
		runID := strings.TrimSpace(sctx.InstanceID)
		if runID == "" {
			runID = sctx.ID
		}
		var planner *orchestrator.Planner
		plannerClient := sess.llmClient
		if plannerClient != nil {
			// The root agent owns the request-scoped client; the planner runs
			// concurrently, so give it an isolated clone.
			plannerClient = plannerClient.Clone()
		} else if cfg != nil {
			plannerClient = llm.NewClient(cfg)
		}
		goalText := strings.TrimSpace(sess.goal)
		objStatement := ""
		var objConstraints []string
		var objCriteria []orchestrator.PlannerCriterion
		if sess.objective != nil {
			objStatement = strings.TrimSpace(sess.objective.Statement)
			objConstraints = append([]string(nil), sess.objective.Constraints...)
			for _, c := range sess.objective.Criteria {
				objCriteria = append(objCriteria, orchestrator.PlannerCriterion{
					ID: c.ID, Kind: c.Kind, VulnClass: c.VulnClass,
					Capability: c.Capability, VerifyBy: c.VerifyBy, Required: c.Required,
				})
			}
		}
		if goalText == "" {
			// Structured analysis is the fallback only when the task has no
			// human goal text; it never replaces a real GOAL.
			goalText = objStatement
		}
		if plannerClient != nil {
			planner = orchestrator.NewPlanner(sctx.Ctx, orchestrator.PlannerConfig{
				ScanID:               runID,
				SessionID:            sctx.ID,
				Target:               sess.target,
				Goal:                 goalText,
				Instruction:          sess.instruction,
				UserInstruction:      sess.userInstruction,
				ObjectiveStatement:   objStatement,
				ObjectiveConstraints: objConstraints,
				ObjectiveCriteria:    objCriteria,
				Client:               plannerClient,
				Repo:                 graph.Backend(),
				ScanCtx:              sctx,
				ControlPlaneHosts:    sess.cfg.ControlPlaneHosts,
				ScopeTargets:         scopeTargets,
				ScopeDeny:            scopeDeny,
				ScopeAllowPorts:      scopePorts,
				Emit: func(kind, content string, meta map[string]any) {
					select {
					case events <- agent.Event{Type: kind, Content: content, Meta: meta, AgentID: "planner", Timestamp: time.Now()}:
					default:
					}
				},
				Interval: time.Minute,
				Steer: func(unitID, message string) error {
					if agnt == nil {
						return fmt.Errorf("root agent unavailable")
					}
					return agnt.SteerWorker(unitID, message)
				},
				Kill: func(unitID, reasonCode, reason string) error {
					if agnt == nil {
						return fmt.Errorf("root agent unavailable")
					}
					// Prefer the reason-carrying kill path when the agent
					// provides it; fall back to the plain kill otherwise.
					if killer, ok := any(agnt).(workerKillWithReason); ok {
						return killer.KillWorkerWithReason(unitID, reasonCode, reason)
					}
					return agnt.KillWorker(unitID, reason)
				},
				OnDrained: func() {
					if agnt != nil {
						agnt.NotifyMainSystem("The planner produced no new intent and the frontier is drained. If the user's goal is complete, call finish; otherwise insert the next intent or ask the user.")
					}
				},
			})
			defer planner.Stop()
		}
		pool, poolErr := orchestrator.New(sctx.Ctx, orchestrator.Config{
			ScanID:            runID,
			Workers:           workerCount,
			Targets:           []string{sess.target},
			Repo:              graph.Backend(),
			Root:              agnt,
			ControlPlaneHosts: sess.cfg.ControlPlaneHosts,
			ScopeTargets:      scopeTargets,
			ScopeDeny:         scopeDeny,
			ScopeAllowPorts:   scopePorts,
			// Round-5 P0-1: how long an execution-lane worker may produce no
			// information-layer increment before the engine reclaims its slot.
			WorkerProgressTimeout: workerProgressTimeoutFromEnv(),
			Goal:                  goalText,
			Instruction:           sess.instruction,
			UserInstruction:       sess.userInstruction,
			ScanCtx:               sctx,
			OnSettled: func(unitID string) {
				if planner != nil {
					planner.Notify()
				}
				_ = unitID
			},
			OnStalled: func() {
				if planner != nil {
					planner.Stall()
				}
			},
		})
		if poolErr != nil {
			log.Printf("[orchestrator] pool unavailable for %s: %v — continuing without workers", sess.instanceID, poolErr)
		} else {
			// Register the live handles so a task edit can be applied to this
			// run without a restart (task_config_live.go).
			s.registerLiveRun(&liveRunHandles{
				instanceID: sess.instanceID,
				scanCtx:    sctx,
				agent:      agnt,
				planner:    planner,
				pool:       pool,
				llmClient:  sess.llmClient,
			})
			defer s.unregisterLiveRun(sess.instanceID)
			// Planner-first: the planner derives the first intents from the task
			// goal/instruction; the pool starts with an empty frontier and
			// claims whatever the planner writes. The planner blocks once all
			// configured workers are busy, so open intents stay bounded by the
			// pool size instead of accumulating ahead of execution.
			if planner != nil {
				planner.SetMaxIntents(workerCount)
				planner.SetWaitForCapacity(pool.WaitForSlot)
				planner.SetStallReport(pool.StallReport)
			}
			s.registerWorkerPool(runID, pool)
			defer s.unregisterWorkerPool(runID, pool)
			pool.Start()
			defer pool.Stop()
			if planner != nil {
				planner.Start()
			}
		}
	}
	agnt.Run([]string{sess.target}, instruction)

	// 8. Close events channel and wait for event processor to drain
	close(events)
	<-done

	if status, stopReason := s.instanceRunStatus(sess.instanceID); isInterruptedInstanceStatus(status) {
		sess.record.Status = status
		sess.record.StopReason = stopReason
		sess.record.FinishedAt = time.Now().Format(time.RFC3339)
		// NOTE: in-memory→record merge and child→parent reporting merge
		// are deferred to sess.cleanup() (Wave C 4.2) so they survive an
		// agent panic. The deferred path is idempotent: merge dedups by
		// vuln summary key and MergeVulnsToContext skips ID/semantic
		// duplicates, so the success path merges exactly once.
		s.saveScanRecordTo(sess.record, sess.scanDir)
		return
	}

	// 8a. Abnormal LLM-side pause (provider rate limit, quota exhausted, overloaded).
	// Persist the current record, notes, ledger, iteration count, and workspace
	// exactly as the resume machinery expects, without failing or marking finished.
	if isProviderPauseReason(sess.abortReason) {
		stopReason := sess.abortReason
		if stopReason == "llm_rate_limited" {
			stopReason = "provider_rate_limited"
		}
		pausedAt := time.Now().Format(time.RFC3339)
		sess.record.Status = "paused"
		sess.record.StopReason = stopReason
		sess.record.FinishedAt = pausedAt

		if sess.instanceID != "" {
			s.instancesMu.RLock()
			inst, ok := s.instances[sess.instanceID]
			s.instancesMu.RUnlock()
			if ok {
				inst.mu.Lock()
				if inst.Status == "running" || inst.Status == "pending" || inst.Status == "" {
					inst.Status = "paused"
					inst.StopReason = stopReason
					inst.FinishedAt = pausedAt
					normalizeTerminalWildcardInstanceLocked(inst)
				}
				inst.mu.Unlock()
			}
		}

		s.saveScanRecordTo(sess.record, sess.scanDir)
		log.Printf("[SCAN] %s: agent paused (%s) at phase %d (%d iterations, %d tool calls); state preserved for resume",
			sess.id, stopReason, sess.record.CurrentPhase, sess.record.Iterations, sess.record.ToolCalls)
		return
	}

	// 8b. Abnormal LLM-side abort (agent bailed: refused tools / empty responses
	// / repeated errors / unrecoverable failure).
	//
	// Distinguish two very different situations that both surface as "the model
	// stopped calling tools":
	//
	//   (a) The scan already reported findings — it ran the assessment and just
	//       failed to emit the final `finish` call, then rambled until the
	//       no-tool guard fired. This is effectively a COMPLETION: the report
	//       reflects real work, so record it as "finished" (and let step 10
	//       generate the report). Failing/refunding here would throw away a real
	//       result the customer is entitled to.
	//
	//   (b) The scan bailed with NOTHING to show (no findings) — a genuine
	//       malfunction that produced no result. Record it as "failed" with a
	//       diagnostic stop reason so it is never shown as a clean completion and
	//       (on hosted) is refunded.
	//
	// For (b) we update BOTH the persisted record AND the live instance: the
	// scan-status API (applyInstanceSnapshot) overlays the in-memory instance
	// status over the record, and runMultiScan's deferred finalize would
	// otherwise flip a still-"running" instance to "finished". This runs after
	// the event processor has drained (<-done above), so there is no concurrent
	// processEvent writer.
	//
	// CRITICAL: only a TOP-LEVEL single/DAST session may fail the instance on
	// abort. A wildcard scan runs one discovery session + one session per
	// subdomain, and they ALL share the parent instance ID. Phase 1 discovery
	// routinely ends with a no-tool abort AFTER it has already enumerated the
	// subdomains, and individual subdomain sessions can abort while hundreds
	// more are still queued. Failing the instance from any of those clobbers a
	// scan that is genuinely still running (the reported "running but marked
	// failed" bug) and wrongly refunds it. Wildcard parent status is owned by
	// the orchestrator (runWildcardTarget / runMultiScan), so wildcard
	// sub-sessions fall through to the normal "finished" finalize below.
	if sess.abortReason != "" && shouldFailInstanceOnAbort(sess.scanMode, sess.parentTarget, sess.discoveryMode) {
		reportedVulns := 0
		if sess.sctx != nil {
			reportedVulns = len(reporting.GetVulnerabilitiesForContext(sess.sctx.ID))
		}
		producedResults := reportedVulns > 0 || len(sess.record.Vulns) > 0 || sess.record.CurrentPhase >= 20 || sess.record.ToolCalls >= 5
		if !producedResults {
			finishedAt := time.Now().Format(time.RFC3339)
			sess.record.Status = "failed"
			sess.record.StopReason = sess.abortReason
			sess.record.FinishedAt = finishedAt
			if sess.instanceID != "" {
				s.instancesMu.RLock()
				inst, ok := s.instances[sess.instanceID]
				s.instancesMu.RUnlock()
				if ok {
					inst.mu.Lock()
					// Never clobber a user-initiated stop/pause; only downgrade a
					// still-active instance to failed.
					if inst.Status == "running" || inst.Status == "pending" || inst.Status == "" {
						inst.Status = "failed"
						inst.StopReason = sess.abortReason
						inst.FinishedAt = finishedAt
						normalizeTerminalWildcardInstanceLocked(inst)
					}
					inst.mu.Unlock()
				}
			}
			s.saveScanRecordTo(sess.record, sess.scanDir)
			s.finalizeTokenUsage(sess)
			return
		}
		// (a) findings exist or testing was performed → fall through to a normal "finished" completion.
		// Persist the typed abort reason on the record so the UI can always
		// show WHY the scan ended (e.g. target_unresponsive), even when the
		// scan counts as completed because findings were produced.
		sess.record.StopReason = sess.abortReason
		log.Printf("[SCAN] %s: agent stopped calling tools (%s) after testing (phase %d, %d tool calls); recording as completed with report intact",
			sess.id, sess.abortReason, sess.record.CurrentPhase, sess.record.ToolCalls)
	}

	// 9. Finalize record
	if !s.finalizeScanSessionRecord(sess) {
		// Interrupted (user stop / instance halt / server shutdown): the record
		// is already persisted with its terminal status by
		// finalizeScanSessionRecord. Token diagnostics still finalize so the
		// summary reflects the aborted run too.
		s.finalizeTokenUsage(sess)
		return
	}

	// Token diagnostics: persist the aggregate summary and log the
	// [token-analysis] completion line (observability only; no sensitive
	// content).
	s.finalizeTokenUsage(sess)

	// Reports are operator-triggered only (REPORT_DELIVERY_SPEC §0/§2). The
	// scan lifecycle no longer renders a PDF, emits report_ready, or sends a
	// report attachment; Phase 22 stays a terminal disposition derived from
	// the accepted finish (phase_disposition.go), not an artifact trigger.
}

// processEvent handles a single agent event — forwards to WebSocket, updates scan record, sends Discord.
// markPhaseWorked records that the engine observed concrete work tied to the
// given phase. Deduplicated; first-observation order preserved.
func (r *ScanRecord) markPhaseWorked(phase int) {
	if r == nil || phase <= 0 {
		return
	}
	for _, p := range r.PhasesWorked {
		if p == phase {
			return
		}
	}
	r.PhasesWorked = append(r.PhasesWorked, phase)
}

// mirrorInstancePhaseStatus copies the scan record's authoritative per-phase
// dispositions onto the live instance so /api/instances serves disposition
// summaries (overview phase narration) without fetching each scan record.
// Coordinator sessions only — child sessions (ParentTarget set) never own the
// coordinator's instance view, the same ownership rule mirrorScanIntegrity
// applies. Both maps are cloned: markPhaseWorked appends into
// rec.PhasesWorked in place, and instance readers must never observe a
// backing array that later grows under them.
func (s *Server) mirrorInstancePhaseStatus(instanceID string, rec *ScanRecord) {
	if instanceID == "" || rec == nil || rec.ParentTarget != "" || rec.PhaseStatus == nil {
		return
	}
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		return
	}
	inst.mu.Lock()
	inst.PhaseStatus = clonePhaseStatus(rec.PhaseStatus)
	inst.PhasesWorked = append([]int(nil), rec.PhasesWorked...)
	inst.mu.Unlock()
}

func clonePhaseStatus(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Server) processEvent(evt agent.Event, sess *scanSession) {
	// Durable worker projection: every delegated-agent lifecycle boundary is
	// upserted into run_workers so the session page can list this run's
	// workers (finished ones included) after the live graph is gone.
	s.persistWorkerLifecycle(evt, sess)

	// Redact provider-internal control tokens from every event surface before
	// the event is broadcast or persisted. A model occasionally leaks its
	// transport's channel delimiters into prose; those artifacts must never
	// reach clients or the persisted event history (they also poison the
	// conversation when fed back to the model). Contains-gated, so clean text
	// skips the regex entirely.
	wsEvt := WSEvent{
		Type:        evt.Type,
		Content:     llm.StripProviderControlTokens(evt.Content),
		MessageID:   evt.MessageID,
		Delta:       llm.StripProviderControlTokens(evt.Delta),
		ToolName:    evt.ToolName,
		ToolArgs:    evt.ToolArgs,
		AgentID:     evt.AgentID,
		Timestamp:   evt.Timestamp.Format(time.RFC3339),
		TotalTokens: evt.TotalTokens,
		Aborted:     evt.Aborted,
		AbortReason: evt.AbortReason,
		// Structured lifecycle telemetry (agent_*/plan_updated) rides the
		// documented result_meta field so clients never parse prose. It is an
		// invalidation hint only; projection endpoints stay authoritative.
		ResultMeta: evt.Meta,
	}

	// Latch the full-detail specialist-wave defer diagnostic so a scan
	// resumed after a server restart does not repeat the wall of text:
	// it already displayed once in a previous process lifetime.
	if evt.Type == "message" && strings.HasPrefix(evt.Content, "⏸️ Specialist wave deferred") && sess.record != nil {
		sess.record.DelegationDeferNoted = true
	}

	if evt.Type == "tool_result" {
		wsEvt.Output = llm.StripProviderControlTokens(evt.ToolResult.Output)
		wsEvt.Error = llm.StripProviderControlTokens(evt.ToolResult.Error)
		// Typed outcome provenance (duplicate / verifier_rejected / saved
		// receipt ids): previously only output+error crossed the event schema,
		// so rejected reports were indistinguishable from clean ones unless a
		// consumer parsed prose.
		if evt.ToolResult.Metadata != nil {
			wsEvt.ResultMeta = evt.ToolResult.Metadata
		}
		// The plan's completed tasks are the richest per-phase work signal:
		// merge their phases into the worked ledger on EVERY result.
		// reconcilePlan completes tasks automatically from coverage and
		// deterministic-verifier evidence WITHOUT update_plan, so per-phase
		// accounting must not depend on the model calling it.
		if sess.record != nil && sess.agent != nil {
			for _, phase := range sess.agent.PlanWorkedPhases() {
				sess.record.markPhaseWorked(phase)
			}
			if dispositions := sess.agent.PhaseDispositions(); len(dispositions) > 0 {
				sess.record.PhaseStatus = agent.PhaseStatusMap(dispositions)
				sess.record.PhaseReasons = agent.PhaseReasonMap(dispositions)
				s.mirrorInstancePhaseStatus(sess.instanceID, sess.record)
			}
		}

		// Push vuln to UI in real-time when report_vulnerability succeeds
		if evt.ToolName == "report_vulnerability" && evt.ToolResult.Error == "" {
			vulnID, reported := metadataString(evt.ToolResult.Metadata, "vuln_id")
			if !reported {
				log.Printf("[VULN] report_vulnerability returned without a new vuln_id; not broadcasting stored vuln again")
			} else {
				vulns := reporting.GetVulnerabilitiesForContext(sess.sctx.ID)
				log.Printf("[VULN] report_vulnerability tool created %s, vulns in list: %d", vulnID, len(vulns))
				latest, found := findReportedVulnerabilityByID(vulns, vulnID)
				if !found {
					log.Printf("[VULN] report_vulnerability metadata referenced %s, but it was not found in context %s", vulnID, sess.sctx.ID)
				} else {
					vs := vulnToSummary(latest)
					vs.SourceScanID = sess.id
					// In-place upgrade: the reporting store replaced the
					// unverified candidate (same finding ID) with the
					// verified evidence. Mirror the replacement in the
					// record instead of accumulating the stale candidate
					// next to the upgrade.
					if metadataBool(evt.ToolResult.Metadata, "upgraded") {
						applySummaryReplacement(&sess.record.Vulns, &vs, true)
					}
					log.Printf("[VULN] Latest vuln: %s %s (CVSS %.1f)", vs.Severity, vs.Title, vs.CVSS)

					// Severity filter is a DISPLAY/BROADCAST gate, NOT a
					// persistence gate. Every vuln the agent reports must be
					// persisted to the scan record (and thus the on-disk
					// scan.json + the PDF report) so the report reflects
					// everything found — not just the severities the operator
					// chose to surface live. Filtering here previously dropped
					// below-threshold vulns from the record entirely, causing
					// "report shows no findings but logs show critical" (#157
					// customer feedback).
					allowed := true
					if len(sess.severityFilter) > 0 {
						allowed = false
						for _, sev := range sess.severityFilter {
							if strings.EqualFold(sev, vs.Severity) {
								allowed = true
								break
							}
						}
						log.Printf("[VULN] Severity filter active: filter=%v, allowed=%v", sess.severityFilter, allowed)
					}

					// Always persist to the record (report + on-disk source of truth).
					if appendVulnSummaryUnique(&sess.record.Vulns, vs) {
						log.Printf("[VULN] Vuln persisted to record: %s %s", vs.Severity, vs.Title)
						// A finding is a durable event, not just another feed
						// line: save the record NOW instead of waiting for the
						// every-10-events checkpoint, so the project findings
						// page (which reads the durable record) shows it on its
						// next poll rather than after an unrelated batch.
						s.saveScanRecordTo(sess.record, sess.scanDir)
						// Refresh the task-level projection too. run_findings is
						// the cold-path replica the task findings tab and the
						// task's finding_count read; without this it only
						// catches up at session cleanup, so a run in flight
						// looked like "findings sync after the run finishes".
						s.queueRunFindingProjection(sess.instanceID, sess.record)
						// Broadcast + notify only when the severity filter allows it.
						if allowed {
							wsEvt.Vulns = []VulnSummary{vs}
							log.Printf("[VULN] Vuln broadcast real-time: %s %s", vs.Severity, vs.Title)

							// Discord: vulnerability found (respects XALGORIX_DISCORD_MIN_SEVERITY)
							sevColor := 0xef4444 // red for critical/high
							switch vs.Severity {
							case "medium":
								sevColor = 0xd97706
							case "low", "info":
								sevColor = 0x3b82f6
							}
							details := vulnNotificationDetails(vs)
							// Apply Discord minimum severity filter
							if severityMeetsThreshold(vs.Severity, s.discordMinSeverity) {
								s.sendDiscordTo(s.discordWebhookForScan(sess.discordWebhook), sevColor, fmt.Sprintf("🐛 %s Vulnerability Found", strings.ToUpper(vs.Severity)), details)
							} else {
								log.Printf("[DISCORD] Skipping %s vuln notification (min severity: %s)", vs.Severity, s.discordMinSeverity)
							}
							// Apply Telegram minimum severity filter (independent of Discord)
							if s.telegramConfigured() && severityMeetsThreshold(vs.Severity, s.telegramMinSeverity) {
								s.sendTelegram(sevColor, fmt.Sprintf("🐛 %s Vulnerability Found", strings.ToUpper(vs.Severity)), details)
							} else if s.telegramConfigured() {
								log.Printf("[TELEGRAM] Skipping %s vuln notification (min severity: %s)", vs.Severity, s.telegramMinSeverity)
							}
							// Domestic IM: per-channel severity thresholds are applied
							// inside sendDomesticIM, independent of Discord/Telegram.
							s.sendDomesticIM(vs.Severity, sevColor, fmt.Sprintf("🐛 %s Vulnerability Found", strings.ToUpper(vs.Severity)), details)
						}
					} else {
						log.Printf("[VULN] Skipping duplicate vuln already present in session record: %s %s", vs.ID, vs.Title)
					}
					if !allowed {
						log.Printf("[VULN] Vuln persisted but NOT broadcast (filtered out by severity: %s, filter: %v)", vs.Severity, sess.severityFilter)
					}
				}
			}
		}
	}

	if phase := inferCurrentPhase(wsEvt, sess.phases); phase > 0 {
		// Monotonic: the phase-progress bar only ever moves forward. The agent
		// is autonomous and non-linear — it dips back into recon between
		// exploit attempts — so a last-wins update made the bar bounce
		// backward (and, before the keyword cleanup above, snap forward on a
		// single stray request). Reporting the max reached keeps progress
		// honest and stable.
		if sess.record != nil {
			// Observed-work ledger HONESTY: only phases inferred from TOOL
			// ACTIVITY (engine-classified command/tool arguments) count as
			// worked. Prose mentions ("moving to phase 20 now") and
			// lifecycle events advance the monotonic CurrentPhase — pure
			// narration — but never mark a phase as worked; authoritative
			// per-phase completion comes from record.PhaseStatus.
			if wsEvt.Type == "tool_call" {
				sess.record.markPhaseWorked(phase)
			}
			if phase > sess.record.CurrentPhase {
				sess.record.CurrentPhase = phase
			}
			wsEvt.CurrentPhase = sess.record.CurrentPhase
		} else {
			wsEvt.CurrentPhase = phase
		}
	}

	if evt.Type == "finished" || evt.Type == "paused" {
		// An abnormal LLM-side abort (refused tools / empty responses / repeated
		// errors / rate-limit) reuses the "finished" or "paused" event type but is NOT a
		// clean completion. Record the reason so finalize marks the scan failed or paused.
		// Delegated-agent events share the root event stream. A failed specialist
		// must be surfaced to the coordinator (the agent graph marks it failed),
		// but must not poison the root session's final status: the coordinator can
		// still cover that lane itself or use another specialist.
		// However, upstream provider unavailability (rate limits, quota exhaustion,
		// server overload) impacts the shared provider/account and cannot be bypassed
		// by switching agents, so provider-level pause reasons pause the root session.
		isRoot := isRootAgentEvent(sess, evt)
		isProviderPause := evt.Type == "paused" || isProviderPauseReason(evt.AbortReason)
		if evt.Aborted && (isRoot || isProviderPause) {
			if !isProviderPauseReason(sess.abortReason) || isProviderPause {
				sess.abortReason = evt.AbortReason
				if sess.abortReason == "" {
					if evt.Type == "paused" {
						sess.abortReason = "provider_rate_limited"
					} else {
						sess.abortReason = "llm_aborted"
					}
				}
			}
			if isProviderPause && sess.agent != nil {
				sess.agent.Stop()
			}
		}
		// Build set of vulns already broadcast in real-time to avoid duplicates
		seen := make(map[string]bool)
		for _, v := range sess.record.Vulns {
			seen[vulnSummaryKey(v)] = true
		}
		vulns := reporting.GetVulnerabilitiesForContext(sess.sctx.ID)
		log.Printf("[VULN] Finished event: total vulns in system: %d, already broadcast: %d", len(vulns), len(seen))
		for _, v := range vulns {
			vs := vulnToSummary(v)
			vs.SourceScanID = sess.id
			if seen[vulnSummaryKey(vs)] {
				log.Printf("[VULN] Finished: skipping already-broadcast vuln: %s %s", v.ID, v.Title)
				continue
			}
			allowed := true
			if len(sess.severityFilter) > 0 {
				allowed = false
				for _, sev := range sess.severityFilter {
					if strings.EqualFold(sev, vs.Severity) {
						allowed = true
						break
					}
				}
			}
			if allowed {
				wsEvt.Vulns = append(wsEvt.Vulns, vs)
				seen[vulnSummaryKey(vs)] = true
				log.Printf("[VULN] Finished: adding new vuln to final broadcast: %s %s", vs.Severity, vs.Title)
			} else {
				log.Printf("[VULN] Finished: filtered vuln (not added to broadcast): %s (filter: %v)", vs.Severity, sess.severityFilter)
			}
		}
		log.Printf("[VULN] Finished: total vulns in final broadcast: %d", len(wsEvt.Vulns))
	}

	// Track stats on per-session record
	if evt.Type == "thinking" {
		sess.record.Iterations++
	}
	if evt.Type == "tool_call" {
		sess.record.ToolCalls++
		// If no thinking event was emitted for this turn, fallback to tracking iteration on tool_call
		if sess.record.Iterations < sess.record.ToolCalls {
			sess.record.Iterations = sess.record.ToolCalls
		}
	}
	if evt.TotalTokens > 0 {
		sess.record.TotalTokens = max(sess.record.TotalTokens, sess.recordTokenOffset+evt.TotalTokens)
	}
	s.capturePlanDisposition(sess)
	progress := sess.record.AssessmentProgress

	// Update parent instance stats — ACCUMULATE across sessions (phases/subdomains),
	// don't overwrite. Each subdomain scan creates a fresh scanSession with zeroed
	// counters, so we increment the instance counters on each event.
	if sess.instanceID != "" {
		s.instancesMu.RLock()
		if inst, ok := s.instances[sess.instanceID]; ok {
			inst.mu.Lock()
			if evt.Type == "thinking" {
				inst.Iterations++
			}
			if evt.Type == "tool_call" {
				inst.ToolCalls++
				if inst.Iterations < inst.ToolCalls {
					inst.Iterations = inst.ToolCalls
				}
			}
			accountSessionUsageLocked(inst, sess.id, evt.TotalTokens, progress)
			sess.lastSessionTokens = max(sess.lastSessionTokens, evt.TotalTokens)
			sess.lastSessionProgress = max(sess.lastSessionProgress, progress)
			if sess.parentTarget == "" {
				sess.record.UsageBySession = cloneSessionUsage(inst.UsageBySession)
			}
			// Vulns: route through effectiveVulnCount so the counter source
			// is consistent across the scan lifecycle. While running, this
			// returns the in-memory count (parent context for wildcard child
			// sessions, session context otherwise); after teardown the
			// helper falls back to len(inst.Vulns). See Task 3.1 in
			// .kiro/specs/findings-consistency-and-pagination/tasks.md.
			inst.VulnCount = s.effectiveVulnCount(inst, sess)
			inst.mu.Unlock()
		}
		s.instancesMu.RUnlock()
	}

	// Integration invocation records (MCP calls / skill loads / delegations):
	// best-effort cross-scan projection derived from engine events. Runs
	// before the thinking filter — derivation only consumes tool_result /
	// delegated-finished shapes, so a thinking event falls through harmlessly.
	deriveInvocationRecords(evt, sess)

	// Filter thinking events from being stored or broadcast to the frontend
	if evt.Type == "thinking" {
		return
	}

	// Accumulate events for persistence (limit stored output size)
	savedEvt := wsEvt
	if len(savedEvt.Output) > 500 {
		savedEvt.Output = savedEvt.Output[:500] + "..."
	}
	if sess.record.EventsJournal {
		savedEvt.EventID = fmt.Sprintf("%s:%d", sess.id, sess.record.EventsTotal+1)
		wsEvt.EventID = savedEvt.EventID
		if err := appendScanEventJournal(sess.scanDir, savedEvt); err != nil {
			sess.abortReason = "checkpoint_write_failed"
			log.Printf("[checkpoint] event append failed: %v", err)
			if sess.agent != nil {
				sess.agent.Stop()
			}
			return
		}
		sess.record.EventsTotal++
		sess.record.Events = appendEventTail(sess.record.Events, savedEvt)
		sess.record.EventsTruncated = sess.record.EventsTotal > len(sess.record.Events)
	} else {
		sess.record.Events = append(sess.record.Events, savedEvt)
	}

	// Periodically save scan record (every 10 events)
	if max(sess.record.EventsTotal, len(sess.record.Events))%10 == 0 {
		s.saveScanRecordTo(sess.record, sess.scanDir)
	}

	// Use instance-scoped broadcasting
	log.Printf("[VULN] Broadcasting: type=%s, instanceID=%s, vulns=%d", evt.Type, sess.instanceID, len(wsEvt.Vulns))
	if sess.instanceID != "" {
		s.broadcastToInstance(sess.instanceID, wsEvt)
	} else {
		s.broadcast(wsEvt)
	}
}

// buildSeverityPrefix creates the severity filter instruction prefix.
func buildSeverityPrefix(severityFilter []string) string {
	severityText := "CRITICAL INSTRUCTION: You MUST ONLY look for and report "
	severities := make([]string, len(severityFilter))
	copy(severities, severityFilter)
	severityText += strings.Join(severities, " and ") + " severity vulnerabilities. "
	severityText += "DO NOT report, investigate, or mention any LOW severity, INFORMATIONAL, or INFO findings. "
	severityText += "Ignore any potential LOW/INFO issues - they are out of scope for this engagement. "
	severityText += "Focus ONLY on: " + strings.Join(severities, ", ") + "."
	return severityText
}

func firstSelectedPhase(phases []int) int {
	if len(phases) == 0 {
		return 1
	}
	first := 0
	for _, phase := range phases {
		if phase < 1 || phase > 22 {
			continue
		}
		if first == 0 || phase < first {
			first = phase
		}
	}
	if first == 0 {
		return 1
	}
	return first
}

func phaseAllowed(phases []int, phase int) bool {
	if phase < 1 || phase > 22 {
		return false
	}
	if len(phases) == 0 {
		return true
	}
	for _, allowed := range phases {
		if allowed == phase {
			return true
		}
	}
	return false
}

func isReconReportOnlyPhaseSelection(phases []int) bool {
	if len(phases) == 0 {
		return false
	}
	for _, phase := range phases {
		if phase != 1 && phase != 22 {
			return false
		}
	}
	return true
}

var phaseMentionRe = regexp.MustCompile(`(?i)\bphase\s+([0-9]{1,2})\b`)

func inferCurrentPhase(evt WSEvent, allowed []int) int {
	if phase := parsePhaseMention(evt.Content); phaseAllowed(allowed, phase) {
		return phase
	}
	switch evt.Type {
	case "queue_started", "target_started", "scan_started":
		return firstSelectedPhase(allowed)
	case "queue_finished", "report_ready":
		if phaseAllowed(allowed, 22) {
			return 22
		}
	}

	if evt.Type != "tool_call" {
		return 0
	}
	args := strings.ToLower(strings.Join(mapValues(evt.ToolArgs), " "))

	switch {
	case strings.Contains(args, "sqlmap") || strings.Contains(args, "dalfox") ||
		strings.Contains(args, "union select") || strings.Contains(args, "<script") ||
		strings.Contains(args, "sleep("):
		if phaseAllowed(allowed, 6) {
			return 6
		}
	case strings.Contains(args, "ffuf") || strings.Contains(args, "gobuster") ||
		strings.Contains(args, "dirsearch") || strings.Contains(args, "feroxbuster"):
		if phaseAllowed(allowed, 3) {
			return 3
		}
	case strings.Contains(args, "ssrf") || strings.Contains(args, "169.254.169.254"):
		if phaseAllowed(allowed, 7) {
			return 7
		}
	// Phase 8: IDOR & Broken Access Control — detect from IDOR/BAC testing patterns
	case strings.Contains(args, "idor") || strings.Contains(args, "broken access") ||
		strings.Contains(args, "access control") || strings.Contains(args, "horizontal") ||
		strings.Contains(args, "vertical privilege") || strings.Contains(args, "privilege escalation"):
		if phaseAllowed(allowed, 8) {
			return 8
		}
	// Phase 9: API & GraphQL Testing
	case strings.Contains(args, "graphql") || strings.Contains(args, "introspection") ||
		strings.Contains(args, "__schema") || strings.Contains(args, "batch query") ||
		strings.Contains(args, "rest api") || strings.Contains(args, "swagger") ||
		strings.Contains(args, "openapi"):
		if phaseAllowed(allowed, 9) {
			return 9
		}
	// Phase 10: File Upload Testing
	case strings.Contains(args, "file upload") || strings.Contains(args, "multipart") ||
		strings.Contains(args, "upload.php") || strings.Contains(args, "webshell") ||
		strings.Contains(args, "shell.php"):
		if phaseAllowed(allowed, 10) {
			return 10
		}
	// Phase 11: Deserialization & RCE
	case strings.Contains(args, "deserialize") || strings.Contains(args, "rce") ||
		strings.Contains(args, "command injection") || strings.Contains(args, "code execution") ||
		strings.Contains(args, "pickle") || strings.Contains(args, "ysoserial"):
		if phaseAllowed(allowed, 11) {
			return 11
		}
	// Phase 12: Race Conditions & Business Logic
	case strings.Contains(args, "race condition") || strings.Contains(args, "concurrent") ||
		strings.Contains(args, "business logic") || strings.Contains(args, "turbo intruder") ||
		strings.Contains(args, "time-of-check"):
		if phaseAllowed(allowed, 12) {
			return 12
		}
	// Phase 13: Subdomain Takeover
	case strings.Contains(args, "subdomain takeover") || strings.Contains(args, "dangling") ||
		strings.Contains(args, "cname") || strings.Contains(args, "can-i-take-over-xyz") ||
		strings.Contains(args, "nuclei takeover"):
		if phaseAllowed(allowed, 13) {
			return 13
		}
	// Phase 14: Open Redirect Testing
	case strings.Contains(args, "open redirect") || strings.Contains(args, "url redirect") ||
		strings.Contains(args, "redirect=") || strings.Contains(args, "next=") ||
		strings.Contains(args, "returnurl=") || strings.Contains(args, "return_to="):
		if phaseAllowed(allowed, 14) {
			return 14
		}
	// Phase 16: Cloud & Infrastructure
	case strings.Contains(args, "s3 bucket") || strings.Contains(args, "aws") ||
		strings.Contains(args, "gcp") || strings.Contains(args, "azure") ||
		strings.Contains(args, "cloud storage") || strings.Contains(args, "terraform"):
		if phaseAllowed(allowed, 16) {
			return 16
		}
	// Phase 17: WebSocket Testing
	case strings.Contains(args, "websocket") || strings.Contains(args, "ws://") ||
		strings.Contains(args, "wss://") || strings.Contains(args, "socket.io"):
		if phaseAllowed(allowed, 17) {
			return 17
		}
	// Phase 20: Exploit Verification
	case strings.Contains(args, "exploit") || strings.Contains(args, "verify exploit") ||
		strings.Contains(args, "proof of concept") || strings.Contains(args, "poc"):
		if phaseAllowed(allowed, 20) {
			return 20
		}
	// Phase 21: Novel Vulnerability Discovery
	case strings.Contains(args, "novel") || strings.Contains(args, "fuzzing") ||
		strings.Contains(args, "mutation") || strings.Contains(args, "edge case"):
		if phaseAllowed(allowed, 21) {
			return 21
		}
	// NOTE: we deliberately do NOT infer phases 4/5 from tool-arg keywords
	// like "authorization", "cookie", "login", or "session". Those tokens
	// appear in ORDINARY requests on almost every target (an authed scan
	// sends an Authorization header from the first recon request), so they
	// caused the progress bar to false-jump to a late phase during early
	// reconnaissance. Those phases advance via the agent's own phase
	// narration (parsePhaseMention above), which is a real signal.
	case strings.Contains(args, "nmap") || strings.Contains(args, "naabu") ||
		strings.Contains(args, "masscan") || strings.Contains(args, "dig ") ||
		strings.Contains(args, "nslookup") || strings.Contains(args, "host ") ||
		strings.Contains(args, "whatweb") || strings.Contains(args, "wappalyzer") ||
		strings.Contains(args, "httpx") || strings.Contains(args, "wafw00f") ||
		strings.Contains(args, "subfinder") || strings.Contains(args, "amass") ||
		strings.Contains(args, "crt.sh"):
		if phaseAllowed(allowed, 1) {
			return 1
		}
	// Fallback: any remaining request-bearing tool call (plain curl/wget/
	// http_request) is target interaction that previously fell through
	// unclassified, so the every-scan-does-it recon phase never registered
	// as worked and rendered "skipped" on completed scans. This only feeds
	// the WORKED ledger; it cannot jump the progress bar.
	case strings.Contains(args, "http"):
		if phaseAllowed(allowed, 1) {
			return 1
		}
	}

	return 0
}

func parsePhaseMention(text string) int {
	match := phaseMentionRe.FindStringSubmatch(text)
	if len(match) != 2 {
		return 0
	}
	var phase int
	if _, err := fmt.Sscanf(match[1], "%d", &phase); err != nil {
		return 0
	}
	if phase < 1 || phase > 22 {
		return 0
	}
	return phase
}

func mapValues(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// latestDigestBriefing returns the most recent active context-digest snapshots
// for the run, newest first, bounded to 12 KiB. These nodes are engine
// projections of the authoritative ledger/graph/finding/coverage state written
// at compaction time; they deliberately contain IDs rather than raw bodies, so
// the resumed agent must re-fetch details before acting.
func latestDigestBriefing(rec *ScanRecord, contextID string) string {
	repo := graph.Backend()
	if repo == nil {
		return ""
	}
	scanID := strings.TrimSpace(rec.InstanceID)
	if scanID == "" {
		scanID = strings.TrimSpace(rec.ID)
	}
	if scanID == "" {
		scanID = strings.TrimSpace(contextID)
	}
	if scanID == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	nodes, err := repo.ListNodes(ctx, scanID, graph.KindDigest, graph.StateActive)
	if err != nil || len(nodes) == 0 {
		return ""
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].CreatedAt.After(nodes[j].CreatedAt) })

	const budget = 12 * 1024
	var sb strings.Builder
	sb.WriteString("Engine state snapshot from before the restart (authoritative projection; re-fetch IDs before relying on them):\n")
	written := 0
	for _, n := range nodes {
		var payload map[string]any
		_ = json.Unmarshal([]byte(n.Payload), &payload)
		snapshot, _ := payload["snapshot"].(string)
		if strings.TrimSpace(snapshot) == "" {
			continue
		}
		if written > 0 && written+len(snapshot) > budget {
			break
		}
		if len(snapshot) > budget-written {
			snapshot = snapshot[:budget-written] + "\n… (truncated to the resume budget)"
		}
		sb.WriteString("\n")
		sb.WriteString(snapshot)
		sb.WriteString("\n")
		written += len(snapshot)
		if written >= budget {
			break
		}
	}
	if written == 0 {
		return ""
	}
	return sb.String()
}

func formatResumeBriefing(rec *ScanRecord, contextID string) string {
	if rec == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔄 SCENARIO RESUME BRIEFING (Continued scan session from Iteration %d):\n", rec.Iterations))
	sb.WriteString(fmt.Sprintf("Target: %s\n", rec.Target))
	// L3 (REDESIGN 2026-11-22): surface why the previous session ended so the
	// resumed agent doesn't re-derive it from scratch (eino_runtime_error
	// mid-tool, user_stopped, goal_achieved, …).
	if rec.StopReason != "" {
		sb.WriteString(fmt.Sprintf("Previous session ended with: %s. Pick up from there — do not redo completed work.\n", rec.StopReason))
	}
	if len(rec.Vulns) > 0 {
		sb.WriteString(fmt.Sprintf("Confirmed Vulnerabilities Reported So Far (%d):\n", len(rec.Vulns)))
		for _, v := range rec.Vulns {
			sb.WriteString(fmt.Sprintf(" - [%s] %s (%s)\n", strings.ToUpper(v.Severity), v.Title, v.Endpoint))
		}
	} else {
		sb.WriteString("No vulnerabilities reported yet.\n")
	}

	notesData := notes.FormatForContextBudgetID(contextID, notes.NotesContextBudgetBytes)
	if notesData != "" {
		sb.WriteString("\nDiscovered Endpoints & Target Notes:\n")
		sb.WriteString(notesData)
		sb.WriteString("\n")
	}

	// IG6d: a resumed run starts from the last engine projection of the
	// authoritative state (ledger/graph/findings/coverage), not from a replay
	// of raw transcript bytes. Bounded to 12 KiB.
	if digest := latestDigestBriefing(rec, contextID); digest != "" {
		sb.WriteString("\n")
		sb.WriteString(digest)
	}

	var lastEvents []string
	if len(rec.Events) > 0 {
		for i := len(rec.Events) - 1; i >= 0 && len(lastEvents) < 6; i-- {
			e := rec.Events[i]
			switch e.Type {
			case "tool_call":
				lastEvents = append([]string{fmt.Sprintf("Executed tool %s with args: %v", e.ToolName, e.ToolArgs)}, lastEvents...)
			case "tool_result":
				out := e.Output
				errStr := e.Error
				if strings.Contains(out, "missing required parameter") || strings.Contains(errStr, "missing required parameter") {
					continue
				}
				if len(out) > 300 {
					out = out[:300] + "... (truncated)"
				}
				lastEvents = append([]string{fmt.Sprintf("Tool result (%s): %s", e.ToolName, out)}, lastEvents...)
			}
		}
	}
	if len(lastEvents) > 0 {
		sb.WriteString("\nRecent Execution Events Prior To Restart:\n")
		for _, te := range lastEvents {
			sb.WriteString(" • " + te + "\n")
		}
	}

	sb.WriteString("\nCONTINUE ASSESSMENT: Resume deep penetration testing from where you left off. Do not re-run basic recon steps already documented above. Target new endpoints or unverified attack vectors now.")
	return sb.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isProviderPauseReason(reason string) bool {
	return reason == "provider_rate_limited" ||
		reason == "provider_quota_exhausted" ||
		reason == "provider_overloaded" ||
		reason == "llm_rate_limited" ||
		// R7/M1: the authoritative graph is unavailable; the run pauses
		// (resumable) instead of proceeding on a divergent in-memory plan.
		reason == "graph_unavailable"
}

func isRootAgentEvent(sess *scanSession, evt agent.Event) bool {
	if evt.AgentID == "" {
		return true
	}
	if sess != nil && sess.agent != nil && sess.agent.ID != "" && evt.AgentID == sess.agent.ID {
		return true
	}
	return !strings.HasPrefix(evt.AgentID, "sub_") && !strings.HasPrefix(evt.AgentID, "sync_")
}

// capturePlanDisposition records the root plan's final task dispositions on
// the scan record. Called on every terminal path (interrupted, server-shutdown,
// failed, and clean finish) so partial assessments are visible everywhere, not
// only on the happy path.
func (s *Server) capturePlanDisposition(sess *scanSession) {
	if sess == nil || sess.record == nil || sess.agent == nil {
		return
	}
	// R7 planner_worker: the main agent does not own the plan. Capture terminal
	// plan counters/detail from the authoritative graph instead of the empty
	// main-agent ScanState.
	planFromGraph := false
	if sess.cfg != nil && strings.EqualFold(strings.TrimSpace(sess.cfg.Orchestrator), "planner_worker") {
		if repo := graph.Backend(); repo != nil {
			runID := strings.TrimSpace(sess.instanceID)
			if runID == "" {
				runID = sess.id
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			counts, cerr := repo.Counts(ctx, runID)
			tasks, terr := repo.PlanTasks(ctx, runID)
			cancel()
			if cerr == nil {
				total := counts.OpenIntents + counts.RunningIntents + counts.PausedIntents + counts.DoneIntents + counts.BlockedIntents
				sess.record.PlanTasksTotal = total
				sess.record.PlanTasksCompleted = counts.DoneIntents
				sess.record.PlanTasksSkipped = counts.BlockedIntents
				sess.record.PlanTasksUnfinished = counts.OpenIntents + counts.RunningIntents + counts.PausedIntents
				sess.record.PlanPresent = total > 0
				planFromGraph = true
			}
			if terr == nil && len(tasks) > 0 {
				view := planViewFromGraphTasks(tasks)
				sess.record.PlanView = &view
				for _, t := range tasks {
					if t.Status == "completed" && t.Phase > 0 {
						sess.record.markPhaseWorked(t.Phase)
					}
				}
			}
		}
	}
	if !planFromGraph {
		sess.record.PlanTasksTotal, sess.record.PlanTasksCompleted,
			sess.record.PlanTasksSkipped, sess.record.PlanTasksUnfinished = sess.agent.PlanDisposition()
		sess.record.PlanPresent = sess.agent.HasPlan()
		// Persist the plan projection next to its counters (same write point, same
		// atomic record save). Without it a terminal run can only report numbers,
		// which is exactly the "Planner pane is empty" failure this endpoint fixes.
		if view := sess.agent.PlanSnapshot(); view.Present {
			snapshot := view
			sess.record.PlanView = &snapshot
		}
	}
	sess.record.AssessmentProgress = max(sess.record.AssessmentProgress, sess.agent.AssessmentProgress())
	// Completed plan tasks carry explicit phase numbers - the richest
	// per-phase work evidence (auth=5, business-logic=12, classes the command
	// heuristics never see). Merge them into the worked ledger so terminal
	// records are complete.
	if !planFromGraph {
		for _, phase := range sess.agent.PlanWorkedPhases() {
			sess.record.markPhaseWorked(phase)
		}
	}
	// Final authoritative dispositions: Phase 20 (verification) and 22
	// (reporting) settle here alongside the task-derived phases.
	if dispositions := sess.agent.PhaseDispositions(); len(dispositions) > 0 {
		sess.record.PhaseStatus = agent.PhaseStatusMap(dispositions)
		sess.record.PhaseReasons = agent.PhaseReasonMap(dispositions)
		s.mirrorInstancePhaseStatus(sess.instanceID, sess.record)
	}
	// The objective evaluation is captured on the same terminal paths as
	// the plan disposition, so a finished run and an interrupted run both
	// carry the honest objective state.
	s.captureObjectiveDisposition(sess)
	s.mirrorScanIntegrity(sess.instanceID, sess.record)
}

// finalizeScanSessionRecord saves the terminal or interrupted scan record to disk.
// Returns true if the scan finished normally and report generation should proceed,
// or false if the session was interrupted/stopped and must not generate a report.
func (s *Server) finalizeScanSessionRecord(sess *scanSession) bool {
	if sess == nil || sess.record == nil {
		return false
	}
	if instStatus, stopReason := s.instanceRunStatus(sess.instanceID); isInterruptedInstanceStatus(instStatus) {
		sess.record.Status = instStatus
		sess.record.Completion = "partial"
		sess.record.StopReason = stopReason
		sess.record.FinishedAt = time.Now().Format(time.RFC3339)
		s.capturePlanDisposition(sess)
		s.saveScanRecordTo(sess.record, sess.scanDir)
		return false
	}
	if s.stopReq.Load() || (sess.parentCtx != nil && sess.parentCtx.Err() != nil) {
		sess.record.Status = "stopped"
		sess.record.Completion = "partial"
		sess.record.StopReason = "server_shutdown"
		sess.record.FinishedAt = time.Now().Format(time.RFC3339)
		s.capturePlanDisposition(sess)
		s.saveScanRecordTo(sess.record, sess.scanDir)
		return false
	}

	// Structured completeness: a session that terminated through a forced stop
	// (loop limit, exhausted budget, unrecoverable error) is NOT a complete
	// assessment even when findings exist and a report is generated. Persist
	// the completion outcome and reason separately from the terminal status so
	// billing/report flows stay unchanged while the record stays honest — a
	// forced stop must never read as an unqualified coverage-success claim.
	sess.record.Completion = "full"
	if sess.abortReason != "" {
		sess.record.Completion = "partial"
		sess.record.StopReason = sess.abortReason
	} else if sess.agent != nil && sess.agent.CompletionOutcome() == agent.CompletionStatusIncomplete {
		// The engine's honest terminal state: the finish gates released the
		// scan through the bounded rejection ceiling while obligations
		// remained (finish_gate_exhausted). Findings and the report are
		// preserved, but the record must never read as full coverage.
		sess.record.Completion = "partial"
		if sess.record.StopReason == "" {
			// Typed reason from the engine (finish_gate_exhausted, or
			// untested_at_finish when a principled finish closed plan work
			// unexecuted with no validated finding).
			reason := sess.agent.CompletionStopReason()
			if reason == "" {
				reason = agent.StopReasonFinishGateExhausted
			}
			sess.record.StopReason = reason
		}
	}
	// Final plan dispositions: the completion label alone cannot express what
	// the assessment actually executed. A clean finish with 8 skipped and 2
	// unfinished tasks is a different report from one with everything executed.
	s.capturePlanDisposition(sess)
	sess.record.Status = "finished"
	sess.record.FinishedAt = time.Now().Format(time.RFC3339)
	s.mirrorScanIntegrity(sess.instanceID, sess.record)

	// NOTE: merges are deferred to sess.cleanup() under safe.Recover boundaries
	// to guarantee panic-safe persistence.
	s.saveScanRecordTo(sess.record, sess.scanDir)
	return true
}

// mergeQueuedGuidance folds guidance turns queued while no run was active
// (API_CONTRACT §3.1) into the session's instruction, flipping each row to
// injected and broadcasting guidance_injected. Called once per session start
// (not an iteration hot path); a platform-less or unmapped-instance session
// passes the instruction through untouched. Order is chronological (oldest
// operator intent first).
//
// consumed is NOT derived here: no reliable observation chain exists yet to
// prove the engine acted on a specific turn (no observer on the message
// path), so rows stay injected until a cold-path observer flips them via
// UpdateGuidanceStatus (R2a decision, documented in the platform repository).
// deliverQueuedGuidanceToMain hands every still-queued human turn of this run
// to the R7 main agent, in chronological order. The main chat answers each one
// as a normal conversation turn; without this, a message sent during dispatch
// (before the instance was registered) never reached the agent and the
// operator saw a sent message with no reply.
func (s *Server) deliverQueuedGuidanceToMain(sess *scanSession, agnt *agent.Agent) {
	store := s.currentPlatformStore()
	if store == nil || sess == nil || sess.instanceID == "" || agnt == nil {
		return
	}
	ctx := context.Background()
	runs, err := store.RunsByInstanceID(ctx, sess.instanceID)
	if err != nil || len(runs) == 0 {
		return
	}
	items, err := store.ListGuidance(ctx, runs[0].TaskID, 50)
	if err != nil || len(items) == 0 {
		return
	}
	for i := len(items) - 1; i >= 0; i-- { // newest-first list → chronological
		g := items[i]
		if g.Status != platform.GuidanceQueued || g.InstanceID != sess.instanceID {
			continue
		}
		s.injectGuidanceToMain(sess.instanceID, g, agnt)
	}
}

// injectGuidanceToMain delivers one human turn to the R7 main chat exactly
// once per process. The session-start drain and the guidance POST can both
// observe the same queued row (the POST's readiness check may run a moment
// before the agent attaches); the delivered marker makes the loser a no-op.
func (s *Server) injectGuidanceToMain(instanceID string, g platform.Guidance, agnt *agent.Agent) bool {
	if agnt == nil || strings.TrimSpace(g.ID) == "" {
		return false
	}
	s.guidanceDeliveredMu.Lock()
	if s.guidanceDelivered == nil {
		s.guidanceDelivered = make(map[string]struct{})
	}
	if _, done := s.guidanceDelivered[g.ID]; done {
		s.guidanceDeliveredMu.Unlock()
		return false
	}
	// Only queued rows are ever injected, so clearing a huge map cannot
	// re-deliver anything.
	if len(s.guidanceDelivered) > 4096 {
		s.guidanceDelivered = make(map[string]struct{})
	}
	s.guidanceDelivered[g.ID] = struct{}{}
	s.guidanceDeliveredMu.Unlock()

	if _, err := agnt.SendMessage(operatorGuidanceMessage(g.ID, g.Message)); err != nil {
		s.guidanceDeliveredMu.Lock()
		delete(s.guidanceDelivered, g.ID)
		s.guidanceDeliveredMu.Unlock()
		log.Printf("[platform] queued guidance %s delivery to %s failed: %v", g.ID, instanceID, err)
		return false
	}
	// Information-layer projection: delivered guidance is a source=human
	// observation, not only an R7 hint node.
	s.projectGuidanceInfoItem(agnt.ScanContext(), g)

	if store := s.currentPlatformStore(); store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := store.UpdateGuidanceStatus(ctx, g.ID, platform.GuidanceInjected); err != nil {
			// The turn is already in the main agent's inbox; keep the delivered
			// marker so it is not sent twice. The row stays queued on disk.
			log.Printf("[platform] queued guidance %s flip to injected failed: %v", g.ID, err)
		}
		cancel()
	}
	g.Status = platform.GuidanceInjected
	s.broadcastToInstance(instanceID, WSEvent{
		Type:     "guidance_injected",
		Content:  "Operator guidance delivered to the main agent",
		Guidance: &g,
	})
	s.watchGuidanceConsumption(instanceID, g.ID)
	return true
}

// guidanceDeliveredAlready reports whether this process already handed the row
// to the main agent (the loser of a delivery race).
func (s *Server) guidanceDeliveredAlready(id string) bool {
	s.guidanceDeliveredMu.Lock()
	defer s.guidanceDeliveredMu.Unlock()
	_, ok := s.guidanceDelivered[id]
	return ok
}

func (s *Server) mergeQueuedGuidance(sess *scanSession, instruction string) string {
	store := s.currentPlatformStore()
	if store == nil || sess.instanceID == "" {
		return instruction
	}
	ctx := context.Background()
	runs, err := store.RunsByInstanceID(ctx, sess.instanceID)
	if err != nil || len(runs) == 0 {
		return instruction
	}
	items, err := store.ListGuidance(ctx, runs[0].TaskID, 50)
	if err != nil || len(items) == 0 {
		return instruction
	}
	var queued []platform.Guidance
	for i := len(items) - 1; i >= 0; i-- { // newest-first list → chronological
		if items[i].Status == platform.GuidanceQueued {
			queued = append(queued, items[i])
		}
	}
	if len(queued) == 0 {
		return instruction
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(instruction, "\n"))
	b.WriteString("\n\n[OPERATOR GUIDANCE]:\n")
	for _, g := range queued {
		fmt.Fprintf(&b, "- %s\n", g.Message)
	}
	b.WriteString("The operator left the guidance above for this task: fold it into your plan as human-provided intent and act on it alongside the original goal.")
	for i := range queued {
		g := &queued[i]
		if _, err := store.UpdateGuidanceStatus(ctx, g.ID, platform.GuidanceInjected); err != nil {
			log.Printf("[platform] guidance %s flip to injected failed: %v", g.ID, err)
			continue
		}
		g.Status = platform.GuidanceInjected
		s.projectGuidanceInfoItem(sess.sctx, *g)
		s.broadcastToInstance(sess.instanceID, WSEvent{
			Type:     "guidance_injected",
			Content:  "Queued operator guidance merged into this run",
			Guidance: g,
		})
	}
	return b.String()
}

// workerProgressTimeoutFromEnv reads XALGORIX_WORKER_PROGRESS_TIMEOUT (Go
// duration, e.g. "20m"). Unset or invalid leaves the orchestrator default
// (20m); "0" or "off" disables the reclaim clock for this deployment.
func workerProgressTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("XALGORIX_WORKER_PROGRESS_TIMEOUT"))
	if raw == "" {
		return 0
	}
	if raw == "0" || strings.EqualFold(raw, "off") || strings.EqualFold(raw, "disabled") {
		return -time.Second
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	log.Printf("[orchestrator] XALGORIX_WORKER_PROGRESS_TIMEOUT=%q is not a duration; using the default", raw)
	return 0
}
