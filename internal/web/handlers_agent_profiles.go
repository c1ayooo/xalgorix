package web

// handlers_agent_profiles.go — REST surface for operator-authored agent
// delegation profiles (docs/API_CONTRACT.md §integration-agent-profiles):
//
//	GET    /api/agent-profiles   → {profiles: [...]}   (builtin rows merged in, read-only)
//	POST   /api/agent-profiles   {role, focus, vuln_classes?, evidence_contract, stopping_rule, tool_scope_hidden?, enabled} → AgentProfile
//	GET    /api/agent-profiles/{id}          → AgentProfile
//	PATCH  /api/agent-profiles/{id}          → AgentProfile
//	DELETE /api/agent-profiles/{id}          → {ok: true}
//
// Builtin specialist profiles (recon-discovery / authz-logic / business-logic
// / injection-serverside / client-source) are code-defined: GET merges them
// in as builtin:true rows, and every mutation against a builtin id/role is
// rejected (404 by id — builtin rows have no store id; 409 by role on
// create). Errors map through writePlatformError; nil store → 503. Mutations
// broadcast agent_profiles_changed dashboard-wide.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/xalgord/xalgorix/v4/internal/agent"
	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// builtinProfileRows renders the code-defined specialists as read-only wire
// rows (fresh IDs per response — they are not store rows).
func builtinProfileRows() []platform.AgentProfile {
	roles := agent.BuiltinSpecialistRoles()
	out := make([]platform.AgentProfile, 0, len(roles))
	// The contract text lives in the agent package's renderSpecialistProfiles;
	// the wire rows expose the identity surface the UI needs (role + builtin
	// flag). Focus/contract details stay engine-owned to avoid duplicating
	// the canonical strings here.
	for _, role := range roles {
		out = append(out, platform.AgentProfile{
			ID:      "builtin:" + role,
			Role:    role,
			Builtin: true,
			Enabled: true,
		})
	}
	return out
}

// isBuiltinRole reports whether a role name collides with a code-defined
// specialist.
func isBuiltinRole(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	for _, r := range agent.BuiltinSpecialistRoles() {
		if r == role {
			return true
		}
	}
	return false
}

// handleAgentProfiles serves GET (merged list) and POST (create) on
// /api/agent-profiles.
func (s *Server) handleAgentProfiles(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		profiles, err := store.ListAgentProfiles(r.Context())
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, map[string]any{"profiles": append(builtinProfileRows(), profiles...)})
	case http.MethodPost:
		s.handleAgentProfilesCreate(w, r, store)
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleAgentProfilesCreate(w http.ResponseWriter, r *http.Request, store platform.Repository) {
	limitJSONBody(w, r)
	var req struct {
		Role             string   `json:"role"`
		Focus            string   `json:"focus"`
		VulnClasses      []string `json:"vuln_classes"`
		EvidenceContract string   `json:"evidence_contract"`
		StoppingRule     string   `json:"stopping_rule"`
		ToolScopeHidden  []string `json:"tool_scope_hidden"`
		Enabled          bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if isBuiltinRole(req.Role) {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "role is reserved by a built-in specialist profile"})
		return
	}
	profile, err := store.CreateAgentProfile(r.Context(), platform.AgentProfileInput{
		Role:             req.Role,
		Focus:            req.Focus,
		VulnClasses:      req.VulnClasses,
		EvidenceContract: req.EvidenceContract,
		StoppingRule:     req.StoppingRule,
		ToolScopeHidden:  req.ToolScopeHidden,
		Enabled:          req.Enabled,
		CreatedBy:        s.operatorIdentity(),
	})
	if err != nil {
		writePlatformError(w, err)
		return
	}
	s.broadcastDashboard(WSEvent{
		Type:    "agent_profiles_changed",
		Content: "Agent profile created: " + profile.Role,
	})
	writeJSONStatus(w, http.StatusCreated, profile)
}

// handleAgentProfileDetail serves GET / PATCH / DELETE on
// /api/agent-profiles/{id}. Builtin pseudo-ids ("builtin:<role>") are 404 —
// builtin rows have no store identity to mutate.
func (s *Server) handleAgentProfileDetail(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/agent-profiles/")
	if id == "" || strings.Contains(id, "/") || strings.HasPrefix(id, "builtin:") {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "agent profile not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		profile, err := store.GetAgentProfile(r.Context(), id)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, profile)
	case http.MethodPatch:
		limitJSONBody(w, r)
		var req struct {
			Role             *string  `json:"role"`
			Focus            *string  `json:"focus"`
			VulnClasses      []string `json:"vuln_classes"`
			EvidenceContract *string  `json:"evidence_contract"`
			StoppingRule     *string  `json:"stopping_rule"`
			ToolScopeHidden  []string `json:"tool_scope_hidden"`
			Enabled          *bool    `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if req.Role != nil && isBuiltinRole(*req.Role) {
			writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "role is reserved by a built-in specialist profile"})
			return
		}
		profile, err := store.UpdateAgentProfile(r.Context(), id, platform.AgentProfileUpdate{
			Role:             req.Role,
			Focus:            req.Focus,
			VulnClasses:      req.VulnClasses,
			EvidenceContract: req.EvidenceContract,
			StoppingRule:     req.StoppingRule,
			ToolScopeHidden:  req.ToolScopeHidden,
			Enabled:          req.Enabled,
		})
		if err != nil {
			writePlatformError(w, err)
			return
		}
		s.broadcastDashboard(WSEvent{
			Type:    "agent_profiles_changed",
			Content: "Agent profile updated: " + profile.Role,
		})
		writeJSONStatus(w, http.StatusOK, profile)
	case http.MethodDelete:
		if err := store.DeleteAgentProfile(r.Context(), id); err != nil {
			writePlatformError(w, err)
			return
		}
		s.broadcastDashboard(WSEvent{
			Type:    "agent_profiles_changed",
			Content: "Agent profile deleted: " + id,
		})
		writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}
