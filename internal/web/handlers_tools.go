package web

// handlers_tools.go — read-only REST surface for the built-in tool catalog and
// the built-in tool invocation records:
//
//	GET /api/tools               → {tools: [...], categories: [...]}
//	GET /api/tools/invocations   ?page=&size=&instance_id=&name=&status=
//
// The catalog is a static directory (tools_catalog.go) of what the scan agent
// registers; it never reflects per-scan enablement. The invocation list reuses
// the cross-scan retrieval projection (invocation_journal.go) — a best-effort
// JSONL projection whose per-scan authority is the domain journal. Reads never
// fail a scan and a missing journal file is an empty list, not an error.

import (
	"net/http"
	"strings"
)

// handleToolInvocations serves GET /api/tools/invocations.
func (s *Server) handleToolInvocations(w http.ResponseWriter, r *http.Request) {
	s.handleInvocationList(w, r, InvocationModuleTools)
}

// handleToolCatalog serves GET /api/tools. Optional filters: category (exact
// key) and q (case-insensitive substring over name/description).
func (s *Server) handleToolCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	q := r.URL.Query()
	category := strings.TrimSpace(q.Get("category"))
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))

	all := builtinToolCatalog()
	tools := make([]ToolCatalogEntry, 0, len(all))
	for _, entry := range all {
		if category != "" && entry.Category != category {
			continue
		}
		if search != "" {
			name := strings.ToLower(entry.Name)
			desc := strings.ToLower(entry.Description)
			if !strings.Contains(name, search) && !strings.Contains(desc, search) {
				continue
			}
		}
		tools = append(tools, entry)
	}
	writeJSONStatus(w, http.StatusOK, toolCatalogResponse{
		Tools:      tools,
		Categories: toolCatalogCategories(all),
	})
}
