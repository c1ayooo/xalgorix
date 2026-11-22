package web

// handlers_task_objective.go — GET /api/tasks/{id}/objective (REDESIGN §3.1
// "验收说明"): the structured objective of a task plus its honest evaluation.
//
// The response is read-only. It never accepts a finding, never relabels one,
// and never blocks a scan: it reports what the report pipeline already
// accepted and says "not structured" when the task has no objective.

import (
	"net/http"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// handleTaskObjective serves GET /api/tasks/{id}/objective.
func (s *Server) handleTaskObjective(w http.ResponseWriter, r *http.Request, store platform.Repository, taskID string) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	task, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	report, err := s.evaluateTaskObjective(r.Context(), store, task)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, report)
}
