package web

// project_scope.go — the project page's scope/fence configuration surface
// (REDESIGN §5 × D7).
//
//	GET  /api/projects/{id}/scope           → effective scope + fence + problems
//	PUT  /api/projects/{id}/scope           → replace the scope (validated)
//	POST /api/projects/{id}/scope/validate  → dry-run: shape + per-target verdict
//
// The module exists because the boundary has two halves that used to be edited
// only at project creation: dispatch targets (validated by
// validateTargetsInScope) and the runtime fence the HITL approval gate applies
// (scope_fence.go). Both halves are one object (platform.Scope), so the UI must
// be able to read, edit, dry-run and save it as one unit; Quick-create then
// dispatches against exactly this boundary.
//
// Contract notes:
//   - PUT accepts the raw scope object ({"targets":[...],"allow_ports":[...],
//     "fence":{...}}), same shape Project.scope uses on the wire.
//   - Validation is fail-closed: the store's own validateScope runs after
//     platform.ValidateScope here, and neither normalises away a bad entry.
//   - Tightening the fence while runs are active is allowed (approvals are
//     per-call) but the response carries a warning so the UI can say so.
//
// Registered by handleProjectDetail (the /api/projects/ subtree), so no new
// route prefix is needed.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// projectScopeView is the GET/PUT response: the stored boundary, the problems
// that make it non-enforceable, and whether the fence actually gates approvals
// right now (a fence with HITL off is configuration, not a live control).
type projectScopeView struct {
	ProjectID    string         `json:"project_id"`
	Scope        platform.Scope `json:"scope"`
	Fence        platform.Fence `json:"fence"`
	Problems     []string       `json:"problems"`
	ActiveRuns   int            `json:"active_runs"`
	ApprovalMode string         `json:"approval_mode"`
	FenceBound   bool           `json:"fence_bound"`
	Warning      string         `json:"warning,omitempty"`
}

// scopeTargetCheck is one dry-run verdict for a candidate dispatch target.
type scopeTargetCheck struct {
	Target  string `json:"target"`
	InScope bool   `json:"in_scope"`
	Reason  string `json:"reason,omitempty"`
}

// scopeValidateResponse is the dry-run payload the UI's 校验 action renders.
type scopeValidateResponse struct {
	OK           bool               `json:"ok"`
	Problems     []string           `json:"problems"`
	Entries      []scopeTargetCheck `json:"entries"`
	Scope        platform.Scope     `json:"scope"`
	Fence        platform.Fence     `json:"fence"`
	FenceBound   bool               `json:"fence_bound"`
	ApprovalMode string             `json:"approval_mode"`
}

