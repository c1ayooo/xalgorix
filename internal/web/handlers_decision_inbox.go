package web

// handlers_decision_inbox.go — the operator decision inbox
// (PLANNER_OBSERVATION_AND_CORRECTION §2.4/§5.6).
//
//	GET  /api/scans/{id}/decisions — read-only projection of the few things a
//	                                 human may need to decide: stalled workers,
//	                                 blocked intents, intents parked for a
//	                                 capability, open intents and recent
//	                                 executor deviations.
//	POST /api/scans/{id}/decisions — the bounded decision actions (steer_work,
//	                                 kill_work, retire_intent,
//	                                 reprioritize_intent) routed to the live
//	                                 agent bound to the run, plus
//	                                 issue_capability (INTEL_HANDOFF_SPEC M3),
//	                                 which writes a one-shot capability and
//	                                 resumes the parked intent.
//
// This surface is deliberately NOT an event stream: it never subscribes to
// run_events/SSE. Stalled workers come from the live pool's StallReport; the
// plan comes from the graph authority; deviations come from the audit
// projection. All four actions are decisions only — lane tool surfaces, scope
// checks and the report gate still apply.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/intelaccess"
)

// decisionInboxLimit bounds each list in the inbox so one bad run cannot flood
// the panel or an operator's screen.
const decisionInboxLimit = 50

// decisionInboxReadTimeout bounds the graph/journal reads behind one GET.
const decisionInboxReadTimeout = 5 * time.Second

// decisionStalledView is one stalled worker (live pool projection).
type decisionStalledView struct {
	UnitID       string `json:"unit_id"`
	AgentID      string `json:"agent_id"`
	Lane         string `json:"lane"`
	IdleSeconds  int64  `json:"idle_seconds"`
	ActiveTool   bool   `json:"active_tool"`
	LastProgress string `json:"last_progress,omitempty"`
}

// decisionBlockedView is one blocked intent with the engine's recorded reason.
type decisionBlockedView struct {
	UnitID        string `json:"unit_id"`
	Title         string `json:"title"`
	BlockedReason string `json:"blocked_reason"`
}

