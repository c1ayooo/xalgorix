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
// findAllScanSummaries at read time, which since R6.1 sweeps the durable
// PG runs scan set first and walks legacy files as the residue fallback
// (charter §5.1 v3.0, REDESIGN D12).
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
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
		// No report job survives a process restart: queued/generating rows
		// found at startup are ghosts. Mark them failed so the operator can
		// regenerate instead of waiting on a job that no longer exists.
		if reconciler, ok := store.(platform.ReportReconciler); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if n, err := reconciler.ReconcileActiveReports(ctx, "interrupted by process restart"); err != nil {
				log.Printf("[report] reconcile interrupted reports: %v", err)
			} else if n > 0 {
				log.Printf("[report] marked %d interrupted report(s) failed", n)
			}
			cancel()
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
	// byInstance keeps EVERY record claiming the instance, in walk order.
	// Fresh-state re-dispatches (REDESIGN D11) intentionally create several
	// records per instance; picking one arbitrarily (first-wins over an
	// alphabetical walk) silently dropped every sibling's findings from the
	// projections and task views.
	byInstance map[string][]*ScanRecord
	byScanID   map[string]*ScanRecord
	// dirByScanID maps each record id to its on-disk directory so the merge
	// primitives below can hand journal readers real paths.
	dirByScanID map[string]string
}

// scanSummaryIndexFor builds the index from one shared findAllScanSummaries
// walk. Records are shallow cached copies — read-only use only.
func (s *Server) scanSummaryIndexFor(entries []scanEntry) *scanSummaryIndex {
	idx := &scanSummaryIndex{
		byInstance:  make(map[string][]*ScanRecord, len(entries)),
		byScanID:    make(map[string]*ScanRecord, len(entries)),
		dirByScanID: make(map[string]string, len(entries)),
	}
	for i := range entries {
		rec := &entries[i].rec
		key := strings.TrimSpace(rec.InstanceID)
		if key == "" {
			key = rec.ID
		}
		if key != "" {
			idx.byInstance[key] = append(idx.byInstance[key], rec)
		}
		if rec.ID != "" {
			if _, ok := idx.byScanID[rec.ID]; !ok {
				idx.byScanID[rec.ID] = rec
			}
			if _, ok := idx.dirByScanID[rec.ID]; !ok {
				idx.dirByScanID[rec.ID] = entries[i].dir
			}
		}
	}
	return idx
}

// newestSibling applies the scanDirRecordForRun tie-break (REDESIGN D11):
// among records sharing an instance id, the newest session wins.
func newestSibling(recs []*ScanRecord) *ScanRecord {
	var best *ScanRecord
	for _, rec := range recs {
		if best == nil || rec.StartedAt > best.StartedAt {
			best = rec
		}
	}
	return best
}

