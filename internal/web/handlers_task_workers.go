package web

import (
	"net/http"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// taskWorkerRunView is one run's delegated-worker history in the task-scoped
// session rail. It embeds the run projection (title/status/timestamps) so the
// panel can render "run → workers" without a second round trip, and keeps the
// worker pool under its own key so the per-instance projection shape is reused
// verbatim.
type taskWorkerRunView struct {
	platform.Run
	Workers instanceAgentsView `json:"workers"`
}

// handleTaskWorkers serves GET /api/tasks/{id}/workers — the task-scoped worker
// history the session rail lists. Each run's workers come from the same
// authority as GET /api/instances/{id}/agents (live agentsgraph, then the
// durable execution-graph checkpoint), so completed/failed workers stay visible
// after they stop producing output. Runs that never delegated are omitted
// instead of rendering an empty section; the selected run still has its own
// per-instance projection in the Workers module.
func (s *Server) handleTaskWorkers(w http.ResponseWriter, r *http.Request, store platform.Repository, taskID string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	page, size := parsePageParams(r.URL.Query().Get("page"), r.URL.Query().Get("size"))
	runs, total, err := store.ListRuns(r.Context(), taskID, page, size)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	// Same read-time projection the run list uses: status/timestamps are not
	// stored on the mount row.
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	idx.projectRuns(runs, s.pendingDispatchExists)

	items := make([]taskWorkerRunView, 0, len(runs))
	for _, run := range runs {
		if run.InstanceID == "" {
			continue
		}
		workers := s.instanceAgentsView(run.InstanceID)
		if len(workers.Agents) == 0 {
			continue
		}
		items = append(items, taskWorkerRunView{Run: run, Workers: workers})
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
		"page":  page,
		"size":  size,
	})
}