// decisionPausedView is one intent parked for an operator decision: it needs a
// capability before the pool may dispatch it out of scope (M3).
type decisionPausedView struct {
	UnitID string `json:"unit_id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
	Target string `json:"target,omitempty"`
}

// decisionOpenIntentView is one open intent awaiting a worker.
type decisionOpenIntentView struct {
	UnitID     string `json:"unit_id"`
	Title      string `json:"title"`
	Priority   int    `json:"priority"`
	Target     string `json:"target"`
	TargetKind string `json:"target_kind"`
}

// decisionDeviationView is one executor-reported departure from its intent.
type decisionDeviationView struct {
	UnitID     string `json:"unit_id"`
	Summary    string `json:"summary"`
	RecordedAt string `json:"recorded_at"`
}

// decisionInboxView is the GET response. Every list is non-nil so the panel
// renders [] rather than null.
type decisionInboxView struct {
	ScanID      string                   `json:"scan_id"`
	GeneratedAt string                   `json:"generated_at"`
	Stalled     []decisionStalledView    `json:"stalled"`
	Blocked     []decisionBlockedView    `json:"blocked"`
	Paused      []decisionPausedView     `json:"paused"`
	OpenIntents []decisionOpenIntentView `json:"open_intents"`
	Deviations  []decisionDeviationView  `json:"deviations"`
}

// decisionRequest is the POST body.
type decisionRequest struct {
	Action     string `json:"action"`
	UnitID     string `json:"unit_id"`
	Message    string `json:"message"`
	ReasonCode string `json:"reason_code"`
	Reason     string `json:"reason"`
	// Priority is a pointer so a missing value is distinguishable from 0.
	Priority *int `json:"priority"`
	// issue_capability: the scope of one approval (INTEL_HANDOFF_SPEC M3).
	Targets    []string `json:"targets"`
	UsesLeft   int      `json:"uses_left"`
	TTLSeconds int      `json:"ttl_s"`
}

// scanDecisionID parses the scan id out of /api/scans/{id}/decisions.
func scanDecisionID(path string) string {
	scanID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/scans/"), "/decisions")
	scanID = strings.TrimSpace(scanID)
	if scanID == "" || strings.Contains(scanID, "/") {
		return ""
	}
	return scanID
}

// handleScanDecisions dispatches GET (projection) and POST (decision action).
func (s *Server) handleScanDecisions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getScanDecisions(w, r)
	case http.MethodPost:
		s.postScanDecision(w, r)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// getScanDecisions serves the read-only inbox projection.
func (s *Server) getScanDecisions(w http.ResponseWriter, r *http.Request) {
	scanID := scanDecisionID(r.URL.Path)
	if scanID == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "scan id is required"})
		return
	}
	runID := s.graphRunID(scanID)
	view := decisionInboxView{
		ScanID:      scanID,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Stalled:     []decisionStalledView{},
		Blocked:     []decisionBlockedView{},
		Paused:      []decisionPausedView{},
		OpenIntents: []decisionOpenIntentView{},
		Deviations:  []decisionDeviationView{},
	}

	// Stalled workers: the live pool's own projection. No event stream.
	s.workerPoolsMu.Lock()
	pool := s.workerPools[runID]
	s.workerPoolsMu.Unlock()
	if pool != nil {
		for _, stalled := range pool.StallReport() {
			entry := decisionStalledView{
				UnitID:      stalled.UnitID,
				AgentID:     stalled.AgentID,
				Lane:        stalled.Lane,
				IdleSeconds: int64(stalled.IdleFor.Seconds()),
				ActiveTool:  stalled.ActiveTool,
			}
			if !stalled.LastProgress.IsZero() {
				entry.LastProgress = stalled.LastProgress.UTC().Format(time.RFC3339)
			}
			view.Stalled = append(view.Stalled, entry)
		}
	}

	// Blocked / open intents: the graph is the plan authority.
	repo := graph.Backend()
	if repo != nil {
		ctx, cancel := context.WithTimeout(r.Context(), decisionInboxReadTimeout)
		defer cancel()
		tasks, err := repo.PlanTasks(ctx, runID)
		if err != nil {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
				"error": "graph plan read failed", "detail": err.Error(), "scan_id": scanID,
			})
			return
		}
		// blocked_reason and target_kind live on the node, not the plan view.
		blockedReason := map[int64]string{}
		payloads := map[int64]map[string]any{}
		if nodes, nerr := repo.ListNodes(ctx, runID, graph.KindIntent); nerr == nil {
			for _, node := range nodes {
				blockedReason[node.ID] = node.BlockedReason
				payloads[node.ID] = node.PayloadJSON()
			}
		}
		for _, task := range tasks {
			state := strings.ToLower(strings.TrimSpace(task.GraphState))
			if state == "" {
				state = strings.ToLower(strings.TrimSpace(task.Status))
			}
			switch state {
			case string(graph.StateBlocked), "skipped":
				if len(view.Blocked) >= decisionInboxLimit {
					continue
				}
				view.Blocked = append(view.Blocked, decisionBlockedView{
					UnitID:        task.TaskID,
					Title:         task.Title,
					BlockedReason: blockedReason[task.NodeID],
				})
			case string(graph.StatePaused):
				if len(view.Paused) >= decisionInboxLimit {
					continue
				}
				target := strings.TrimSpace(task.Target)
				if target == "" && payloads[task.NodeID] != nil {
					if raw, ok := payloads[task.NodeID]["target"].(string); ok {
						target = strings.TrimSpace(raw)
					}
				}
				view.Paused = append(view.Paused, decisionPausedView{
					UnitID: task.TaskID,
					Title:  task.Title,
					Reason: blockedReason[task.NodeID],
					Target: target,
				})
			case string(graph.StateOpen), "pending":
				if len(view.OpenIntents) >= decisionInboxLimit {
					continue
				}
				target := strings.TrimSpace(task.Target)
				targetKind := ""
				if payload := payloads[task.NodeID]; payload != nil {
					if target == "" {
						if raw, ok := payload["target"].(string); ok {
							target = strings.TrimSpace(raw)
						}
					}
					if raw, ok := payload["target_kind"].(string); ok {
						targetKind = strings.TrimSpace(raw)
					}
				}
				view.OpenIntents = append(view.OpenIntents, decisionOpenIntentView{
					UnitID:     task.TaskID,
					Title:      task.Title,
					Priority:   task.Priority,
					Target:     target,
					TargetKind: targetKind,
				})
			}
		}
	}

	view.Deviations = s.decisionDeviations(r.Context(), scanID, runID)
	writeJSONStatus(w, http.StatusOK, view)
}

// decisionDeviations projects the run's executor deviations from the audit
// journal. A missing/legacy journal simply yields an empty list: the inbox
// never fabricates a history.
func (s *Server) decisionDeviations(ctx context.Context, scanID, runID string) []decisionDeviationView {
	path := ""
	if dir, ok := s.resolveScanDirByID(scanID); ok {
		path = filepath.Join(dir, domainjournal.FileName)
	}
	recs, _, err := domainjournal.ReadRecordsResolved(ctx, path, runID)
	if err != nil {
		return []decisionDeviationView{}
	}
	proj := domainjournal.Project(runID, recs)
	if len(proj.Deviations) == 0 {
		return []decisionDeviationView{}
	}
	deviations := proj.Deviations
	if len(deviations) > decisionInboxLimit {
		deviations = deviations[len(deviations)-decisionInboxLimit:]
	}
	out := make([]decisionDeviationView, 0, len(deviations))
	for _, d := range deviations {
		recordedAt := ""
		if !d.At.IsZero() {
			recordedAt = d.At.UTC().Format(time.RFC3339)
		}
		out = append(out, decisionDeviationView{
			UnitID:     d.IntentID,
			Summary:    d.Summary,
			RecordedAt: recordedAt,
		})
	}
	return out
}

// postScanDecision applies one human decision action against the live agent
// bound to the run. The engine keeps the final say: a refused action returns
// 4xx with the engine's error rather than a silent no-op.
func (s *Server) postScanDecision(w http.ResponseWriter, r *http.Request) {
	scanID := scanDecisionID(r.URL.Path)
	if scanID == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "scan id is required"})
		return
	}
	limitJSONBody(w, r)
	var req decisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	unitID := strings.TrimSpace(req.UnitID)
	if action == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "action is required"})
		return
	}
	if unitID == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "unit_id is required"})
		return
	}

	runID := s.graphRunID(scanID)
	// issue_capability acts on the graph + projection store, so it works even
	// when no agent is live (a parked intent outlives its worker).
	if action == "issue_capability" {
		s.postIssueCapability(w, r, scanID, runID, unitID, req)
		return
	}
	agnt := s.instanceAgent(runID)
	if agnt == nil {
		s.mu.RLock()
		agnt = s.currentAgents[scanID]
		if agnt == nil && runID != scanID {
			agnt = s.currentAgents[runID]
		}
		s.mu.RUnlock()
	}
	if agnt == nil {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": "no live agent is bound to this run", "scan_id": scanID,
		})
		return
	}

	var message string
	switch action {
	case "steer_work":
		msg := strings.TrimSpace(req.Message)
		if msg == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "message is required for steer_work"})
			return
		}
		if err := agnt.SteerWorker(unitID, msg); err != nil {
			writeDecisionError(w, scanID, err)
			return
		}
		message = fmt.Sprintf("Steered the worker for %s (actor=human).", unitID)
	case "kill_work":
		if err := agnt.KillWorkerWithReason(unitID, req.ReasonCode, req.Reason); err != nil {
			writeDecisionError(w, scanID, err)
			return
		}
		message = fmt.Sprintf("Cancelled the worker for %s (reason_code=%s, actor=human).", unitID, decisionReasonCode(req.ReasonCode))
	case "retire_intent":
		if strings.TrimSpace(req.Reason) == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "reason is required for retire_intent"})
			return
		}
		if err := agnt.RetireIntentForAPI(unitID, req.Reason); err != nil {
			writeDecisionError(w, scanID, err)
			return
		}
		message = fmt.Sprintf("Retired intent %s (actor=human).", unitID)
	case "reprioritize_intent":
		if req.Priority == nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "priority is required for reprioritize_intent"})
			return
		}
		if err := agnt.ReprioritizeIntent(unitID, *req.Priority, req.Reason); err != nil {
			writeDecisionError(w, scanID, err)
			return
		}
		message = fmt.Sprintf("Reprioritized intent %s to %d (actor=human).", unitID, *req.Priority)
	default:
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "unsupported action (allowed: steer_work, kill_work, retire_intent, reprioritize_intent, issue_capability)",
		})
		return
	}

	writeJSONStatus(w, http.StatusOK, map[string]any{
		"ok":      true,
		"action":  action,
		"unit_id": unitID,
		"message": message,
	})
}

// writeDecisionError maps an engine refusal to a 4xx/5xx. State conflicts
// (a stale target, a refused stagnant kill) are 409; an unavailable pool is
// 503 so the operator knows the decision could not even be routed.
func writeDecisionError(w http.ResponseWriter, scanID string, err error) {
	status := http.StatusConflict
	if strings.Contains(strings.ToLower(err.Error()), "unavailable") {
		status = http.StatusServiceUnavailable
	}
	writeJSONStatus(w, status, map[string]any{
		"error":   err.Error(),
		"scan_id": scanID,
	})
}

// decisionReasonCode mirrors the engine's empty-code default so the response
// message matches what the engine actually applied.
func decisionReasonCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return "human"
	}
	return code
}

// capabilityApproval bounds one operator approval. The engine never issues a
// broader permission than this, whatever the request body says.
const (
	capabilityDefaultUses = 1
	capabilityMaxUses     = 10
	capabilityDefaultTTL  = 10 * time.Minute
	capabilityMinTTL      = time.Minute
	capabilityMaxTTL      = 2 * time.Hour
)

// postIssueCapability issues one-shot permissions for a parked intent and
// resumes it (INTEL_HANDOFF_SPEC M3 / D3 / D9). DenyTargets never reach this
// endpoint: the judge refuses them before a request exists, and a target that
// no usable intel grant covers cannot be approved here either.
func (s *Server) postIssueCapability(w http.ResponseWriter, r *http.Request, scanID, runID, unitID string, req decisionRequest) {
	store := intelaccess.StoreOrNil()
	if store == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{
			"error": "intel projection store is not available",
		})
		return
	}
	repo := graph.Backend()
	if repo == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{
			"error": "graph authority is not available",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tasks, err := repo.PlanTasks(ctx, runID)
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error": "graph plan read failed", "detail": err.Error(),
		})
		return
	}
	var (
		nodeID   int64
		fallback string
	)
	for _, task := range tasks {
		if strings.TrimSpace(task.TaskID) == unitID {
			nodeID = task.NodeID
			fallback = strings.TrimSpace(task.Target)
			break
		}
	}
	if nodeID == 0 {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"error": "no intent named " + unitID + " in this run", "scan_id": scanID,
		})
		return
	}

	targets := make([]string, 0, len(req.Targets))
	for _, raw := range req.Targets {
		raw = strings.TrimSpace(raw)
		if raw != "" {
			targets = append(targets, raw)
		}
	}
	if len(targets) == 0 {
		if fallback == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{
				"error": "targets are required (the intent names no target to approve)",
			})
			return
		}
		targets = []string{fallback}
	}

	uses := req.UsesLeft
	if uses <= 0 {
		uses = capabilityDefaultUses
	}
	if uses > capabilityMaxUses {
		uses = capabilityMaxUses
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = capabilityDefaultTTL
	}
	if ttl < capabilityMinTTL {
		ttl = capabilityMinTTL
	}
	if ttl > capabilityMaxTTL {
		ttl = capabilityMaxTTL
	}

	issued := 0
	batches := make([]string, 0, 2)
	for _, raw := range targets {
		target, ok := intelaccess.NormalizeTarget(raw)
		if !ok {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{
				"error": "target " + raw + " is not a host:port or CIDR",
			})
			return
		}
		access, found, err := store.FindGrant(ctx, runID, target)
		if err != nil {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
				"error": "intel projection read failed", "detail": err.Error(),
			})
			return
		}
		if !found {
			writeJSONStatus(w, http.StatusConflict, map[string]any{
				"error":  "no usable intel grants this target",
				"target": target,
				"hint":   "confirm the intel item first, or add the operator grant on the 情报 tab",
			})
			return
		}
		capabilities, err := store.IssueCapabilities(ctx, intelaccess.IssueInput{
			ScanID:   runID,
			IntentID: nodeID,
			AccessID: access.ID,
			Targets:  []string{target},
			UsesLeft: uses,
			TTL:      ttl,
			IssuedBy: "operator",
		})
		if err != nil {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
				"error": "capability write failed", "detail": err.Error(),
			})
			return
		}
		issued += len(capabilities)
		for _, capability := range capabilities {
			if capability.BatchID != "" && (len(batches) == 0 || batches[len(batches)-1] != capability.BatchID) {
				batches = append(batches, capability.BatchID)
			}
		}
	}

	// Resume the parked intent. Not being paused is not an error: an approval
	// may arrive before the pool has parked it (or after a kill), and the
	// capability still applies when the intent is next dispatched.
	resumed := false
	if moved, err := repo.Transition(ctx, runID, nodeID, graph.KindIntent, graph.StatePaused, graph.StateOpen); err == nil && moved {
		resumed = true
	}

	writeJSONStatus(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"action":              "issue_capability",
		"unit_id":             unitID,
		"capabilities_issued": issued,
		"batches":             batches,
		"uses_left":           uses,
		"expires_in_seconds":  int(ttl.Seconds()),
		"resumed":             resumed,
		"message": fmt.Sprintf("Issued %d capability row(s) for %s (uses=%d, ttl=%s).",
			issued, unitID, uses, ttl),
	})
}
