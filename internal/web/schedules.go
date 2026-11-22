package web

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// scheduleIDPattern validates schedule IDs to prevent path traversal.
var scheduleIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// handleSchedules handles GET /api/schedules and POST /api/schedules
func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		s.schedulesMu.RLock()
		defer s.schedulesMu.RUnlock()
		list := make([]*ScanSchedule, 0, len(s.schedules))
		for _, sch := range s.schedules {
			list = append(list, sch)
		}
		// Sort by Name alphabetically
		sort.Slice(list, func(i, j int) bool {
			return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
		})
		_ = json.NewEncoder(w).Encode(list)
		return
	case http.MethodPost:
		var req ScanSchedule
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := s.validateScanWebhook(req.DiscordWebhook); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		normalizeScheduleActivity(&req)
		if err := normalizeScheduleTiming(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateScheduleTargets(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			if len(req.Targets) > 0 {
				req.Name = "Scheduled Scan " + strings.Join(req.Targets, ", ")
			} else {
				req.Name = "Scheduled " + req.mode()
			}
		}
		if req.Interval == "" {
			req.Interval = "daily"
		}
		req.ID = randomSlug()
		req.Enabled = true
		req.NextRun = calculateNextRun(&req, time.Now())

		s.schedulesMu.Lock()
		s.schedules[req.ID] = &req
		diskCopy := req // snapshot under lock for race-free disk write
		s.schedulesMu.Unlock()

		if err := s.saveScheduleToDisk(&diskCopy); err != nil {
			log.Printf("[SCHEDULER] Error saving schedule to disk: %v", err)
			http.Error(w, "failed to save schedule: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Link the fresh schedule to its schedule task immediately (idempotent
		// find-or-create): without it the scheduler-entry scope check would
		// skip every tick of this schedule until the next restart re-imports.
		s.ensureScheduleTask(&req)
		_ = json.NewEncoder(w).Encode(req)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// handleScheduleDetail handles GET /api/schedules/{id}, PUT /api/schedules/{id}, DELETE /api/schedules/{id}, and POST /api/schedules/{id}/trigger
func (s *Server) handleScheduleDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api/schedules/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	id := parts[0]
	if !scheduleIDPattern.MatchString(id) {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}

	s.schedulesMu.RLock()
	sch, exists := s.schedules[id]
	s.schedulesMu.RUnlock()

	if !exists {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}

	// Handle trigger action
	if len(parts) > 1 && parts[1] == "trigger" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Snapshot the schedule under the read lock; everything below runs
		// lock-free so the platform work (task reverse-lookup, scope check,
		// run mount) cannot hold /api/schedules hostage.
		s.schedulesMu.RLock()
		snapshot := *sch
		s.schedulesMu.RUnlock()

		// Same scheduler-entry closure as the tick: schedule targets must
		// fall inside the linked project's scope (D7). The authorization
		// runs on a detached context (handleTaskRunStart mount precedent): a
		// client that disconnects mid-request must not cancel the store
		// lookups and turn the decision into whatever the error fallback
		// would answer. Unlike the tick there is no retry storm to damp, so
		// a refusal is a synchronous 400 and LastRun stays untouched; a
		// transient store failure is 503 (retryable, not a config error).
		authCtx, authCancel := context.WithTimeout(context.Background(), 5*time.Second)
		target, authErr := s.authorizeScheduledScan(authCtx, &snapshot)
		authCancel()
		if authErr != nil {
			if errors.Is(authErr, errScheduleLinkUnavailable) {
				http.Error(w, authErr.Error(), http.StatusServiceUnavailable)
				return
			}
			http.Error(w, authErr.Error(), http.StatusBadRequest)
			return
		}
		taskID := target.TaskID
		// A manual trigger of a create_task schedule mints the task now, so the
		// operator sees the exact task the run will accumulate into.
		if snapshot.mode() == ScheduleModeCreateTask && target.Project != nil {
			created, err := s.createTaskForSchedule(&snapshot, target.Project.ID)
			if err != nil {
				http.Error(w, "failed to create task: "+err.Error(), http.StatusInternalServerError)
				return
			}
			taskID = created.ID
		}

		req := scanRequestForSchedule(&snapshot)
		if len(req.Targets) == 0 && target.Project != nil && len(target.Project.Scope.Targets) > 0 {
			req.Targets = append([]string(nil), target.Project.Scope.Targets...)
		}
		if len(snapshot.ContextPaths) > 0 {
			req.ScanContextPaths = append([]string(nil), snapshot.ContextPaths...)
		}
		if taskID != "" {
			if store := s.currentPlatformStore(); store != nil {
				taskCtx, taskCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if task, err := store.GetTask(taskCtx, taskID); err == nil && len(task.ContextPaths) > 0 {
					req.ScanContextPaths = append([]string(nil), task.ContextPaths...)
				}
				taskCancel()
			}
		}
		scanCfg := s.configSnapshot()
		if scanCfg == nil {
			http.Error(w, "server configuration unavailable", http.StatusServiceUnavailable)
			return
		}
		if snapshot.Model != "" {
			scanCfg.LLM = snapshot.Model
		}

		instanceID := s.dispatchScheduledScan(req, scanCfg, &snapshot, taskID, "schedule")

		s.schedulesMu.Lock()
		sch.LastRun = time.Now()
		// A manual trigger also rides the execution window: record the
		// dispatch so the window closer can pause/stop it.
		if hasExecutionWindow(&snapshot) && instanceID != "" {
			sch.RuntimeInstanceID = instanceID
		}
		diskCopy := *sch // snapshot under lock for race-free disk write
		s.schedulesMu.Unlock()
		if err := s.saveScheduleToDisk(&diskCopy); err != nil {
			log.Printf("[SCHEDULER] Failed to persist schedule %s after manual trigger: %v", diskCopy.ID, err)
		}

		_ = json.NewEncoder(w).Encode(map[string]string{"status": "triggered", "instance_id": instanceID})
		return
	}

	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(sch)
		return

	case http.MethodPut:
		var req ScanSchedule
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := s.validateScanWebhook(req.DiscordWebhook); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		normalizeScheduleActivity(&req)
		if err := normalizeScheduleTiming(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateScheduleTargets(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.schedulesMu.Lock()
		oldEnabled := sch.Enabled
		oldTiming := sch.timing()

		sch.Name = req.Name
		sch.Interval = req.Interval
		sch.RunAt = req.RunAt
		sch.RunDay = req.RunDay
		sch.Timezone = req.Timezone
		sch.Enabled = req.Enabled
		sch.Targets = req.Targets
		sch.Instruction = req.Instruction
		sch.ScanMode = req.ScanMode
		sch.SeverityFilter = req.SeverityFilter
		sch.Phases = req.Phases
		sch.ReconMode = req.ReconMode
		sch.ScanIntensity = req.ScanIntensity
		sch.CompanyName = req.CompanyName
		sch.LogoPath = req.LogoPath
		sch.DiscordWebhook = req.DiscordWebhook
		sch.Model = req.Model
		// v4.6: project binding, target mode and the execution window.
		sch.ProjectID = req.ProjectID
		sch.Mode = req.Mode
		sch.TaskID = req.TaskID
		sch.TaskGoal = req.TaskGoal
		sch.ContextPaths = req.ContextPaths
		sch.WindowStart = req.WindowStart
		sch.WindowEnd = req.WindowEnd
		sch.WindowEndAction = req.WindowEndAction
		// A window change invalidates the previous close bookkeeping so the
		// next end instant is acted on.
		sch.WindowClosedOn = ""

		// If any timing field changed, or enabled transitioned false -> true, recalculate NextRun
		if sch.timing() != oldTiming || (sch.Enabled && !oldEnabled) {
			sch.NextRun = calculateNextRun(sch, time.Now())
		}

		diskCopy := *sch // snapshot under lock for race-free disk write
		s.schedulesMu.Unlock()

		if err := s.saveScheduleToDisk(&diskCopy); err != nil {
			http.Error(w, "failed to save schedule: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Re-link after every update (idempotent find-or-create): the task's
		// legacy fields mirror the schedule, and an update that landed while
		// the linkage was missing heals here instead of skipping forever.
		s.ensureScheduleTask(&diskCopy)
		_ = json.NewEncoder(w).Encode(&diskCopy)
		return

	case http.MethodDelete:
		s.schedulesMu.Lock()
		delete(s.schedules, id)
		s.schedulesMu.Unlock()

		if err := s.deleteScheduleFromDisk(id); err != nil {
			http.Error(w, "failed to delete schedule: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
