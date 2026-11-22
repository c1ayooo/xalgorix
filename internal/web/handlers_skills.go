package web

// handlers_skills.go — REST surface for operator-authored skills
// (docs/API_CONTRACT.md §integration-skills):
//
//	GET    /api/skills   → {skills: [...]}          (user skills only; the embedded corpus is engine-side)
//	POST   /api/skills   {name, description, tags?, intent?, body, enabled} → Skill
//	GET    /api/skills/{id}        → Skill
//	PATCH  /api/skills/{id}        {name?, description?, tags?, intent?, body?, enabled?} → Skill
//	DELETE /api/skills/{id}        → {ok: true}
//
// Errors map through writePlatformError (ErrValidation→400, ErrConflict→409
// for duplicate names, ErrNotFound→404); nil store → 503. Creation, update
// and deletion broadcast skills_changed dashboard-wide so open tabs refetch.
// Created_by carries the operator:<user> vocabulary (approval rules
// precedent). Disabled skills are configuration-visible only — the engine
// receives enabled skills at scan launch (integration_inputs.go).

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// handleSkills serves GET (list) and POST (create) on /api/skills.
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		skills, err := store.ListSkills(r.Context())
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, map[string]any{"skills": skills})
	case http.MethodPost:
		s.handleSkillsCreate(w, r, store)
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleSkillsCreate(w http.ResponseWriter, r *http.Request, store platform.Repository) {
	limitJSONBody(w, r)
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Intent      string   `json:"intent"`
		Body        string   `json:"body"`
		Enabled     bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	skill, err := store.CreateSkill(r.Context(), platform.SkillInput{
		Name:        req.Name,
		Description: req.Description,
		Tags:        req.Tags,
		Intent:      req.Intent,
		Body:        req.Body,
		Enabled:     req.Enabled,
		CreatedBy:   s.operatorIdentity(),
	})
	if err != nil {
		writePlatformError(w, err)
		return
	}
	s.broadcastDashboard(WSEvent{
		Type:    "skills_changed",
		Content: "User skill created: " + skill.Name,
	})
	writeJSONStatus(w, http.StatusCreated, skill)
}

// handleSkillDetail serves GET / PATCH / DELETE on /api/skills/{id}.
func (s *Server) handleSkillDetail(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/skills/")
	if id == "" || strings.Contains(id, "/") {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "skill not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		skill, err := store.GetSkill(r.Context(), id)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, skill)
	case http.MethodPatch:
		limitJSONBody(w, r)
		var req struct {
			Name        *string  `json:"name"`
			Description *string  `json:"description"`
			Tags        []string `json:"tags"`
			Intent      *string  `json:"intent"`
			Body        *string  `json:"body"`
			Enabled     *bool    `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		skill, err := store.UpdateSkill(r.Context(), id, platform.SkillUpdate{
			Name:        req.Name,
			Description: req.Description,
			Tags:        req.Tags,
			Intent:      req.Intent,
			Body:        req.Body,
			Enabled:     req.Enabled,
		})
		if err != nil {
			writePlatformError(w, err)
			return
		}
		s.broadcastDashboard(WSEvent{
			Type:    "skills_changed",
			Content: "User skill updated: " + skill.Name,
		})
		writeJSONStatus(w, http.StatusOK, skill)
	case http.MethodDelete:
		if err := store.DeleteSkill(r.Context(), id); err != nil {
			writePlatformError(w, err)
			return
		}
		s.broadcastDashboard(WSEvent{
			Type:    "skills_changed",
			Content: "User skill deleted: " + id,
		})
		writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}
