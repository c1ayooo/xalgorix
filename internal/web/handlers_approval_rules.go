package web

// handlers_approval_rules.go — REST surface for operator-authored approval
// rules ("approve & remember", REDESIGN §5.2, docs/API_CONTRACT.md §3.2):
//
//	GET    /api/approval-rules   ?project_id=&kind=  → {rules: [...]}
//	POST   /api/approval-rules   {kind, match, project_id, reason?, expires_at?,
//	                              created_from_approval_id?} → ApprovalRule
//	DELETE /api/approval-rules/{id}                  → {ok: true}
//
// The gate layers consult the rules through the repository directly — no
// cache; the shared 3s store deadline bounds every read. Creation and
// deletion broadcast approval_rules_changed dashboard-wide (guidance_queued
// precedent) so other tabs refetch. created_by carries the same
// "operator:<user>" vocabulary as approval decided_by. Errors map through
// writePlatformError (404/409/400); nil store → 503.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// handleApprovalRules serves GET (list) and POST (create) on /api/approval-rules.
func (s *Server) handleApprovalRules(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleApprovalRulesList(w, r, store)
	case http.MethodPost:
		s.handleApprovalRulesCreate(w, r, store)
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleApprovalRulesList(w http.ResponseWriter, r *http.Request, store platform.Repository) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "" && kind != platform.ApprovalKindCommand && kind != platform.ApprovalKindHTTP {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "kind must be command or http"})
		return
	}
	rules, err := store.ListApprovalRules(r.Context(), platform.ApprovalRuleFilter{
		ProjectID: q.Get("project_id"),
		Kind:      kind,
	})
	if err != nil {
		writePlatformError(w, err)
		return
	}
	s.projectRuleNames(r.Context(), store, rules)
	writeJSONStatus(w, http.StatusOK, map[string]any{"rules": rules})
}

// handleApprovalRulesCreate creates one rule (POST body). The dispatcher has
// already validated the method and the platform store.
func (s *Server) handleApprovalRulesCreate(w http.ResponseWriter, r *http.Request, store platform.Repository) {
	limitJSONBody(w, r)
	limitJSONBody(w, r)
	var req struct {
		ProjectID             string                     `json:"project_id"`
		Kind                  string                     `json:"kind"`
		Match                 platform.ApprovalRuleMatch `json:"match"`
		Reason                string                     `json:"reason"`
		ExpiresAt             string                     `json:"expires_at"` // RFC3339, optional
		CreatedFromApprovalID string                     `json:"created_from_approval_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	var expires *time.Time
	if raw := strings.TrimSpace(req.ExpiresAt); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "expires_at must be RFC3339"})
			return
		}
		expires = &t
	}
	rule, err := store.CreateApprovalRule(r.Context(), platform.ApprovalRuleInput{
		ProjectID:             req.ProjectID,
		Kind:                  req.Kind,
		Match:                 req.Match,
		Reason:                req.Reason,
		CreatedBy:             s.operatorIdentity(),
		CreatedFromApprovalID: req.CreatedFromApprovalID,
		ExpiresAt:             expires,
	})
	if err != nil {
		writePlatformError(w, err)
		return
	}
	if project, perr := store.GetProject(r.Context(), rule.ProjectID); perr == nil {
		rule.ProjectName = project.Name
	}
	s.broadcastDashboard(WSEvent{
		Type:    "approval_rules_changed",
		Content: "Approval rule created: " + ruleDisplayText(rule),
		Rule:    rule,
	})
	writeJSONStatus(w, http.StatusOK, rule)
}

// handleApprovalRuleDetail serves DELETE /api/approval-rules/{id}.
func (s *Server) handleApprovalRuleDetail(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	if r.Method != http.MethodDelete {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/approval-rules/")
	if id == "" || strings.Contains(id, "/") {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "approval rule not found"})
		return
	}
	if err := store.DeleteApprovalRule(r.Context(), id); err != nil {
		writePlatformError(w, err)
		return
	}
	s.broadcastDashboard(WSEvent{
		Type:    "approval_rules_changed",
		Content: "Approval rule deleted: " + id,
	})
	writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true})
}

// operatorIdentity mirrors decideApproval's "operator:<user>" vocabulary.
func (s *Server) operatorIdentity() string {
	if cfg := s.configSnapshot(); cfg != nil && strings.TrimSpace(cfg.Username) != "" {
		return "operator:" + cfg.Username
	}
	return "operator"
}

// projectRuleNames fills the list-only project_name projection the rules
// dialog renders (same posture as projectApprovalNames).
func (s *Server) projectRuleNames(ctx context.Context, store platform.Repository, rules []platform.ApprovalRule) {
	names := map[string]string{}
	for i := range rules {
		r := &rules[i]
		if r.ProjectID == "" {
			continue
		}
		if name, ok := names[r.ProjectID]; ok {
			r.ProjectName = name
			continue
		}
		if project, err := store.GetProject(ctx, r.ProjectID); err == nil {
			names[r.ProjectID] = project.Name
			r.ProjectName = project.Name
		} else {
			names[r.ProjectID] = ""
		}
	}
}
