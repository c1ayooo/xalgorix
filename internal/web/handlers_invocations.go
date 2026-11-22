package web

// handlers_invocations.go — read-only REST surface for the cross-scan
// integration invocation records (docs/API_CONTRACT.md §integration-invocations):
//
//	GET /api/mcp-servers/invocations        ?page=&size=&instance_id=&name=&status=
//	GET /api/skills/invocations             ?page=&size=&instance_id=&name=&status=
//	GET /api/agent-profiles/invocations     ?page=&size=&instance_id=&name=&status=
//
// The records are the web layer's best-effort retrieval projection of the
// per-scan domain journal invocation audit (charter §5) — reads never fail a
// scan and a missing journal file is an empty list, not an error. Pagination
// follows the standard envelope {items, total, page, size}. An invalid
// status filter is 400; unknown query params are ignored (list-endpoint
// convention). No platform store is consulted: records live in the web
// layer's own JSONL journals, so they work identically with and without a
// DSN.

import (
	"net/http"
	"strings"
)

// handleMcpInvocations serves GET /api/mcp-servers/invocations.
func (s *Server) handleMcpInvocations(w http.ResponseWriter, r *http.Request) {
	s.handleInvocationList(w, r, InvocationModuleMCP)
}

// handleSkillInvocations serves GET /api/skills/invocations.
func (s *Server) handleSkillInvocations(w http.ResponseWriter, r *http.Request) {
	s.handleInvocationList(w, r, InvocationModuleSkills)
}

// handleAgentInvocations serves GET /api/agent-profiles/invocations.
func (s *Server) handleAgentInvocations(w http.ResponseWriter, r *http.Request) {
	s.handleInvocationList(w, r, InvocationModuleAgents)
}

// handleInvocationList is the shared reader: method guard, filter parsing,
// paged envelope.
func (s *Server) handleInvocationList(w http.ResponseWriter, r *http.Request, module string) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	q := r.URL.Query()
	status := strings.TrimSpace(q.Get("status"))
	if status != "" && status != InvocationStatusOK && status != InvocationStatusError {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "status must be ok or error"})
		return
	}
	page, size := parsePageParams(q.Get("page"), q.Get("size"))
	items, total, err := listInvocations(module, page, size,
		strings.TrimSpace(q.Get("instance_id")),
		strings.TrimSpace(q.Get("name")),
		status)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "failed to read invocation records"})
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
		"page":  page,
		"size":  size,
	})
}
