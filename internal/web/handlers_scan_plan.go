package web

import (
	"net/http"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
)

// handleScanPlan serves GET /api/scans/{id}/plan — the read projection of the
// root plan (charter §5.1/§6: the plan is state, not telemetry).
//
// Resolution order, each step strictly more degraded than the previous:
//  1. live agent's published plan snapshot (most current, race-free);
//  2. the terminal ScanRecord.PlanView captured at the same safe point as the
//     plan counters (survives the agent being released and process restarts);
//  3. counters-only fallback flagged partial:"not_persisted" (runs whose plan
//     detail predates the projection) — never an empty plan pretending to be
//     complete;
//  4. present:false (no plan was ever built).
func (s *Server) handleScanPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/plan")
	if scanID == "" || strings.Contains(scanID, "/") {
		http.Error(w, `{"error":"scan id is required"}`, http.StatusBadRequest)
		return
	}

	// A live agent without a published plan yet (fresh start, or resumed and
	// not through its first turn) must not blank a plan the record already
	// knows about — fall through to the persisted projection instead.
	if view, ok := s.livePlanView(scanID); ok && view.Present {
		writeJSONStatus(w, http.StatusOK, view)
		return
	}

	dir, ok := s.resolveScanDirByID(scanID)
	if !ok {
		http.Error(w, `{"error":"scan not found"}`, http.StatusNotFound)
		return
	}
	rec, ok := loadScanRecordFromDir(dir)
	if !ok {
		http.Error(w, `{"error":"scan record unavailable"}`, http.StatusNotFound)
		return
	}
	writeJSONStatus(w, http.StatusOK, planViewFromRecord(rec))
}

// livePlanView returns the live agent's plan projection when this instance is
// still attached to a running (or paused) session.
func (s *Server) livePlanView(instanceID string) (agent.PlanView, bool) {
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		return agent.PlanView{}, false
	}
	inst.mu.RLock()
	live := inst.agent
	inst.mu.RUnlock()
	if live == nil {
		return agent.PlanView{}, false
	}
	return live.PlanSnapshot(), true
}

// planViewFromRecord projects a persisted record. Counters are authoritative
// even when the task detail was never captured; the partial flag keeps that
// distinction visible instead of rendering an empty plan.
func planViewFromRecord(rec *ScanRecord) agent.PlanView {
	if rec == nil {
		return agent.PlanView{Source: "none", Tasks: []agent.PlanTaskView{}}
	}
	if rec.PlanView != nil {
		view := *rec.PlanView
		if view.Tasks == nil {
			view.Tasks = []agent.PlanTaskView{}
		}
		return view
	}
	present := rec.PlanPresent || rec.PlanTasksTotal > 0
	view := agent.PlanView{
		Present: present,
		Source:  "none",
		Counters: agent.PlanCountersView{
			Total:      rec.PlanTasksTotal,
			Completed:  rec.PlanTasksCompleted,
			Skipped:    rec.PlanTasksSkipped,
			Unfinished: rec.PlanTasksUnfinished,
		},
		Tasks: []agent.PlanTaskView{},
	}
	if present {
		view.Partial = true
		view.PartialReason = "not_persisted"
	}
	return view
}
