package web

// handlers_approvals.go — R2a HITL REST surface (REDESIGN §5 / D5,
// docs/API_CONTRACT.md §3/§3.1):
//
//	GET  /api/approvals                 ?status=pending|decided|all&project_id=&task_id=&page=&size=
//	GET  /api/approvals/summary         {pending_total, nearest_deadline?}
//	POST /api/approvals/{id}/decision   {decision, reason?} → Approval (409 on non-pending)
//	POST /api/approvals/batch-decision  {ids, decision, reason?} → counts
//	GET/POST /api/tasks/{id}/guidance   (contract §3.1; routed via handleTaskDetail)
//
// The running-agent injection path embeds a stable `[OPERATOR GUIDANCE
// gi:<id>]: ` marker and arms the journal observer (guidance_observer.go) that
// flips the row to consumed once engine output cites the marker.
//
// pending_total is ALWAYS the cross-project count (D7 exception: the
// OpsPulse pending counter is the one cross-project data surface). Response
// shapes follow mock-backend.mjs:887-958 field-for-field — the frontend
// cache breaks on any shape drift. Decision endpoints are state-changing and
// inherit the global auth+CSRF middleware.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// handleApprovals serves GET /api/approvals (list + cross-project pending
// total) and GET /api/approvals/summary (OpsPulse, 5s polling).
func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/summary") {
		s.handleApprovalsSummary(w, r, store)
		return
	}

	q := r.URL.Query()
	status := q.Get("status")
	if status == "" {
		status = "pending" // mock-backend default
	}
	switch status {
	case "pending", "decided", "all":
	default:
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "status must be pending, decided or all"})
		return
	}
	page, size := parsePageParams(q.Get("page"), q.Get("size"))
	approvals, err := store.ListApprovals(r.Context(), platform.ApprovalFilter{
		ProjectID: q.Get("project_id"),
		TaskID:    q.Get("task_id"),
		Status:    status,
		Page:      page,
		Size:      size,
	})
	if err != nil {
		writePlatformError(w, err)
		return
	}
	s.projectApprovalNames(r.Context(), store, approvals)
	summary, err := store.PendingSummary(r.Context())
	if err != nil {
		log.Printf("[platform] pending summary: %v", err)
		writePlatformError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"approvals":     approvals,
		"pending_total": summary.Total,
	})
}

// handleApprovalsSummaryRoute is the mux-level entry for the exact
// "/api/approvals/summary" pattern. It validates method and store before
// delegating, so the summary can never be swallowed by the detail subtree.
func (s *Server) handleApprovalsSummaryRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	s.handleApprovalsSummary(w, r, store)
}

// handleApprovalsSummary serves GET /api/approvals/summary: the cross-project
// pending count plus the nearest pending deadline (front-end countdown).
func (s *Server) handleApprovalsSummary(w http.ResponseWriter, r *http.Request, store platform.Repository) {
	summary, err := store.PendingSummary(r.Context())
	if err != nil {
		writePlatformError(w, err)
		return
	}
	resp := struct {
		PendingTotal    int        `json:"pending_total"`
		NearestDeadline *time.Time `json:"nearest_deadline,omitempty"`
	}{PendingTotal: summary.Total}
	if summary.NearestDeadline != nil {
		t := summary.NearestDeadline.UTC()
		resp.NearestDeadline = &t
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

// handleApprovalDetail serves POST /api/approvals/{id}/decision and POST
// /api/approvals/batch-decision (both state-changing, operator-only).
func (s *Server) handleApprovalDetail(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/batch-decision") {
		s.handleBatchDecision(w, r, store)
		return
	}
	// Path: /api/approvals/{id}/decision
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/approvals/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "decision" {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	limitJSONBody(w, r)
	var req struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	switch strings.ToLower(strings.TrimSpace(req.Decision)) {
	case platform.ApprovalApproved, platform.ApprovalRejected:
	default:
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "decision must be approved or rejected"})
		return
	}
	approval, err := s.decideApproval(r.Context(), store, parts[0], strings.ToLower(strings.TrimSpace(req.Decision)), req.Reason)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, approval)
}