// lookup resolves the single record that represents a run: the newest sibling
// when several share the instance id, the legacy scan id record otherwise.
func (idx *scanSummaryIndex) lookup(instanceID, scanID string) *ScanRecord {
	if instanceID != "" {
		if rec := newestSibling(idx.byInstance[instanceID]); rec != nil {
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

// lookupAll returns every record behind a run: all siblings claiming the
// instance id, plus the legacy scan id record when it claims a different
// instance. Findings corpora must merge across siblings — one record per
// instance is a projection blind spot, not the truth.
func (idx *scanSummaryIndex) lookupAll(instanceID, scanID string) []*ScanRecord {
	var out []*ScanRecord
	seen := make(map[string]struct{})
	add := func(rec *ScanRecord) {
		if rec == nil {
			return
		}
		if _, dup := seen[rec.ID]; dup {
			return
		}
		seen[rec.ID] = struct{}{}
		out = append(out, rec)
	}
	for _, rec := range idx.byInstance[instanceID] {
		add(rec)
	}
	if scanID != "" {
		add(idx.byScanID[scanID])
	}
	return out
}

// siblingDirs returns the on-disk directories behind a run — every sibling
// session claiming the instance plus the legacy scan id record — newest
// session first. Journal projections merge across them: a re-dispatched run's
// history lives in several session directories, and reading any single one is
// the blind spot, not the truth (D11, same discipline as lookupAll).
func (idx *scanSummaryIndex) siblingDirs(instanceID, scanID string) []string {
	recs := idx.lookupAll(instanceID, scanID)
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].StartedAt > recs[j].StartedAt })
	dirs := make([]string, 0, len(recs))
	for _, rec := range recs {
		if d := idx.dirByScanID[rec.ID]; d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
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

// taskActiveRun returns the task's first active run (running/pending/paused),
// projected exactly like handleTaskRunList projects them: scan.json status via
// the summary index, with the durable dispatch snapshot covering the window
// before the scan.json registers. This is the one-shot guard for
// POST /api/tasks/{id}/runs and the scheduler tick: a task with an active run
// must not stack a second one.
func (s *Server) taskActiveRun(ctx context.Context, store platform.Repository, taskID string) (platform.Run, bool) {
	runs, _, err := store.ListRuns(ctx, taskID, 1, runProjectionPageSize)
	if err != nil || len(runs) == 0 {
		return platform.Run{}, false
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	idx.projectRuns(runs, s.pendingDispatchExists)
	for i := range runs {
		switch strings.ToLower(runs[i].Status) {
		case "running", "pending", "paused":
			return runs[i], true
		}
	}
	return platform.Run{}, false
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

// taskTerminalRun returns the task's newest run once it is no longer active
// (projected exactly like taskActiveRun). A "stopping" run is returned too —
// it is not resumable yet, and the caller maps it to 409.
func (s *Server) taskTerminalRun(ctx context.Context, store platform.Repository, taskID string) (platform.Run, bool) {
	runs, _, err := store.ListRuns(ctx, taskID, 1, runProjectionPageSize)
	if err != nil || len(runs) == 0 {
		return platform.Run{}, false
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	idx.projectRuns(runs, s.pendingDispatchExists)
	if len(runs) == 0 {
		return platform.Run{}, false
	}
	// Only the newest run is inspected: an active newest run means the caller
	// already 409'd via taskActiveRun; anything else re-dispatches it.
	switch strings.ToLower(runs[0].Status) {
	case "running", "pending", "paused":
		return platform.Run{}, false
	}
	return runs[0], true
}

// recordTaskRunRevision stamps the runnable-content revision an accepted
// dispatch consumed. It is best-effort: a failed marker write only means the
// task keeps reading as "content changed" (the run button stays available),
// never that a run is blocked or lost. The task object must be the one the
// dispatch was built from — that is the content actually consumed.
func (s *Server) recordTaskRunRevision(store platform.Repository, task *platform.Task) {
	if store == nil || task == nil || strings.TrimSpace(task.ID) == "" {
		return
	}
	revision := platform.TaskContentRevision(*task)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.SetTaskRunRevision(ctx, task.ID, revision); err != nil {
		log.Printf("[platform] task %s dispatched but run revision not recorded: %v", task.ID, err)
	}
}

// scanDirForRun resolves the on-disk scan dir for a run's instance from the
// shared events-free summary walk (instance_id first, legacy scan id as the
// fallback key), so a 1:1 re-dispatch can bind to the same scan dir and keep
// notes / ledger / execution checkpoint / engine checkpoint ID continuous.
func (s *Server) scanDirRecordForRun(instanceID, legacyScanID string) (string, *ScanRecord) {
	entries := s.findAllScanSummaries()
	// Multiple records can share an instance id after fresh-state re-dispatches
	// (REDESIGN D11): prefer the NEWEST so continuity (notes/ledger) and the
	// replay come from the most recent session, not an ancestor.
	var best string
	var bestRec *ScanRecord
	pick := func(dir string, rec *ScanRecord) {
		if bestRec == nil || rec.StartedAt > bestRec.StartedAt {
			best, bestRec = dir, rec
		}
	}
	if instanceID != "" {
		for i := range entries {
			if strings.TrimSpace(entries[i].rec.InstanceID) == instanceID {
				pick(entries[i].dir, &entries[i].rec)
			}
		}
	}
	if best != "" {
		return best, bestRec
	}
	if legacyScanID != "" {
		for i := range entries {
			if entries[i].rec.ID == legacyScanID {
				return entries[i].dir, &entries[i].rec
			}
		}
	}
	return "", nil
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
		recs := idx.lookupAll(r.InstanceID, r.LegacyScanID)
		if len(recs) == 0 {
			continue
		}
		if findingKeys[r.TaskID] == nil {
			findingKeys[r.TaskID] = make(map[string]struct{})
		}
		for _, rec := range recs {
			for _, v := range rec.Vulns {
				findingKeys[r.TaskID][dedupFindingKey(rec.Target, v)] = struct{}{}
			}
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
				tasks[i].LastRunCompletion = rec.Completion
			}
		}
		// Run gate (用户裁决): a task owns one run, so once a dispatch has
		// consumed this exact content a plain re-run would replay it. The
		// revision comparison is content-based (not a timestamp), so a no-op
		// edit cannot unlock a replay and an edit made during a run still
		// counts. Never-run tasks and unrecorded markers stay runnable.
		revision := platform.TaskContentRevision(tasks[i])
		changed := tasks[i].RunCount == 0 || revision == "" ||
			platform.DispatchedRunRevision(tasks[i]) != revision
		tasks[i].ContentChangedSinceRun = &changed
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
		// Seed the scope observer baseline (v4.6) so a fresh project is not
		// born with an empty rules table: bulk-destruction / upload / web-OSS
		// writes start out visible and tunable in the rules dialog.
		if n := s.seedScopeApprovalRules(r.Context(), store, project.ID); n > 0 {
			log.Printf("[scope-observer] seeded %d baseline rule(s) for project %s", n, project.ID)
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
	if len(parts) > 1 && parts[1] == "assets" {
		s.handleProjectAssets(w, r, store, projectID, parts[2:])
		return
	}
	if len(parts) > 1 && parts[1] == "conversations" {
		s.handleProjectConversations(w, r, store, projectID)
		return
	}
	if len(parts) > 1 && parts[1] == "analyze-goal" {
		s.handleAnalyzeGoal(w, r, store, projectID)
		return
	}
	if len(parts) > 1 && parts[1] == "scope" {
		// Scope/fence configuration module (project_scope.go): read, save and
		// dry-run the same authorization boundary Quick-create dispatches
		// against.
		s.handleProjectScope(w, r, store, projectID, parts[2:])
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
			AuditMode    string                 `json:"audit_mode"`
			Objective    *platform.Objective    `json:"objective"`
			// Model chain ("<provider>:<profileId>", order = priority).
			LLMChain  []string `json:"llm_chain"`
			ActiveLLM string   `json:"active_llm"`
			// Outbound proxy pin: "" inherit, "direct", or a pool index.
			Proxy string `json:"proxy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(in.Name) == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		// Normalize + verify the model chain before persisting: a chain entry
		// that cannot resolve to a live profile would otherwise fail the first
		// run instead of the save.
		if chain, active := platform.NormalizeTaskLLMChain(in.LLMChain, in.ActiveLLM); len(chain) > 0 {
			if err := s.validateTaskLLMChain(r.Context(), chain); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			in.LLMChain, in.ActiveLLM = chain, active
		}
		if pin := platform.NormalizeTaskProxy(in.Proxy); pin != "" {
			if err := s.validateTaskProxyPin(pin); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			in.Proxy = pin
		} else {
			in.Proxy = ""
		}
		task, err := store.CreateTask(r.Context(), projectID, platform.TaskInput{
			Name:         in.Name,
			Goal:         in.Goal,
			Instruction:  in.Instruction,
			PhasePolicy:  in.PhasePolicy,
			Schedule:     in.Schedule,
			ContextPaths: in.ContextPaths,
			AuditMode:    in.AuditMode,
			Objective:    in.Objective,
			LLMChain:     in.LLMChain,
			ActiveLLM:    in.ActiveLLM,
			Proxy:        in.Proxy,
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
	// AuditMode is the task-level HITL switch (inherit/off/enforce).
	AuditMode *string `json:"audit_mode"`
	// Objective is raw so the three states stay distinguishable: absent =
	// leave as is, explicit null = clear, object = replace.
	Objective json.RawMessage `json:"objective"`
	Archived  *bool           `json:"archived"`
	// Model chain: llm_chain replaces the ordered chain (empty clears it);
	// active_llm replaces the chain head. Absent leaves them unchanged.
	LLMChain  *[]string `json:"llm_chain"`
	ActiveLLM *string   `json:"active_llm"`
	// Proxy non-nil replaces the outbound proxy pin ("" = inherit).
	Proxy *string `json:"proxy"`
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
		case "reports":
			// Manual report delivery (REPORT_DELIVERY_SPEC): GET list / POST
			// generate a task-level report revision. No scan lifecycle hook
			// creates reports.
			s.handleTaskReports(w, r, store, taskID)
			return
		case "findings":
			if r.Method != http.MethodGet {
				writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			s.handleTaskFindings(w, r, store, taskID)
			return
		case "workers":
			// Task-scoped worker history for the session rail: every run's
			// delegated workers, including completed/failed ones.
			s.handleTaskWorkers(w, r, store, taskID)
			return
		case "guidance":
			// R2a HITL-intent (API_CONTRACT §3.1): queued human turns merged
			// into the next session briefing, or injected into a running agent.
			s.handleTaskGuidance(w, r, store, taskID)
			return
		case "questions":
			// Side-question responder (chat_questions.go): tool-less LLM
			// answer over a scan snapshot; never enters the scan loop.
			s.handleTaskQuestions(w, r, store, taskID)
			return
		case "objective":
			// Structured task goal + its honest evaluation (REDESIGN §3.1).
			s.handleTaskObjective(w, r, store, taskID)
			return
		case "intelligence":
			// 情报 tab: task-level intel aggregation + operator triage
			// (handlers_task_intelligence.go).
			if len(parts) > 2 {
				s.handleIntelTriage(w, r, store, taskID, parts[2])
				return
			}
			if r.Method != http.MethodGet {
				writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			s.handleTaskIntelligence(w, r, store, taskID)
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
		upd := platform.TaskUpdate{
			Name:         req.Name,
			Goal:         req.Goal,
			Instruction:  req.Instruction,
			PhasePolicy:  req.PhasePolicy,
			Schedule:     req.Schedule,
			ContextPaths: req.ContextPaths,
			AuditMode:    req.AuditMode,
			Archived:     req.Archived,
			LLMChain:     req.LLMChain,
			ActiveLLM:    req.ActiveLLM,
			Proxy:        req.Proxy,
		}
		// PATCH names the replacement chain wholesale; validate what will be
		// stored (empty means "clear", which is always valid).
		if req.Proxy != nil {
			if pin := platform.NormalizeTaskProxy(*req.Proxy); pin != "" {
				if err := s.validateTaskProxyPin(pin); err != nil {
					writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
			}
		}
		if req.LLMChain != nil && len(*req.LLMChain) > 0 {
			chain, _ := platform.NormalizeTaskLLMChain(*req.LLMChain, "")
			if err := s.validateTaskLLMChain(r.Context(), chain); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}
		if len(req.Objective) > 0 {
			if strings.TrimSpace(string(req.Objective)) == "null" {
				upd.ClearObjective = true
			} else {
				var obj platform.Objective
				if err := json.Unmarshal(req.Objective, &obj); err != nil {
					writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "objective must be an object or null"})
					return
				}
				upd.Objective = &obj
			}
		}
		// Snapshot the pre-edit task so the live run (if any) can be told
		// exactly what changed; the PATCH itself is still wholesale.
		existing, gerr := store.GetTask(r.Context(), taskID)
		if gerr != nil {
			writePlatformError(w, gerr)
			return
		}
		task, err := store.UpdateTask(r.Context(), taskID, upd)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		// Hot-apply to an active run (planner brief + main agent + model +
		// egress + audit). Persisted first; the run is never restarted.
		s.applyTaskConfigToLiveRuns(r.Context(), existing, task)
		writeJSONStatus(w, http.StatusOK, task)
	case http.MethodDelete:
		if s.taskHasActiveReport(r.Context(), taskID) {
			writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "该任务有报告正在生成，请先取消或等待完成"})
			return
		}
		// Report metadata rows cascade with the task in the repository; the
		// rendered artifacts live on disk and must be removed here.
		s.deleteTaskReportDirs(r.Context(), taskID)
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
	// Backfill titles for runs mounted before conversation metadata existed
	// (migration 0011 leaves them empty). One write per run, then the sidebar
	// has something readable; a rename or the first human message wins later.
	s.backfillRunTitles(r.Context(), store, taskID, runs)
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
// orderedTaskLLMChain returns the task chain with the active entry first
// (deduped), so the run starts on the operator's current choice and advances
// through the remaining priority order.
func orderedTaskLLMChain(chain []string, active string) []string {
	active = strings.TrimSpace(active)
	out := make([]string, 0, len(chain)+1)
	seen := map[string]bool{}
	if active != "" {
		out = append(out, active)
		seen[active] = true
	}
	for _, key := range chain {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// validateTaskProxyPin checks that a task's outbound proxy pin is usable in the
// CURRENT process configuration: "direct" conflicts with proxy-required mode,
// and a pool index must still exist after a settings change.
func (s *Server) validateTaskProxyPin(pin string) error {
	switch pin {
	case "":
		return nil
	case "direct":
		if proxy.Required() {
			return fmt.Errorf("the process requires a proxy (XALGORIX_PROXY_REQUIRED); choose a proxy entry instead of direct")
		}
		return nil
	default:
		idx, err := strconv.Atoi(pin)
		if err != nil || idx <= 0 {
			return fmt.Errorf("proxy pin must be \"direct\", \"\" (inherit) or a positive pool index")
		}
		if proxy.EntryAt(idx) == nil {
			return fmt.Errorf("proxy entry %d does not exist in the current pool", idx)
		}
		return nil
	}
}

// validateTaskLLMChain rejects a chain entry that does not resolve to a live
// auth profile and catalog entry. Saving is the right failure point: a bad
// downstream entry would otherwise only surface when the run tries to fail over
// mid-scan.
func (s *Server) validateTaskLLMChain(ctx context.Context, chain []string) error {
	if len(chain) == 0 {
		return nil
	}
	if s.profiles == nil || s.catalog == nil {
		return fmt.Errorf("%w: provider stores are not configured", errUnknownProviderProfile)
	}
	for _, key := range chain {
		prof, ok, err := s.profiles.Get(ctx, key)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: %q", errUnknownProviderProfile, key)
		}
		if _, ok, err := s.catalog.Get(ctx, prof.Provider); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w: %q references unknown provider %q", errUnknownProviderProfile, key, prof.Provider)
		}
	}
	return nil
}

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

	// One-shot guard: a task runs one execution at a time. While a run is
	// active (pending/running/paused), refuse instead of stacking a second
	// concurrent run on the same goal — the operator drives continuation by
	// finishing/pausing the current run (or adding a new goal afterwards).
	// The 409 carries the active run identity so callers can re-attach to it.
	if active, hasActive := s.taskActiveRun(r.Context(), store, task.ID); hasActive {
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error":       "task already has an active run (pending/running/paused); stop or resume it before starting another",
			"run_id":      active.ID,
			"instance_id": active.InstanceID,
		})
		return
	}

	// 1:1 task:run (REDESIGN 2026-11-22): a task owns exactly ONE run. When
	// that run is terminal, POST runs re-dispatches the SAME instance under
	// the SAME mount — it never stacks a second run record. The agent
	// hot-resumes from the run's own scan dir (notes/ledger/execution
	// checkpoint continuity; conversation replay per scan_session). Steering
	// between continuations is PATCHing the task goal/instruction, which the
	// re-dispatch picks up below like a fresh start would.
	terminalRun, hasTerminal := s.taskTerminalRun(r.Context(), store, task.ID)
	if hasTerminal && strings.EqualFold(terminalRun.Status, "stopping") {
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error":       "the task's run is still stopping; retry once it reaches a terminal state",
			"run_id":      terminalRun.ID,
			"instance_id": terminalRun.InstanceID,
		})
		return
	}
	var resumeDir string
	if hasTerminal {
		resumeDir, _ = s.scanDirRecordForRun(terminalRun.InstanceID, terminalRun.LegacyScanID)
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
	// The structured objective of the task rides along with the run: it is
	// operator intent for the loop (an independent prompt section, a
	// human-sourced InfoItem, and the criteria the evaluator observes later).
	// It never widens the authorized scope and never changes what counts as
	// a finding.
	req.Objective = task.Objective
	req.ObjectiveProjectID = project.ID
	req.ObjectiveTaskID = task.ID
	// Task-level HITL switch: snapshot the effective posture (task override or
	// process default) onto the run. The agent gate and the MITM wire gate both
	// read it from the ScanContext.
	req.AuditMode = effectiveTaskAuditMode(task, s.cfg)
	// Round-0 raw material (R-goal): when the task carries no structured
	// objective, the dispatch goroutine analyzes the raw goal before the
	// agent starts (goal_analysis.go roundZeroGoalAnalysis).
	req.ObjectiveGoal = task.Goal
	req.ObjectiveInstruction = task.Instruction
	// Task model chain: the active profile is the run's default credential.
	// An explicit per-run provider_profile still wins (operator override), and
	// an empty chain leaves the legacy server-default resolution untouched.
	if strings.TrimSpace(req.ProviderProfile) == "" && strings.TrimSpace(task.ActiveLLM) != "" {
		req.ProviderProfile = strings.TrimSpace(task.ActiveLLM)
	}
	// The full chain rides along (active entry first) so the LLM client can
	// fail over at the profile level after quota/auth exhaustion, exactly like
	// the key pool does one level down. A single-entry chain stays inert.
	if strings.TrimSpace(req.ProviderProfile) == "" {
		req.ProviderProfileChain = nil
	} else {
		req.ProviderProfileChain = orderedTaskLLMChain(task.LLMChain, req.ProviderProfile)
	}
	// Task context_paths are the Context half of the Task model. They are
	// additive with an explicit request scan_context and all enter the seeded
	// attack surface (never silently reduced to the first path).
	// Task-level outbound proxy pin: validated again here because the pool may
	// have changed since the task was saved (hot-applied settings, restarted
	// process). An unresolvable pin fails the dispatch instead of silently
	// falling back to the global pool.
	req.Proxy = platform.NormalizeTaskProxy(task.Proxy)
	if req.Proxy != "" {
		if err := s.validateTaskProxyPin(req.Proxy); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
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
	instanceID := ""
	resumeSameInstance := hasTerminal
	if resumeSameInstance {
		// Same identity, same scan dir: notes, ledger, execution checkpoint
		// and the engine checkpoint ID (scanCtx.ID) all stay continuous. The
		// hot-resume ladder (checkpoint → conversation replay → briefing)
		// picks the agent's starting context in scan_session.
		instanceID = terminalRun.InstanceID
		// Fresh scan state (REDESIGN D11): re-entering the finished-record
		// state machine via IsResume instantly re-finalizes the run, so the
		// re-dispatch starts clean under the same identity. Continuity is
		// carried instead: durable notes/ledger/intelligence are copied from
		// the previous scan dir (orchestrator) and the conversation is
		// replayed from its journal (scan_session), so the agent resumes warm
		// without resuming the state machine.
		req.ResumeIntent = true
		if resumeDir != "" {
			req.RedispatchFromDir = resumeDir
		}
	} else {
		instanceID = strings.TrimSpace(req.DispatchID)
	}
	callerDispatchID := !resumeSameInstance && instanceID != ""
	if callerDispatchID {
		if !validDispatchID(instanceID) {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "dispatch_id must be 16-128 URL-safe characters"})
			return
		}
	} else if !resumeSameInstance {
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
	if resumeSameInstance {
		// Reopen the terminal snapshot as a pending dispatch under the same
		// id (REDESIGN 2026-11-22): rewriting — not bypassing — the tombstone
		// keeps registerMultiScan's exact-stop guard intact (it now sees a
		// pending snapshot and proceeds, while a stale duplicate of this POST
		// finds the reservation and is rejected). The run mount is reused,
		// never re-created; persistPendingDispatchSnapshot's prior-snapshot-
		// wins rule must not run on this path. reactivated=false means the
		// instance has no snapshot at all (save-only / purged history): the
		// durable queue state below is the acceptance, same as a fresh run.
		reactivated, rerr := s.reactivateTerminalDispatchSnapshot(instanceID, req)
		if rerr != nil {
			ownership.release()
			s.releaseDispatchID(instanceID)
			if errors.Is(rerr, errDispatchBusy) {
				writeJSONStatus(w, http.StatusConflict, map[string]string{"error": rerr.Error()})
			} else {
				writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not reopen the task's run"})
			}
			return
		}
		log.Printf("[platform] task %s run %s re-dispatch on instance %s (snapshot %s)",
			task.ID, terminalRun.ID, instanceID,
			map[bool]string{true: "reactivated", false: "absent; queue state is the acceptance"}[reactivated])
		req.Name = strings.TrimSpace(req.Name)
		req.InstanceID = instanceID
		// Transfer ownership to the orchestrator exactly like the fresh path:
		// without this the handler's flock blocks runMultiScan's own queue
		// acquisition ("queue is owned by another process") and the lock
		// leaks past the handler's return.
		req.queueOwnership = ownership
		if err := s.saveQueueStateDurable(0, req); err != nil {
			if abortErr := s.abortPendingDispatchSnapshot(instanceID, "queue_state_persist_failed"); abortErr != nil {
				log.Printf("[scan] failed to terminalize dispatch %s after queue-state error: %v", instanceID, abortErr)
			}
			ownership.release()
			s.releaseDispatchID(instanceID)
			writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not durably persist resumable dispatch"})
			return
		}
		// The task's content is now consumed by an accepted dispatch. Record
		// its revision so the UI stops offering a plain re-run of the exact
		// same content until the operator edits it (内容变了才给跑).
		s.recordTaskRunRevision(store, task)
		go func() {
			defer s.releaseDispatchID(instanceID)
			s.runMultiScan(req, &scanCfg, instanceID)
		}()
		// Same ack semantics as /api/scan: queued as "pending" until
		// admission. RunID is the existing mount — one task, one run.
		writeJSONStatus(w, http.StatusOK, taskRunAck{Status: "pending", RunID: terminalRun.ID, InstanceID: instanceID})
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
	// Accepted: this exact content has now been dispatched. The revision
	// marker is what lets the task API answer "content_changed_since_run".
	s.recordTaskRunRevision(store, task)
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
		Title:      conversationTitle(task.Name, time.Now()),
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
