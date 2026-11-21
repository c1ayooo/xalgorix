package web

// R1 platform API surface (REDESIGN D6/D7, docs/API_CONTRACT.md §1/§2/§3.2):
//
//	GET/POST   /api/projects
//	GET/PATCH/DELETE /api/projects/{id}
//	GET/POST   /api/projects/{id}/tasks
//	GET/PATCH/DELETE /api/tasks/{id}
//	POST/GET   /api/tasks/{id}/runs        (POST dispatches through the same
//	                                        durable steps as POST /api/scan)
//	GET        /api/tasks/{id}/findings
//
// The platform repository is optional (nil = 503, same posture as the
// cold-path views); scanning never depends on it (charter §6.6). Runs are
// mount records only — status/timing/counts are projected here from
// findAllScanSummaries at read time so scan.json stays the single status
// authority (charter §5.1, REDESIGN §3.1).
//
// D7 five-dimension isolation status in this milestone (REDESIGN §1 D7):
//   - row level: every repository list/filter takes projectID (enforced);
//   - scope: project scope.targets validated on every POST /runs (enforced);
//   - credentials: R1 deliberately leaves the global auth-profiles.json
//     untouched — the project-private credential table ships with the R2
//     approvals/proxy batch, so this surface writes no credential material;
//   - approvals: no approvals API exists yet (R2); the future GET
//     /api/approvals project_id filter will ride on the same repository seam;
//   - UI: /api/projects|tasks carry the projected counters the project
//     switcher and task pages render.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// runProjectionPageSize caps how many runs a single task view / findings
// projection loads. Mount records are tiny rows; a task with more than this
// many runs needs the pagination endpoint anyway.
const runProjectionPageSize = 500

// SetPlatformStore wires the optional platform repository. Called at startup;
// nil (the default) keeps every platform endpoint at 503 while the rest of
// the dashboard keeps serving (mirrors SetColdPathSyncer). The wiring moment
// also triggers the approval gate's one-shot startup reconciliation
// (approvals_gate.go, MEDIUM-1): no approval waiter survives a process
// restart, so pending rows found here are ghosts.
func (s *Server) SetPlatformStore(store platform.Repository) {
	s.platformStoreMu.Lock()
	s.platformStore = store
	s.platformStoreMu.Unlock()
	// Reconciliation reads the store through currentPlatformStore; it must run
	// after the write lock is released or the RLock deadlocks startup/tests.
	if store != nil {
		if svc := s.approvalService(); svc != nil {
			svc.ReconcilePending()
		}
	}
}

func (s *Server) currentPlatformStore() platform.Repository {
	s.platformStoreMu.RLock()
	defer s.platformStoreMu.RUnlock()
	return s.platformStore
}

// requirePlatformStore answers 503 when the repository is missing. Returns
// the store (and true) when present.
func (s *Server) requirePlatformStore(w http.ResponseWriter) (platform.Repository, bool) {
	store := s.currentPlatformStore()
	if store == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{
			"error": "platform store not initialized",
		})
		return nil, false
	}
	return store, true
}

// writePlatformError maps repository errors onto the {"error": ...} envelope
// with contract status codes (404/409/400/500). The "platform: sentinel: "
// wrapper is stripped so operators see the reason, not the sentinel name.
func writePlatformError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, platform.ErrNotFound):
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": friendlyPlatformError(err)})
	case errors.Is(err, platform.ErrConflict):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": friendlyPlatformError(err)})
	case errors.Is(err, platform.ErrValidation):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": friendlyPlatformError(err)})
	default:
		log.Printf("[platform] repository error: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "platform operation failed"})
	}
}