// handleProjectScope dispatches the /api/projects/{id}/scope subtree.
func (s *Server) handleProjectScope(w http.ResponseWriter, r *http.Request, store platform.Repository, projectID string, rest []string) {
	switch {
	case len(rest) == 0:
		switch r.Method {
		case http.MethodGet:
			s.getProjectScope(w, r, store, projectID)
		case http.MethodPut:
			s.putProjectScope(w, r, store, projectID)
		default:
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	case len(rest) == 1 && rest[0] == "validate":
		if r.Method != http.MethodPost {
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		s.validateProjectScope(w, r, store, projectID)
	default:
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (s *Server) getProjectScope(w http.ResponseWriter, r *http.Request, store platform.Repository, projectID string) {
	project, err := store.GetProject(r.Context(), projectID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, s.buildProjectScopeView(r.Context(), store, project, scopeProblems(project.Scope), ""))
}

func (s *Server) putProjectScope(w http.ResponseWriter, r *http.Request, store platform.Repository, projectID string) {
	limitJSONBody(w, r)
	var scope platform.Scope
	if err := json.NewDecoder(r.Body).Decode(&scope); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if problems := scopeProblems(scope); len(problems) > 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{
			"error":    problems[0],
			"problems": problems,
		})
		return
	}
	project, err := store.UpdateProject(r.Context(), projectID, platform.ProjectUpdate{Scope: &scope})
	if err != nil {
		writePlatformError(w, err)
		return
	}
	// Scope observer baseline (v4.6): materialize preset rules for the new
	// scope hosts so the operator sees what the observer will auto-deny.
	if n := s.seedScopeApprovalRules(r.Context(), store, project.ID); n > 0 {
		log.Printf("[scope-observer] seeded %d baseline rule(s) after scope update of %s", n, project.ID)
	}
	// Live runs hold a scope snapshot at start. Replace it now so every
	// subsequent guard/dispatch/plan check uses the new boundary (XALG019).
	s.applyProjectScopeToLiveRuns(project.ID, project.Scope)
	view := s.buildProjectScopeView(r.Context(), store, project, scopeProblems(project.Scope), "")
	if view.Fence.Enforce && view.ActiveRuns > 0 {
		view.Warning = "the fence applies immediately to every subsequent approval of this project's running scans; calls already waiting for a decision keep their existing row"
	}
	writeJSONStatus(w, http.StatusOK, view)
}

func (s *Server) validateProjectScope(w http.ResponseWriter, r *http.Request, store platform.Repository, projectID string) {
	limitJSONBody(w, r)
	var req struct {
		Scope   *platform.Scope `json:"scope"`
		Targets []string        `json:"targets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	project, err := store.GetProject(r.Context(), projectID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	scope := project.Scope
	if req.Scope != nil {
		scope = *req.Scope
	}
	problems := scopeProblems(scope)
	fence := platform.Fence{}
	if scope.Fence != nil {
		fence = *scope.Fence
	}
	resp := scopeValidateResponse{
		OK:           len(problems) == 0,
		Problems:     problems,
		Entries:      scopeTargetChecks(scope, req.Targets),
		Scope:        scope,
		Fence:        fence,
		FenceBound:   fence.Enforce && s.approvalModeEnforced(),
		ApprovalMode: s.approvalModeLabel(),
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

// scopeProblems renders the fail-closed validation of a candidate scope. Empty
// means the store will accept it.
func scopeProblems(scope platform.Scope) []string {
	out := []string{}
	if err := platform.ValidateScope(scope); err != nil {
		out = append(out, strings.TrimPrefix(err.Error(), platform.ErrValidation.Error()+": "))
	}
	if scope.Fence != nil && scope.Fence.Enforce && len(scope.Targets) == 0 && len(out) == 0 {
		out = append(out, "fence is enforced but the scope has no targets")
	}
	return out
}

// scopeTargetChecks answers the two questions a dispatch would ask, in the
// order the engine asks them: is the target inside the scope (dispatch), and
// does the runtime fence accept it (approval gate). Both must pass.
func scopeTargetChecks(scope platform.Scope, targets []string) []scopeTargetCheck {
	entries := make([]scopeTargetCheck, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, raw := range targets {
		target := strings.TrimSpace(raw)
		if target == "" {
			continue
		}
		if _, dup := seen[target]; dup {
			continue
		}
		seen[target] = struct{}{}
		check := scopeTargetCheck{Target: target, InScope: true}
		var reasons []string
		if err := validateTargetsInScope([]string{target}, scope); err != nil {
			reasons = append(reasons, err.Error())
		}
		if reason := fenceTargetReason(scope, target); reason != "" {
			reasons = append(reasons, "fence: "+reason)
		}
		if len(reasons) > 0 {
			check.InScope = false
			check.Reason = strings.Join(reasons, "; ")
		}
		entries = append(entries, check)
	}
	return entries
}

// buildProjectScopeView assembles the GET/PUT payload, including the live
// active-run projection that decides whether a fence change lands mid-flight.
func (s *Server) buildProjectScopeView(ctx context.Context, store platform.Repository, project *platform.Project, problems []string, warning string) projectScopeView {
	fence := platform.Fence{}
	if project.Scope.Fence != nil {
		fence = *project.Scope.Fence
	}
	view := projectScopeView{
		ProjectID:    project.ID,
		Scope:        project.Scope,
		Fence:        fence,
		Problems:     problems,
		ApprovalMode: s.approvalModeLabel(),
		FenceBound:   fence.Enforce && s.approvalModeEnforced(),
		Warning:      warning,
	}
	runs, err := store.ListRunsByProject(ctx, project.ID)
	if err == nil {
		idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
		view.ActiveRuns = idx.activeRunCount(runs, s.pendingDispatchExists)
	}
	return view
}

// approvalModeLabel reports the configured HITL mode (off unless explicitly
// enforced — newApprovalService reads the same field).
func (s *Server) approvalModeLabel() string {
	if s.cfg != nil && strings.EqualFold(strings.TrimSpace(s.cfg.ApprovalMode), "enforce") {
		return "enforce"
	}
	return "off"
}

func (s *Server) approvalModeEnforced() bool { return s.approvalModeLabel() == "enforce" }

// applyProjectScopeToLiveRuns replaces the enforced scope snapshot of every
// live run mounted on the project. A project fence that was just turned off
// clears the snapshot; turning it on installs it. The operator's PUT is the
// explicit authorization for either direction.
func (s *Server) applyProjectScopeToLiveRuns(projectID string, scope platform.Scope) {
	if s == nil || strings.TrimSpace(projectID) == "" {
		return
	}
	var targets, deny []string
	var ports []int
	if scope.Fence != nil && scope.Fence.Enforce {
		targets = append([]string(nil), scope.Targets...)
		deny = append([]string(nil), scope.Fence.DenyTargets...)
		ports = append([]int(nil), scope.AllowPorts...)
	}

	s.liveRunsMu.RLock()
	handles := make([]*liveRunHandles, 0, len(s.liveRuns))
	for _, h := range s.liveRuns {
		if h != nil && h.scanCtx != nil && strings.TrimSpace(h.scanCtx.ProjectID) == projectID {
			handles = append(handles, h)
		}
	}
	s.liveRunsMu.RUnlock()

	for _, h := range handles {
		if h.agent != nil {
			h.agent.SetEngagementScope(targets, deny, ports)
		}
		if h.planner != nil {
			h.planner.SetScope(targets, deny, ports)
		}
		if h.pool != nil {
			h.pool.SetScope(targets, deny, ports)
		}
		if h.agent != nil {
			msg := "PROJECT SCOPE UPDATED — the authorized boundary changed. Re-read it before proposing new work: every subsequent guard/dispatch/plan check uses the new scope. In-flight requests are not retroactively cancelled."
			if len(targets) == 0 {
				msg = "PROJECT SCOPE UPDATED — the enforced fence was removed. The project scope may have expanded; new work must still follow the operator's instruction and the six-ring guards."
			}
			h.agent.NotifyMainSystem(msg)
		}
		log.Printf("[scope] run %s: live scope replaced (%d target(s), %d denied, %d port(s))", h.instanceID, len(targets), len(deny), len(ports))
	}
}
