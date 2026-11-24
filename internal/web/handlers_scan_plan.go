package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
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

	// R7/M1: the exploration graph is the plan authority. Read intent nodes
	// first; a zero-row graph falls through to the live/record path while the
	// legacy plan is still being seeded.
	if repo := graph.Backend(); repo != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		tasks, err := repo.PlanTasks(ctx, s.graphRunID(scanID))
		cancel()
		if err != nil {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
				"error": "graph plan read failed", "detail": err.Error(), "scan_id": scanID,
			})
			return
		}
		if len(tasks) > 0 {
			writeJSONStatus(w, http.StatusOK, planViewFromGraphTasks(tasks))
			return
		}
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
// planViewFromGraphTasks projects authoritative intent nodes onto the legacy
// PlanView shape (source/counters/current phase included). The graph is the
// authority; this adapter only renames fields for existing consumers.
func planViewFromGraphTasks(tasks []graph.PlanTask) agent.PlanView {
	view := agent.PlanView{
		Present: len(tasks) > 0,
		Source:  "none",
		Tasks:   make([]agent.PlanTaskView, 0, len(tasks)),
	}
	engineTasks, modelTasks := 0, 0
	activePhase, lastCompletedPhase := 0, 0
	var completed, skipped int
	for _, t := range tasks {
		origin := strings.ToLower(strings.TrimSpace(t.Origin))
		if strings.HasPrefix(origin, "llm") || origin == "model" {
			modelTasks++
		} else {
			engineTasks++
		}
		switch t.Status {
		case "active":
			if t.Phase > activePhase {
				activePhase = t.Phase
			}
		case "completed":
			completed++
			if t.Phase > lastCompletedPhase {
				lastCompletedPhase = t.Phase
			}
		case "skipped":
			skipped++
		}
		status := t.Status
		if status == "" {
			status = "pending"
		}
		var lease time.Time
		if t.LeaseUntil != nil {
			lease = *t.LeaseUntil
		}
		view.Tasks = append(view.Tasks, agent.PlanTaskView{
			ID:           t.TaskID,
			Title:        t.Title,
			Phase:        t.Phase,
			VulnClass:    t.VulnClass,
			Endpoint:     t.Endpoint,
			Status:       status,
			DependsOn:    append([]string(nil), t.DependsOn...),
			Notes:        t.Notes,
			Disposition:  t.Disposition,
			Origin:       t.Origin,
			Prerequisite: t.Prerequisite,
			WholeTarget:  t.WholeTarget,
			Owner:        t.Owner,
			LeaseUntil:   lease,
		})
	}
	switch {
	case engineTasks > 0 && modelTasks > 0:
		view.Source = "mixed"
	case modelTasks > 0:
		view.Source = "model"
	case engineTasks > 0:
		view.Source = "engine"
	}
	unfinished := len(tasks) - completed - skipped
	if unfinished < 0 {
		unfinished = 0
	}
	view.Counters = agent.PlanCountersView{
		Total:      len(tasks),
		Completed:  completed,
		Skipped:    skipped,
		Unfinished: unfinished,
	}
	if activePhase > 0 {
		view.CurrentPhase = activePhase
	} else {
		view.CurrentPhase = lastCompletedPhase
	}
	return view
}

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