func friendlyPlatformError(err error) string {
	msg := err.Error()
	for _, prefix := range []string{"platform: conflict: ", "platform: not found: ", "platform: invalid input: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}

// ── scan-summary projection (decision: Run status is projected, never stored) ──

// scanSummaryIndex indexes the shared events-free scan walk by the two mount
// keys: instance_id (preferred) and scan record ID (legacy fallback).
type scanSummaryIndex struct {
	byInstance map[string]*ScanRecord
	byScanID   map[string]*ScanRecord
}

// scanSummaryIndexFor builds the index from one shared findAllScanSummaries
// walk. Records are shallow cached copies — read-only use only.
func (s *Server) scanSummaryIndexFor(entries []scanEntry) *scanSummaryIndex {
	idx := &scanSummaryIndex{
		byInstance: make(map[string]*ScanRecord, len(entries)),
		byScanID:   make(map[string]*ScanRecord, len(entries)),
	}
	for i := range entries {
		rec := &entries[i].rec
		key := strings.TrimSpace(rec.InstanceID)
		if key == "" {
			key = rec.ID
		}
		if key != "" {
			if _, ok := idx.byInstance[key]; !ok {
				idx.byInstance[key] = rec
			}
		}
		if rec.ID != "" {
			if _, ok := idx.byScanID[rec.ID]; !ok {
				idx.byScanID[rec.ID] = rec
			}
		}
	}
	return idx
}

func (idx *scanSummaryIndex) lookup(instanceID, scanID string) *ScanRecord {
	if instanceID != "" {
		if rec, ok := idx.byInstance[instanceID]; ok {
			return rec
		}
	}
	if scanID != "" {
		if rec, ok := idx.byScanID[scanID]; ok {
			return rec
		}
	}
	return nil
}

// RunMissingStatus marks a mount whose scan.json no longer exists and which
// has no pending dispatch evidence. It is deliberately distinct from
// "pending": a purged historical run must not count as active or block project
// deletion (API_CONTRACT §2).
const RunMissingStatus = "missing"

// projectRun fills the projected fields of a run from its scan.json record.
// A run with no visible snapshot is "pending" only while a durable dispatch
// snapshot still proves it may start; otherwise the history is missing.
func (idx *scanSummaryIndex) projectRun(run *platform.Run, isPending func(instanceID string) bool) {
	rec := idx.lookup(run.InstanceID, run.LegacyScanID)
	if rec == nil {
		if isPending != nil && isPending(run.InstanceID) {
			run.Status = "pending"
		} else {
			run.Status = RunMissingStatus
		}
		return
	}
	run.Status = rec.Status
	run.StartedAt = rec.StartedAt
	run.FinishedAt = rec.FinishedAt
	run.VulnCount = len(rec.Vulns)
	run.TotalTokens = rec.TotalTokens
}

// activeRunCount counts runs whose projected status is non-terminal
// (running/pending/paused) — the delete-guard and list-counter semantics.
func (idx *scanSummaryIndex) activeRunCount(runs []platform.Run, isPending func(instanceID string) bool) int {
	n := 0
	for _, r := range runs {
		switch strings.ToLower(idx.runStatus(&r, isPending)) {
		case "running", "pending", "paused":
			n++
		}
	}
	return n
}

func (idx *scanSummaryIndex) runStatus(run *platform.Run, isPending func(instanceID string) bool) string {
	if rec := idx.lookup(run.InstanceID, run.LegacyScanID); rec != nil {
		return rec.Status
	}
	if run.Status != "" {
		return run.Status
	}
	if isPending != nil && isPending(run.InstanceID) {
		return "pending"
	}
	return RunMissingStatus
}

// projectRuns projects a page of runs in place.
func (idx *scanSummaryIndex) projectRuns(runs []platform.Run, isPending func(instanceID string) bool) {
	for i := range runs {
		idx.projectRun(&runs[i], isPending)
	}
}

// pendingDispatchExists reports whether a durable dispatch snapshot proves the
// run may still be waiting to register its scan.json.
func (s *Server) pendingDispatchExists(instanceID string) bool {
	if strings.TrimSpace(instanceID) == "" {
		return false
	}
	rec, err := s.loadExactDispatchSnapshot(instanceID)
	if err != nil || rec == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(rec.Status)) {
	case "pending", "running", "paused":
		return true
	default:
		return false
	}
}

// attachTaskProjections fills the cold-path statistics of API_CONTRACT §2:
// run_count / last_run_at / last_run_status / finding_count. Runs come from
// the repository; findings come from the scan.json projection (the task-level
// dedup/upgrade projection matures with the M3 cold path — this is the R1
// deterministic view).
func (s *Server) attachTaskProjections(ctx context.Context, store platform.Repository, tasks []platform.Task) {
	if len(tasks) == 0 {
		return
	}
	projectID := tasks[0].ProjectID
	runs, err := store.ListRunsByProject(ctx, projectID)
	if err != nil {
		log.Printf("[platform] run projection for tasks of %s: %v", projectID, err)
		return
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	byTask := make(map[string][]platform.Run)
	findingKeys := make(map[string]map[string]struct{})
	for _, r := range runs {
		byTask[r.TaskID] = append(byTask[r.TaskID], r)
	}
	for _, r := range runs {
		rec := idx.lookup(r.InstanceID, r.LegacyScanID)
		if rec == nil {
			continue
		}
		if findingKeys[r.TaskID] == nil {
			findingKeys[r.TaskID] = make(map[string]struct{})
		}
		for _, v := range rec.Vulns {
			findingKeys[r.TaskID][dedupFindingKey(rec.Target, v)] = struct{}{}
		}
	}
	fs, hasProjection := store.(platform.FindingsStore)
	for i := range tasks {
		taskRuns := byTask[tasks[i].ID]
		tasks[i].RunCount = len(taskRuns)
		for _, r := range taskRuns {
			rec := idx.lookup(r.InstanceID, r.LegacyScanID)
			if rec == nil {
				continue
			}
			if tasks[i].LastRunAt == "" || rec.StartedAt > tasks[i].LastRunAt {
				tasks[i].LastRunAt = rec.StartedAt
				tasks[i].LastRunStatus = rec.Status
			}
		}
		if hasProjection {
			if count, err := fs.CountTaskFindings(ctx, tasks[i].ID); err == nil {
				tasks[i].FindingCount = count
			} else {
				log.Printf("[platform] finding count for task %s: %v", tasks[i].ID, err)
			}
			continue
		}
		tasks[i].FindingCount = len(findingKeys[tasks[i].ID])
	}
}

// ── projects ─────────────────────────────────────────────────────────────────

// handleProjects serves GET/POST /api/projects.
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		projects, err := store.ListProjects(r.Context())
		if err != nil {
			writePlatformError(w, err)
			return
		}
		s.attachProjectProjections(r.Context(), store, projects)
		writeJSONStatus(w, http.StatusOK, map[string]any{"projects": projects})
	case http.MethodPost:
		limitJSONBody(w, r)
		var req struct {
			Name  string         `json:"name"`
			Scope platform.Scope `json:"scope"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		project, err := store.CreateProject(r.Context(), req.Name, req.Scope)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusCreated, project)
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// attachProjectProjections fills task_count / active_runs (contract §1,
// optional fields) using one repository round-trip and one shared walk.
func (s *Server) attachProjectProjections(ctx context.Context, store platform.Repository, projects []platform.Project) {
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	for i := range projects {
		tasks, err := store.ListTasks(ctx, projects[i].ID)
		if err != nil {
			log.Printf("[platform] task projection for %s: %v", projects[i].ID, err)
			continue
		}
		runs, err := store.ListRunsByProject(ctx, projects[i].ID)
		if err != nil {
			log.Printf("[platform] run projection for %s: %v", projects[i].ID, err)
			continue
		}
		projects[i].TaskCount = len(tasks)
		projects[i].ActiveRuns = idx.activeRunCount(runs, s.pendingDispatchExists)
	}
}

// handleProjectDetail serves /api/projects/{id} (GET/PATCH/DELETE) and
// /api/projects/{id}/tasks (GET/POST) — http.ServeMux funnels every
// sub-path through one handler, so the second segment selects the resource.
func (s *Server) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/projects/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	projectID := parts[0]
	if len(parts) > 1 && parts[1] == "tasks" {
		s.handleProjectTasks(w, r, store, projectID)
		return
	}
	if len(parts) != 1 {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		project, err := store.GetProject(r.Context(), projectID)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, project)
	case http.MethodPatch:
		limitJSONBody(w, r)
		var req struct {
			Name     *string         `json:"name"`
			Scope    *platform.Scope `json:"scope"`
			Archived *bool           `json:"archived"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		project, err := store.UpdateProject(r.Context(), projectID, platform.ProjectUpdate{
			Name:     req.Name,
			Scope:    req.Scope,
			Archived: req.Archived,
		})
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, project)
	case http.MethodDelete:
		// Active-run guard: run status lives in scan.json, so this is a
		// web-layer projection ahead of the repository delete.
		if err := s.guardProjectDeletion(r.Context(), store, projectID); err != nil {
			writePlatformError(w, err)
			return
		}
		if err := store.DeleteProject(r.Context(), projectID); err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// guardProjectDeletion refuses to delete a project that still has active
// runs (projected from scan.json). ErrConflict carries the reason.
func (s *Server) guardProjectDeletion(ctx context.Context, store platform.Repository, projectID string) error {
	runs, err := store.ListRunsByProject(ctx, projectID)
	if err != nil {
		return err
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	if n := idx.activeRunCount(runs, s.pendingDispatchExists); n > 0 {
		return fmt.Errorf("%w: project has %d active run(s); stop them before deleting", platform.ErrConflict, n)
	}
	return nil
}

// ── tasks ────────────────────────────────────────────────────────────────────

func (s *Server) handleProjectTasks(w http.ResponseWriter, r *http.Request, store platform.Repository, projectID string) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := store.ListTasks(r.Context(), projectID)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		s.attachTaskProjections(r.Context(), store, tasks)
		writeJSONStatus(w, http.StatusOK, map[string]any{"tasks": tasks})
	case http.MethodPost:
		limitJSONBody(w, r)
		var in struct {
			Name         string                 `json:"name"`
			Goal         string                 `json:"goal"`
			Instruction  string                 `json:"instruction"`
			PhasePolicy  *platform.PhasePolicy  `json:"phase_policy"`
			Schedule     *platform.TaskSchedule `json:"schedule"`
			ContextPaths []string               `json:"context_paths"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(in.Name) == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		task, err := store.CreateTask(r.Context(), projectID, platform.TaskInput{
			Name:         in.Name,
			Goal:         in.Goal,
			Instruction:  in.Instruction,
			PhasePolicy:  in.PhasePolicy,
			Schedule:     in.Schedule,
			ContextPaths: in.ContextPaths,
		})
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusCreated, task)
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// taskRequestBody mirrors API_CONTRACT §2 write fields (partial updates).
type taskRequestBody struct {
	Name         *string                `json:"name"`
	Goal         *string                `json:"goal"`
	Instruction  *string                `json:"instruction"`
	PhasePolicy  *platform.PhasePolicy  `json:"phase_policy"`
	Schedule     *platform.TaskSchedule `json:"schedule"`
	ContextPaths []string               `json:"context_paths"`
	Archived     *bool                  `json:"archived"`
}

// handleTaskDetail serves /api/tasks/{id} (GET/PATCH/DELETE),
// /api/tasks/{id}/runs (POST/GET) and /api/tasks/{id}/findings (GET).
func (s *Server) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	taskID := parts[0]
	if len(parts) > 1 {
		switch parts[1] {
		case "runs":
			if r.Method == http.MethodPost {
				s.handleTaskRunStart(w, r, store, taskID)
				return
			}
			if r.Method == http.MethodGet {
				s.handleTaskRunList(w, r, store, taskID)
				return
			}
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		case "findings":
			if r.Method != http.MethodGet {
				writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			s.handleTaskFindings(w, r, store, taskID)
			return
		case "guidance":
			// R2a HITL-intent (API_CONTRACT §3.1): queued human turns merged
			// into the next session briefing, or injected into a running agent.
			s.handleTaskGuidance(w, r, store, taskID)
			return
		default:
			writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		task, err := store.GetTask(r.Context(), taskID)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		views := []platform.Task{*task}
		s.attachTaskProjections(r.Context(), store, views)
		writeJSONStatus(w, http.StatusOK, views[0])
	case http.MethodPatch:
		limitJSONBody(w, r)
		var req taskRequestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		task, err := store.UpdateTask(r.Context(), taskID, platform.TaskUpdate{
			Name:         req.Name,
			Goal:         req.Goal,
			Instruction:  req.Instruction,
			PhasePolicy:  req.PhasePolicy,
			Schedule:     req.Schedule,
			ContextPaths: req.ContextPaths,
			Archived:     req.Archived,
		})
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, task)
	case http.MethodDelete:
		if err := store.DeleteTask(r.Context(), taskID); err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// ── task runs ────────────────────────────────────────────────────────────────

// handleTaskRunList serves GET /api/tasks/{id}/runs?page&size with the
// scan.json projection applied to every mount record.
func (s *Server) handleTaskRunList(w http.ResponseWriter, r *http.Request, store platform.Repository, taskID string) {
	page, size := parsePageParams(r.URL.Query().Get("page"), r.URL.Query().Get("size"))
	runs, total, err := store.ListRuns(r.Context(), taskID, page, size)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	idx.projectRuns(runs, s.pendingDispatchExists)
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"items": runs,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

// handleTaskRunStart serves POST /api/tasks/{id}/runs. The request is a
// partial ScanRequest override (API_CONTRACT §2): targets default to the
// project scope and must fall inside it (D7 range isolation); the dispatch
// itself reuses exactly the durable steps of POST /api/scan —
// reserveDispatchID → acquireQueueOwnership → persistPendingDispatchSnapshot
// → saveQueueStateDurable → go runMultiScan — without restructuring
// handleScan. The response "status" carries the real dispatch state
// (queued as "pending" until admission), same semantics as /api/scan.
func (s *Server) handleTaskRunStart(w http.ResponseWriter, r *http.Request, store platform.Repository, taskID string) {
	limitJSONBody(w, r)
	var override ScanRequest
	if err := json.NewDecoder(r.Body).Decode(&override); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	task, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	if task.ArchivedAt != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "task is archived"})
		return
	}
	project, err := store.GetProject(r.Context(), task.ProjectID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	if project.ArchivedAt != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "project is archived"})
		return
	}

	// Targets: request override, else the project scope defaults.
	targets := flattenPlatformTargets(override.Targets)
	if len(targets) == 0 {
		targets = flattenPlatformTargets(project.Scope.Targets)
	}
	if len(targets) == 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "targets required (project scope has no targets and none supplied)"})
		return
	}
	// D7 range isolation: every target must fall inside the project scope.
	if err := validateTargetsInScope(targets, project.Scope); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Same self-scan protection as POST /api/scan: refuse requests whose
	// every target is a local/internal address (the scope guard filters them
	// per-target in runMultiScan, but a fully-blocked request would produce
	// an empty instance the operator must clean up). The message matches
	// handleScan's wording, including the AllowLocalTargets remediation.
	if allTargetsLocallyBlocked(s, targets) {
		msg := "All targets are local/internal addresses or the dashboard's own listener, so the scan was refused (self-scan protection)."
		if cfg := s.configSnapshot(); cfg != nil && cfg.AllowLocalTargets {
			msg += " The dashboard's own listener and the unspecified address (0.0.0.0 / ::) are never scannable, even with local targets enabled — point the scan at the app's actual host:port instead."
		} else {
			msg += " To scan a locally-hosted app on a self-hosted install, enable local targets: Settings → Security → \"Allow local targets\", or set XALGORIX_ALLOW_LOCAL_TARGETS=true in ~/.xalgorix.env and restart. (The dashboard's own listener stays protected either way. Leave this OFF on shared/hosted deployments.)"
		}
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	// Merge task policy under the request overrides (non-empty request
	// fields win; task policy fills the gaps; "single" is the fallback mode).
	policy := task.PhasePolicy
	req := override // webhook/model/api_base/provider_profile/… pass through like /api/scan
	req.Targets = targets
	req.Instruction = firstNonEmptyString(override.Instruction, task.Instruction, task.Goal)
	req.ScanMode = firstNonEmptyString(override.ScanMode, policyScanMode(policy), "single")
	req.SeverityFilter = override.SeverityFilter
	if len(req.SeverityFilter) == 0 && policy != nil {
		req.SeverityFilter = policy.SeverityFilter
	}
	req.Phases = override.Phases
	if len(req.Phases) == 0 && policy != nil {
		req.Phases = policy.Phases
	}
	req.ScanIntensity = firstNonEmptyString(override.ScanIntensity, policyIntensity(policy))
	req.Name = firstNonEmptyString(override.Name, task.Name)
	req.SaveOnly = false // a run always dispatches; save-only stays a /api/scan concept
	// Task context_paths are the Context half of the Task model. They are
	// additive with an explicit request scan_context and all enter the seeded
	// attack surface (never silently reduced to the first path).
	req.ScanContextPaths = taskRunContextPaths(task, override)
	req.ScanContext = ""
	if len(req.ScanContextPaths) > 0 {
		req.ScanContext = req.ScanContextPaths[0]
	}

	// R11.6 fail-fast (same semantics as handleScan): a request naming a
	// provider_profile that cannot resolve fails here with 400 instead of
	// silently falling back to the default client. Non-sentinel resolver
	// errors stay non-fatal (transient flock/profile races surface in the
	// LLM client's own resolver), exactly like /api/scan.
	if _, err := s.resolveScanCredentials(r.Context(), req, s.cfg); err != nil {
		if errors.Is(err, errUnknownProviderProfile) || errors.Is(err, errAPIBaseNeedsKey) {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("scan: precondition resolveScanCredentials returned non-sentinel error: %v", err)
	}

	// The webhook and api_base are outbound destinations chosen by the
	// caller: refuse this machine/link-local webhooks (same self-scan
	// protection targets get) and api_base overrides that would carry the
	// stored provider credentials somewhere else. The platform entry is a
	// wire entry point that starts scans, so it must apply the same guard
	// as POST /api/scan.
	if err := s.checkScanRequestDestinations(r.Context(), req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	scanCfg := *s.cfg
	applyScanLLMOverrides(&scanCfg, req)

	// ── dispatch sequence copied from handleScan (server.go) ──
	instanceID := strings.TrimSpace(req.DispatchID)
	callerDispatchID := instanceID != ""
	if callerDispatchID {
		if !validDispatchID(instanceID) {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "dispatch_id must be 16-128 URL-safe characters"})
			return
		}
	} else {
		for {
			instanceID = randomSlug()
			if _, reserved := s.reserveDispatchID(instanceID); reserved {
				break
			}
		}
	}
	existingRunID := s.existingRunIDForInstance(r.Context(), store, instanceID)
	if callerDispatchID {
		if status, reserved := s.reserveDispatchID(instanceID); !reserved {
			writeJSONStatus(w, http.StatusOK, taskRunAck{Status: status, RunID: existingRunID, InstanceID: instanceID})
			return
		}
	}
	ownership, owned, err := s.acquireQueueOwnership(instanceID)
	if err != nil {
		s.releaseDispatchID(instanceID)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not acquire dispatch queue ownership"})
		return
	}
	if !owned {
		s.releaseDispatchID(instanceID)
		writeJSONStatus(w, http.StatusOK, taskRunAck{Status: "pending", RunID: existingRunID, InstanceID: instanceID})
		return
	}
	req.queueOwnership = ownership
	status, accepted, err := s.persistPendingDispatchSnapshot(req, instanceID)
	if err != nil {
		ownership.release()
		s.releaseDispatchID(instanceID)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not durably reserve dispatch identity"})
		return
	}
	if !accepted {
		ownership.release()
		s.releaseDispatchID(instanceID)
		writeJSONStatus(w, http.StatusOK, taskRunAck{Status: status, RunID: existingRunID, InstanceID: instanceID})
		return
	}
	req.Name = strings.TrimSpace(req.Name) // propagate name to running scans too
	req.InstanceID = instanceID
	if err := s.saveQueueStateDurable(0, req); err != nil {
		if abortErr := s.abortPendingDispatchSnapshot(instanceID, "queue_state_persist_failed"); abortErr != nil {
			log.Printf("[scan] failed to terminalize dispatch %s after queue-state error: %v", instanceID, abortErr)
		}
		ownership.release()
		s.releaseDispatchID(instanceID)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not durably persist resumable dispatch"})
		return
	}
	go func() {
		defer s.releaseDispatchID(instanceID)
		s.runMultiScan(req, &scanCfg, instanceID)
	}()

	// Dispatch reserved durably — mount the run record. The mount uses a
	// detached context, NOT the request context: the scan is already
	// accepted at this point, so a client that disconnects mid-request must
	// not cancel the INSERT and leave the dispatched scan without its run
	// mount. A short timeout bounds a wedged store; a mount failure must not
	// undo the scan either way — it is logged and the response keeps the
	// dispatch identity so the operator can re-attach.
	runID := ""
	mountCtx, mountCancel := context.WithTimeout(context.Background(), 5*time.Second)
	run, mountErr := store.CreateRun(mountCtx, task.ID, platform.RunMount{
		InstanceID: instanceID,
		Mode:       req.ScanMode,
		Trigger:    "manual",
	})
	mountCancel()
	if mountErr != nil {
		log.Printf("[platform] dispatch %s accepted but run mount on task %s failed: %v", instanceID, task.ID, mountErr)
	} else {
		runID = run.ID
	}

	// Same ack semantics as /api/scan: QUEUED as "pending" until the
	// admission gate grants a slot (see handleScan for the rationale).
	writeJSONStatus(w, http.StatusOK, taskRunAck{Status: "pending", RunID: runID, InstanceID: instanceID})
}

// taskRunAck is the POST /api/tasks/{id}/runs acknowledgement shape
// (API_CONTRACT §2). RunID is omitempty: an ack without a mounted run (mount
// failure, duplicate dispatch of an /api/scan-started instance) omits the
// field instead of shipping an empty string the frontend would render.
type taskRunAck struct {
	Status     string `json:"status"`
	RunID      string `json:"run_id,omitempty"`
	InstanceID string `json:"instance_id"`
}

// existingRunIDForInstance returns the run id already mounted for an
// instance, so idempotent re-dispatches can reference their run.
func (s *Server) existingRunIDForInstance(ctx context.Context, store platform.Repository, instanceID string) string {
	if instanceID == "" {
		return ""
	}
	runs, err := store.RunsByInstanceID(ctx, instanceID)
	if err != nil || len(runs) == 0 {
		return ""
	}
	return runs[0].ID
}

// ── task findings (R1 projection) ────────────────────────────────────────────

// handleTaskFindings serves GET /api/tasks/{id}/findings. With a DSN it reads
// the PG cold-path replica (one newest row per dedup key); without one it
// pages through every run and applies the same deterministic projection. It
// never changes finding acceptance, labels or the report gate.
func (s *Server) handleTaskFindings(w http.ResponseWriter, r *http.Request, store platform.Repository, taskID string) {
	if _, err := store.GetTask(r.Context(), taskID); err != nil {
		writePlatformError(w, err)
		return
	}
	var (
		findings []flatFinding
		err      error
	)
	if fs, ok := store.(platform.FindingsStore); ok {
		findings, err = s.taskFindingsFromProjection(r.Context(), fs, taskID)
	} else {
		findings, err = s.taskFindingsFromScans(r.Context(), store, taskID)
	}
	if err != nil {
		writePlatformError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(findings)
}

// ── scope validation (D7 range isolation) ────────────────────────────────────

// flattenPlatformTargets splits comma/space separated mega-targets the same
// way registerMultiScan flattens them, so validation sees what the scan will
// actually run against.
// taskRunContextPaths combines the task's persistent context attachments with
// an explicit request override. All entries are preserved and de-duplicated.
func taskRunContextPaths(task *platform.Task, override ScanRequest) []string {
	var taskPaths []string
	if task != nil {
		taskPaths = task.ContextPaths
	}
	return mergeContextPaths(taskPaths, []string{override.ScanContext}, override.ScanContextPaths)
}

// mergeContextPaths flattens and de-duplicates context artifact paths while
// preserving caller order.
func mergeContextPaths(groups ...[]string) []string {
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, raw := range group {
			path := strings.TrimSpace(raw)
			if path == "" {
				continue
			}
			if _, dup := seen[path]; dup {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	return out
}

func flattenPlatformTargets(raw []string) []string {
	var out []string
	for _, t := range raw {
		fields := strings.FieldsFunc(t, func(r rune) bool {
			return r == ',' || r == ' ' || r == ';' || r == '\n' || r == '\r' || r == '\t'
		})
		for _, f := range fields {
			if f != "" {
				out = append(out, f)
			}
		}
	}
	return out
}

// targetHostPort extracts the hostname (lowercased, path stripped) and an
// explicit port (empty when none was written into the target string).
func targetHostPort(target string) (host, port string) {
	t := strings.TrimSpace(target)
	if t == "" {
		return "", ""
	}
	if strings.Contains(t, "://") {
		if u, err := url.Parse(t); err == nil && u.Host != "" {
			t = u.Host
		}
	} else if idx := strings.IndexAny(t, "/?#"); idx >= 0 {
		t = t[:idx]
	}
	if h, p, err := net.SplitHostPort(t); err == nil {
		return strings.ToLower(strings.Trim(h, "[]")), p
	}
	return strings.ToLower(strings.Trim(t, "[]")), ""
}

// validateTargetsInScope enforces the project scope boundary: each target
// must exactly match a scope entry, a subdomain of one, or a "*." wildcard
// entry. When allow_ports is non-empty, every possible port the target can
// use must be listed — including the HTTP/HTTPS default ports. A scheme-less
// host can be probed over both defaults, so it requires both to be allowed.
func validateTargetsInScope(targets []string, scope platform.Scope) error {
	scopeHosts := make([]string, 0, len(scope.Targets))
	for _, e := range scope.Targets {
		host, _ := targetHostPort(e)
		if host != "" {
			scopeHosts = append(scopeHosts, host)
		}
	}
	for _, t := range targets {
		host, _ := targetHostPort(t)
		if host == "" {
			return fmt.Errorf("target %q is not a valid host", t)
		}
		inScope := false
		for _, entry := range scopeHosts {
			if hostInScope(host, entry) {
				inScope = true
				break
			}
		}
		if !inScope {
			return fmt.Errorf("target %q is outside the project scope", t)
		}
		if len(scope.AllowPorts) == 0 {
			continue
		}
		ports, err := targetPorts(t)
		if err != nil {
			return err
		}
		for _, p := range ports {
			if !portAllowed(scope.AllowPorts, p) {
				return fmt.Errorf("target %q can use port %d which is not in the project allow_ports", t, p)
			}
		}
	}
	return nil
}

// targetPorts returns every HTTP(S) port a target may use. Explicit ports
// win; an http/https scheme without a port contributes its default; a bare
// host is treated as potentially using both defaults (fail-closed).
func targetPorts(target string) ([]int, error) {
	raw := strings.TrimSpace(target)
	if raw == "" {
		return nil, fmt.Errorf("target is empty")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("target %q is not a valid URL", target)
		}
		if p := u.Port(); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > 65535 {
				return nil, fmt.Errorf("target %q has an invalid port", target)
			}
			return []int{n}, nil
		}
		switch strings.ToLower(u.Scheme) {
		case "http":
			return []int{80}, nil
		case "https":
			return []int{443}, nil
		default:
			return nil, fmt.Errorf("target %q has an unsupported scheme without an explicit port", target)
		}
	}
	_, port := targetHostPort(raw)
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("target %q has an invalid port", target)
		}
		return []int{n}, nil
	}
	return []int{80, 443}, nil
}

// hostInScope matches exact hosts, subdomains of a bare scope entry, and the
// explicit "*.domain" wildcard form. The wildcard deliberately does not match
// the apex (the bare domain must be listed separately).
func hostInScope(host, entry string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	entry = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(entry)), ".")
	if host == entry {
		return true
	}
	if suffix, ok := strings.CutPrefix(entry, "*."); ok {
		return suffix != "" && strings.HasSuffix(host, "."+suffix)
	}
	return strings.HasSuffix(host, "."+entry)
}

func portAllowed(allow []int, port int) bool {
	for _, p := range allow {
		if p == port {
			return true
		}
	}
	return false
}

// allTargetsLocallyBlocked mirrors handleScan's fully-blocked refusal.
func allTargetsLocallyBlocked(s *Server, targets []string) bool {
	for _, t := range targets {
		if strings.TrimSpace(t) == "" {
			continue
		}
		if !s.isBlockedTarget(t) {
			return false
		}
	}
	return len(targets) > 0
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func policyScanMode(p *platform.PhasePolicy) string {
	if p == nil {
		return ""
	}
	return p.ScanMode
}

func policyIntensity(p *platform.PhasePolicy) string {
	if p == nil {
		return ""
	}
	return p.Intensity
}

// ── §3.2 project filtering for existing endpoints ────────────────────────────

// errProjectStoreUnavailable means the caller explicitly requested a project
// filter but the platform repository is not wired. Returning an unfiltered
// view here would leak every project's data (D7), so callers map this to 503.
var errProjectStoreUnavailable = errors.New("platform store not initialized")

// writeProjectFilterError maps a matcher failure to the wire contract.
func writeProjectFilterError(w http.ResponseWriter, err error) {
	if errors.Is(err, errProjectStoreUnavailable) {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{
			"error": "project filter unavailable: platform store not initialized",
		})
		return
	}
	writeJSONStatus(w, http.StatusInternalServerError, map[string]string{
		"error": "project lookup failed",
	})
}

// projectScanMatcher resolves the optional project_id query parameter into a
// matcher over scan records (API_CONTRACT §3.2). Rules:
//   - absent param → no filter (byte-for-byte legacy behavior);
//   - explicit param + nil platform store → errProjectStoreUnavailable
//     (fail-closed; never return every project's rows);
//   - unknown project → a matcher that matches nothing (its views are empty,
//     not "everything");
//   - otherwise a record belongs to the project when a mount record links it:
//     runs.instance_id == record.instance_id, or runs.legacy_scan_id ==
//     record.ID (decision: instance_id first, legacy ID fallback).
func (s *Server) projectScanMatcher(ctx context.Context, r *http.Request) (match func(instanceID, scanID string) bool, hasFilter bool, err error) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		return nil, false, nil
	}
	store := s.currentPlatformStore()
	if store == nil {
		return nil, false, errProjectStoreUnavailable
	}
	runs, listErr := store.ListRunsByProject(ctx, projectID)
	if listErr != nil {
		if errors.Is(listErr, platform.ErrNotFound) {
			return func(string, string) bool { return false }, true, nil
		}
		return nil, false, listErr
	}
	instanceSet := make(map[string]struct{}, len(runs))
	legacySet := make(map[string]struct{}, len(runs))
	for _, run := range runs {
		if run.InstanceID != "" {
			instanceSet[run.InstanceID] = struct{}{}
		}
		if run.LegacyScanID != "" {
			legacySet[run.LegacyScanID] = struct{}{}
		}
	}
	return func(instanceID, scanID string) bool {
		if instanceID != "" {
			if _, ok := instanceSet[instanceID]; ok {
				return true
			}
		}
		if scanID != "" {
			if _, ok := instanceSet[scanID]; ok {
				return true
			}
			if _, ok := legacySet[scanID]; ok {
				return true
			}
		}
		return false
	}, true, nil
}