// handleBatchDecision serves POST /api/approvals/batch-decision: rows that
// are missing or already decided count as skipped (mock-backend:919-936),
// never fail the whole batch.
func (s *Server) handleBatchDecision(w http.ResponseWriter, r *http.Request, store platform.Repository) {
	limitJSONBody(w, r)
	var req struct {
		IDs      []string `json:"ids"`
		Decision string   `json:"decision"`
		Reason   string   `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	switch decision {
	case platform.ApprovalApproved, platform.ApprovalRejected:
	default:
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "decision must be approved or rejected"})
		return
	}
	counts := map[string]int{"approved": 0, "rejected": 0, "skipped": 0}
	for _, id := range req.IDs {
		row, err := s.decideApproval(r.Context(), store, id, decision, req.Reason)
		switch {
		case err == nil:
			counts[row.Status]++
		case errors.Is(err, platform.ErrNotFound), errors.Is(err, platform.ErrConflict):
			counts["skipped"]++
		default:
			writePlatformError(w, err)
			return
		}
	}
	writeJSONStatus(w, http.StatusOK, counts)
}

// decideApproval persists one operator decision through the gate service
// (same exit path as timeout/cancel so a waiting tool call wakes and the WS
// event fires). decided_by carries the configured dashboard user, matching
// the contract's "operator:<user>" vocabulary.
func (s *Server) decideApproval(ctx context.Context, store platform.Repository, id, decision, reason string) (*platform.Approval, error) {
	decidedBy := "operator"
	if cfg := s.configSnapshot(); cfg != nil && strings.TrimSpace(cfg.Username) != "" {
		decidedBy = "operator:" + cfg.Username
	}
	if svc := s.approvalService(); svc != nil {
		return svc.Decide(ctx, id, decision, decidedBy, reason)
	}
	// No gate service (unit-wired server): persist directly and still
	// broadcast so live feeds stay coherent.
	row, err := store.DecideApproval(ctx, id, platform.ApprovalDecision{
		Decision:  decision,
		DecidedBy: decidedBy,
		Reason:    reason,
	})
	if err != nil {
		return nil, err
	}
	decided := WSEvent{Type: "approval_decided", Approval: row}
	s.broadcastToInstance(row.InstanceID, decided)
	s.appendConversationEvent(row.InstanceID, decided)
	return row, nil
}

// projectApprovalNames fills the list-only projection fields (project_name /
// task_name) the cross-project OpsPulse view renders (API_CONTRACT §3).
func (s *Server) projectApprovalNames(ctx context.Context, store platform.Repository, approvals []platform.Approval) {
	projectNames := map[string]string{}
	taskNames := map[string]string{}
	for i := range approvals {
		a := &approvals[i]
		if a.ProjectID != "" {
			if name, ok := projectNames[a.ProjectID]; ok {
				a.ProjectName = name
			} else if project, err := store.GetProject(ctx, a.ProjectID); err == nil {
				projectNames[a.ProjectID] = project.Name
				a.ProjectName = project.Name
			} else {
				projectNames[a.ProjectID] = ""
			}
		}
		if a.TaskID != "" {
			if name, ok := taskNames[a.TaskID]; ok {
				a.TaskName = name
			} else if task, err := store.GetTask(ctx, a.TaskID); err == nil {
				taskNames[a.TaskID] = task.Name
				a.TaskName = task.Name
			} else {
				taskNames[a.TaskID] = ""
			}
		}
	}
}

// ── guidance (contract §3.1) ────────────────────────────────────────────────

// maxGuidanceCodePoints is the contract's message ceiling (4000 code points).
const maxGuidanceCodePoints = 4000

// handleTaskGuidance serves GET/POST /api/tasks/{id}/guidance, dispatched from
// handleTaskDetail. POST is idempotent on request_id (a retry returns the
// first outcome with accepted:false — no duplicate human turn, contract §3.1).
func (s *Server) handleTaskGuidance(w http.ResponseWriter, r *http.Request, store platform.Repository, taskID string) {
	switch r.Method {
	case http.MethodGet:
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, ok := atoiPositive(raw); ok {
				limit = parsed
			}
		}
		items, err := store.ListGuidance(r.Context(), taskID, limit)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, map[string]any{"items": items})

	case http.MethodPost:
		limitJSONBody(w, r)
		var req struct {
			Message    string `json:"message"`
			RequestID  string `json:"request_id"`
			InstanceID string `json:"instance_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		req.Message = strings.TrimSpace(req.Message)
		if req.Message == "" || len([]rune(req.Message)) > maxGuidanceCodePoints {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "message must be 1..4000 code points"})
			return
		}
		req.RequestID = strings.TrimSpace(req.RequestID)
		if req.RequestID == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "request_id required"})
			return
		}

		if _, err := store.GetTask(r.Context(), taskID); err != nil {
			writePlatformError(w, err)
			return
		}

		// Target resolution: explicit instance_id first (it must actually be
		// mounted on THIS task — an instance from another task would silently
		// deliver the guidance to a foreign run, so it's a 400), else the
		// task's projected-running run (mock-backend:937-957 semantics).
		runID, instanceID, terr := s.resolveGuidanceRun(r.Context(), store, taskID, req.InstanceID)
		if terr != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": terr.Error()})
			return
		}
		canInject := false
		if instanceID != "" {
			canInject = s.instanceCanReceiveGuidance(instanceID)
		}

		createdBy := ""
		if claims, ok := sessionClaimsFrom(r); ok {
			createdBy = claims.Username
		}
		g, created, err := store.CreateGuidance(r.Context(), platform.GuidanceInput{
			TaskID:     taskID,
			RunID:      runID,
			InstanceID: instanceID,
			Message:    req.Message,
			RequestID:  req.RequestID,
			CreatedBy:  createdBy,
		})
		if err != nil {
			writePlatformError(w, err)
			return
		}
		if !created {
			// Idempotent replay: return the FIRST outcome, never re-inject.
			writeJSONStatus(w, http.StatusOK, map[string]any{
				"status":      g.Status,
				"guidance_id": g.ID,
				"run_id":      g.RunID,
				"accepted":    false,
			})
			return
		}

		// First human turn names the conversation (4zreco chat semantics): the
		// sidebar stops reading like a timestamped auto-mount.
		s.maybeTitleFromGuidance(r.Context(), store, instanceID, g.Message)

		status := platform.GuidanceQueued
		if canInject {
			// Path A: the stable gi:<id> marker (guidance_observer.go) makes
			// the turn attributable from engine output, which is what lets the
			// journal observer flip the row to consumed later.
			if _, serr := s.instanceAgent(instanceID).SendMessage(operatorGuidanceMessage(g.ID, g.Message)); serr == nil {
				status = platform.GuidanceInjected
				if _, uerr := store.UpdateGuidanceStatus(r.Context(), g.ID, platform.GuidanceInjected); uerr != nil {
					log.Printf("[platform] guidance %s flip to injected failed: %v", g.ID, uerr)
					status = platform.GuidanceQueued
				} else {
					g.Status = platform.GuidanceInjected
					s.broadcastToInstance(instanceID, WSEvent{Type: "guidance_injected", Guidance: g, Content: "Operator guidance delivered to the running scan"})
					s.watchGuidanceConsumption(instanceID, g.ID)
				}
			} else {
				log.Printf("[platform] guidance %s injection into run %s failed: %v — queued", g.ID, runID, serr)
			}
		}
		if status == platform.GuidanceQueued {
			// No live agent to receive it: dashboard-scope event (no instance
			// subscription), merged into the next session's briefing instead.
			s.broadcastDashboard(WSEvent{Type: "guidance_queued", Guidance: g, Content: "Operator guidance queued for the next run"})
		}
		writeJSONStatus(w, http.StatusOK, map[string]any{
			"status":      status,
			"guidance_id": g.ID,
			"run_id":      runID,
			"accepted":    true,
		})

	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// resolveGuidanceRun picks (run_id, instance_id) for a guidance turn. An
// explicitly named instance must actually be mounted on THIS task
// (RunsByInstanceID ownership check — L5: an instance from another task is a
// 400, never a silent foreign delivery); otherwise the task's first
// projected-running run wins, else the latest run of the task.
func (s *Server) resolveGuidanceRun(ctx context.Context, store platform.Repository, taskID, instanceID string) (string, string, error) {
	if instanceID != "" {
		mounts, err := store.RunsByInstanceID(ctx, instanceID)
		if err != nil {
			return "", "", fmt.Errorf("could not resolve instance %s: %w", instanceID, err)
		}
		for _, run := range mounts {
			if run.TaskID == taskID {
				return run.ID, run.InstanceID, nil
			}
		}
		return "", "", fmt.Errorf("instance %s is not mounted on task %s", instanceID, taskID)
	}
	runs, _, err := store.ListRuns(ctx, taskID, 1, runProjectionPageSize)
	if err != nil {
		return "", "", nil
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	idx.projectRuns(runs, s.pendingDispatchExists)
	var latest *platform.Run
	for i := range runs {
		run := &runs[i]
		if run.Status == "running" {
			return run.ID, run.InstanceID, nil
		}
		if latest == nil {
			latest = run
		}
	}
	if latest != nil {
		return latest.ID, latest.InstanceID, nil
	}
	return "", "", nil
}

// instanceCanReceiveGuidance mirrors the chat route's liveness check
// (chat.go:64-71): the instance exists, reports running, and carries a live
// agent.
func (s *Server) instanceCanReceiveGuidance(instanceID string) bool {
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		return false
	}
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return inst.Status == "running" && inst.agent != nil
}

func (s *Server) instanceAgent(instanceID string) *agent.Agent {
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		return nil
	}
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return inst.agent
}

// atoiPositive parses a positive integer query value.
func atoiPositive(raw string) (int, bool) {
	n := 0
	if raw == "" {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1_000_000 {
			return 0, false
		}
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}
