package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	// Embed the IANA timezone database so schedules anchored to a Timezone
	// resolve even on minimal container images that ship without tzdata.
	_ "time/tzdata"

	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/safe"
)

type ScanSchedule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Interval string `json:"interval"` // "hourly", "daily", "weekly", "monthly"
	// RunAt anchors the schedule to a wall-clock time of day, "HH:MM" in 24h
	// form. Empty keeps the legacy behavior of firing one interval after the
	// schedule was created or last ran. For "hourly" only the minutes apply.
	RunAt string `json:"run_at,omitempty"`
	// RunDay picks the day within the interval: weekday for "weekly"
	// (0=Sunday … 6=Saturday) and day of month for "monthly" (1-31, clamped to
	// the last day of shorter months). Ignored for the other intervals. No
	// omitempty: 0 is a meaningful weekly value (Sunday).
	RunDay int `json:"run_day"`
	// Timezone is the IANA name RunAt/RunDay are interpreted in, e.g.
	// "America/Argentina/Buenos_Aires". Empty means server local time.
	Timezone       string    `json:"timezone,omitempty"`
	NextRun        time.Time `json:"next_run"`
	LastRun        time.Time `json:"last_run,omitempty"`
	Enabled        bool      `json:"enabled"`
	Targets        []string  `json:"targets"`
	Instruction    string    `json:"instruction,omitempty"`
	ScanMode       string    `json:"scan_mode"`
	SeverityFilter []string  `json:"severity_filter,omitempty"`
	Phases         []int     `json:"phases,omitempty"`
	ReconMode      string    `json:"recon_mode,omitempty"`
	ScanIntensity  string    `json:"scan_intensity,omitempty"`
	CompanyName    string    `json:"company_name,omitempty"`
	LogoPath       string    `json:"logo_path,omitempty"`
	DiscordWebhook string    `json:"discord_webhook,omitempty"`
	Model          string    `json:"model,omitempty"`
}

// runAtPattern validates ScanSchedule.RunAt as a 24h "HH:MM" time of day.
var runAtPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

// scheduleTiming is the comparable subset of a schedule that determines when it
// fires. On update, a change here means NextRun has to be recomputed.
type scheduleTiming struct {
	Interval string
	RunAt    string
	RunDay   int
	Timezone string
}

// timing returns the comparable subset of the schedule that decides when it
// fires, so an update can tell whether NextRun has to be recomputed.
func (sch *ScanSchedule) timing() scheduleTiming {
	return scheduleTiming{
		Interval: sch.Interval,
		RunAt:    sch.RunAt,
		RunDay:   sch.RunDay,
		Timezone: sch.Timezone,
	}
}

// normalizeScheduleTiming validates and canonicalizes the day/time-of-day
// fields. It returns an error for values the scheduler could not honor so the
// API answers 400 instead of silently running at the wrong moment.
func normalizeScheduleTiming(sch *ScanSchedule) error {
	if sch == nil {
		return nil
	}
	sch.RunAt = strings.TrimSpace(sch.RunAt)
	sch.Timezone = strings.TrimSpace(sch.Timezone)

	if sch.RunAt != "" && !runAtPattern.MatchString(sch.RunAt) {
		return fmt.Errorf("run_at must be a 24h time of day such as 09:30, got %q", sch.RunAt)
	}
	if sch.Timezone != "" {
		if _, err := time.LoadLocation(sch.Timezone); err != nil {
			return fmt.Errorf("unknown timezone %q", sch.Timezone)
		}
	}

	switch strings.ToLower(sch.Interval) {
	case "weekly":
		if sch.RunDay < 0 || sch.RunDay > 6 {
			return fmt.Errorf("run_day must be 0 (Sunday) through 6 (Saturday) for weekly schedules, got %d", sch.RunDay)
		}
	case "monthly":
		if sch.RunDay == 0 {
			sch.RunDay = 1
		}
		if sch.RunDay < 1 || sch.RunDay > 31 {
			return fmt.Errorf("run_day must be 1 through 31 for monthly schedules, got %d", sch.RunDay)
		}
	default:
		// Hourly and daily have no day component.
		sch.RunDay = 0
	}
	return nil
}

// scheduleLocation resolves the schedule timezone, falling back to server local
// time for empty or unknown names.
func scheduleLocation(name string) *time.Location {
	if name == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Printf("[SCHEDULER] Unknown timezone %q, falling back to server local time", name)
		return time.Local
	}
	return loc
}

// calculateNextRun returns the next execution instant, always strictly after
// from. With RunAt set the schedule is anchored to that wall-clock time (and to
// RunDay for weekly/monthly); otherwise it simply adds one interval to from.
func calculateNextRun(sch *ScanSchedule, from time.Time) time.Time {
	interval := strings.ToLower(sch.Interval)
	parts := runAtPattern.FindStringSubmatch(sch.RunAt)
	if parts == nil {
		return addInterval(interval, from)
	}
	hour, _ := strconv.Atoi(parts[1])
	minute, _ := strconv.Atoi(parts[2])
	loc := scheduleLocation(sch.Timezone)
	base := from.In(loc)

	switch interval {
	case "hourly":
		// Only the minutes matter: the next :MM of any hour.
		next := time.Date(base.Year(), base.Month(), base.Day(), base.Hour(), minute, 0, 0, loc)
		for !next.After(from) {
			next = next.Add(time.Hour)
		}
		return next
	case "weekly":
		// Wrap rather than trust RunDay: schedules loaded from disk are not
		// re-validated, and an out-of-range weekday must not derail the tick.
		weekday := time.Weekday(((sch.RunDay % 7) + 7) % 7)
		next := time.Date(base.Year(), base.Month(), base.Day(), hour, minute, 0, 0, loc)
		next = next.AddDate(0, 0, (int(weekday)-int(next.Weekday())+7)%7)
		if !next.After(from) {
			next = next.AddDate(0, 0, 7)
		}
		return next
	case "monthly":
		// This month first, then the two following ones — enough to clear any
		// slot that already passed.
		for i := 0; i < 3; i++ {
			month := time.Date(base.Year(), base.Month()+time.Month(i), 1, 0, 0, 0, 0, loc)
			next := monthlyRun(month, sch.RunDay, hour, minute, loc)
			if next.After(from) {
				return next
			}
		}
		return from.AddDate(0, 1, 0)
	default:
		// "daily" and any unknown interval.
		next := time.Date(base.Year(), base.Month(), base.Day(), hour, minute, 0, 0, loc)
		if !next.After(from) {
			next = next.AddDate(0, 0, 1)
		}
		return next
	}
}

// addInterval is the unanchored fallback: one interval after from.
func addInterval(interval string, from time.Time) time.Time {
	switch interval {
	case "hourly":
		return from.Add(time.Hour)
	case "daily":
		return from.AddDate(0, 0, 1)
	case "weekly":
		return from.AddDate(0, 0, 7)
	case "monthly":
		return from.AddDate(0, 1, 0)
	default:
		// Default fallback to 1 day
		return from.AddDate(0, 0, 1)
	}
}

// monthlyRun builds the run instant for day within month, clamping to the last
// day when the month is shorter (31 -> 28 in February).
func monthlyRun(month time.Time, day, hour, minute int, loc *time.Location) time.Time {
	if day < 1 {
		day = 1
	}
	// Day 0 of the following month is the last day of this one.
	if last := time.Date(month.Year(), month.Month()+1, 0, 0, 0, 0, 0, loc).Day(); day > last {
		day = last
	}
	return time.Date(month.Year(), month.Month(), day, hour, minute, 0, 0, loc)
}

// loadSchedulesFromDisk reads schedules directory and loads them into memory.
func (s *Server) loadSchedulesFromDisk() {
	dir := filepath.Join(s.dataDir, "_schedules")
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("[SCHEDULER] Error creating schedules dir: %v", err)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("[SCHEDULER] Error reading schedules dir: %v", err)
		return
	}
	s.schedulesMu.Lock()
	defer s.schedulesMu.Unlock()
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[SCHEDULER] Error reading schedule file %s: %v", path, err)
			continue
		}
		var sch ScanSchedule
		if err := json.Unmarshal(data, &sch); err != nil {
			log.Printf("[SCHEDULER] Error decoding schedule %s: %v", path, err)
			continue
		}
		normalizeScheduleActivity(&sch)
		if err := normalizeScheduleTiming(&sch); err != nil {
			// Keep the schedule: calculateNextRun tolerates the bad value, so
			// losing the whole entry over it would be worse.
			log.Printf("[SCHEDULER] Schedule %s has invalid timing (%v), scheduling defensively", sch.ID, err)
		}
		s.schedules[sch.ID] = &sch
	}
	log.Printf("[SCHEDULER] Loaded %d schedules from disk", len(s.schedules))
}

// saveScheduleToDisk writes a schedule to disk.
func (s *Server) saveScheduleToDisk(sch *ScanSchedule) error {
	dir := filepath.Join(s.dataDir, "_schedules")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(sch, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	path := filepath.Join(dir, sch.ID+".json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath) // best-effort cleanup
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// deleteScheduleFromDisk deletes a schedule file.
func (s *Server) deleteScheduleFromDisk(id string) error {
	path := filepath.Join(s.dataDir, "_schedules", id+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// startScheduler runs the background checker loop.
func (s *Server) startScheduler() {
	// Evaluate overdue schedules immediately on startup so scans missed
	// while the server was down don't wait a full ticker interval.
	s.checkAndRunSchedules()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	log.Printf("[SCHEDULER] Started background scheduler ticker")
	for {
		select {
		case <-s.shutdownChan:
			log.Printf("[SCHEDULER] Stopping background scheduler")
			return
		case <-ticker.C:
			s.checkAndRunSchedules()
		}
	}
}

// checkAndRunSchedules evaluates all schedules and launches due scans.
//
// Lock discipline (R2 scheduler→run closure): schedulesMu is held only to
// snapshot due schedules and advance LastRun/NextRun — never across the
// launch work (task reverse-lookup, project-scope validation, run mount,
// dispatch). The mount can block on the platform store (PostgreSQL, bounded
// by a 5s timeout); holding the lock across it would stall /api/schedules and
// the schedule writers for that long on every 30s tick. The advance happens
// in the locked snapshot phase, so a refused (out-of-scope) or skipped
// (missing task / transient store failure) launch still moves NextRun
// forward — the tick must not retry-storm a permanently broken schedule, it
// only logs the refusal.
func (s *Server) checkAndRunSchedules() {
	defer safe.Recover("scheduler.tick", "")
	for _, sch := range s.collectDueSchedules(time.Now()) {
		s.launchDueSchedule(sch)
	}
}

// collectDueSchedules snapshots and advances every enabled schedule whose
// NextRun has arrived. Callers process each returned copy outside
// schedulesMu. The struct copy (diskCopy := *sch pattern) detaches the
// snapshot from concurrent PUT mutations; the Targets backing array is never
// mutated in place, so reading it lock-free is safe.
func (s *Server) collectDueSchedules(now time.Time) []ScanSchedule {
	s.schedulesMu.Lock()
	defer s.schedulesMu.Unlock()
	var due []ScanSchedule
	for _, sch := range s.schedules {
		if sch == nil || !sch.Enabled {
			continue
		}
		if now.After(sch.NextRun) || now.Equal(sch.NextRun) {
			log.Printf("[SCHEDULER] Triggering scheduled scan: %s (Targets: %v)", sch.Name, sch.Targets)
			sch.LastRun = now
			sch.NextRun = calculateNextRun(sch, now)
			due = append(due, *sch)
		}
	}
	return due
}

// launchDueSchedule processes one due schedule snapshot outside schedulesMu:
// persist the tick advance (file I/O moved out of the lock), enforce the
// project scope (D7), then dispatch and mount. The per-schedule recover keeps
// one bad schedule from derailing the rest of the tick (same semantics as the
// previous in-lock closure).
func (s *Server) launchDueSchedule(sch ScanSchedule) {
	defer safe.Recover("scheduler."+sch.ID, "")

	if err := s.saveScheduleToDisk(&sch); err != nil {
		log.Printf("[SCHEDULER] Error saving triggered schedule %s: %v", sch.ID, err)
	}

	authCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	taskID, authErr := s.authorizeScheduledScan(authCtx, &sch)
	cancel()
	if authErr != nil {
		// NextRun already advanced in collectDueSchedules — nothing left to
		// bookkeep, the refusal only needs to be visible in the log.
		log.Printf("[SCHEDULER] %v (NextRun already advanced)", authErr)
		return
	}

	req := scanRequestForSchedule(&sch)
	scanCfg := s.configSnapshot()
	if scanCfg == nil {
		log.Printf("[SCHEDULER] schedule %s skipped: config snapshot unavailable", sch.ID)
		return
	}
	if sch.Model != "" {
		scanCfg.LLM = sch.Model
	}
	s.dispatchScheduledScan(req, scanCfg, &sch, taskID, "schedule")
}

// scanRequestForSchedule maps a schedule onto the dispatch request — the same
// field mapping the tick and the manual trigger endpoint have always used.
func scanRequestForSchedule(sch *ScanSchedule) ScanRequest {
	return ScanRequest{
		Targets:        sch.Targets,
		Instruction:    sch.Instruction,
		ScanMode:       sch.ScanMode,
		SeverityFilter: sch.SeverityFilter,
		Phases:         sch.Phases,
		ReconMode:      sch.ReconMode,
		ScanIntensity:  sch.ScanIntensity,
		CompanyName:    sch.CompanyName,
		LogoPath:       sch.LogoPath,
		DiscordWebhook: sch.DiscordWebhook,
		Name:           sch.Name + " (Scheduled)",
		Model:          sch.Model,
	}
}

// errScheduleLinkUnavailable marks a transient platform-store failure during
// the scheduler-entry scope check — distinct from a definitive refusal: the
// tick skips the cycle (fail-closed; NextRun already advanced so no retry
// storm), the trigger endpoint answers 503 so the operator retries instead
// of reconfiguring.
var errScheduleLinkUnavailable = errors.New("schedule linkage unavailable")

// authorizeScheduledScan enforces the project-scope boundary for one
// schedule-driven dispatch (REDESIGN §3.3 D7 — closes the transitional
// direct-dispatch path documented in the R1 landing notes).
//
// Authorization source: the schedule task (tasks.context.legacy_schedule_id,
// linked by the startup importer and by every create/update on
// /api/schedules via ensureScheduleTask) ties the schedule to a project.
// When that project's scope has targets, every schedule target must fall
// inside it — the same fail-closed rule as POST /api/tasks/{id}/runs. A
// project with an empty scope (the default-import state) keeps the
// schedule's own targets as the authorization source: they are the
// operator's explicit configuration that predates the platform.
//
// With NO platform store the schedule's targets stay the authorization
// source and no run mounts (legacy direct dispatch, charter §6.6 — scanning
// never depends on the platform store). With a store configured, every
// unexpected state is a skip, not a bypass: a missing task, a missing
// project or a transient store error returns a non-nil error and the caller
// skips this cycle — a broken linkage must never silently widen the target
// authorization to "whatever the schedule says".
//
// It returns the task ID to mount the run on ("" only for the nil-store
// direct dispatch) and a non-nil error when the dispatch must be skipped
// (scope violation, broken linkage, transient store failure).
func (s *Server) authorizeScheduledScan(ctx context.Context, sch *ScanSchedule) (string, error) {
	store := s.currentPlatformStore()
	if store == nil {
		return "", nil
	}
	defaultProject, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		log.Printf("[platform] scheduler scope check: resolve default project: %v", err)
		return "", fmt.Errorf("%w: resolve default project: %v", errScheduleLinkUnavailable, err)
	}
	task, err := store.TaskByLegacyScheduleID(ctx, defaultProject.ID, sch.ID)
	if errors.Is(err, platform.ErrNotFound) {
		log.Printf("[platform] scheduler scope check: schedule %s task missing — run skipped, will retry next cycle", sch.ID)
		return "", fmt.Errorf("schedule %q has no linked schedule task (scope cannot be validated) — run skipped", sch.Name)
	}
	if err != nil {
		log.Printf("[platform] scheduler scope check: task lookup for schedule %s: %v", sch.ID, err)
		return "", fmt.Errorf("%w: task lookup for schedule %s: %v", errScheduleLinkUnavailable, sch.ID, err)
	}
	project, err := store.GetProject(ctx, task.ProjectID)
	if errors.Is(err, platform.ErrNotFound) {
		// The task outlived its project (deleted outside the cascade
		// contract): the authorization source is gone, so nothing may run.
		log.Printf("[platform] scheduler scope check: project %s for schedule %s missing — run skipped, will retry next cycle", task.ProjectID, sch.ID)
		return "", fmt.Errorf("schedule %q project %s is missing — run skipped", sch.Name, task.ProjectID)
	}
	if err != nil {
		log.Printf("[platform] scheduler scope check: project %s for schedule %s: %v", task.ProjectID, sch.ID, err)
		return "", fmt.Errorf("%w: project lookup for schedule %s: %v", errScheduleLinkUnavailable, sch.ID, err)
	}
	if len(project.Scope.Targets) == 0 {
		// Default-import state: the schedule's targets are the operator's
		// own authorization fact (empty-scope compatibility ruling).
		return task.ID, nil
	}
	targets := flattenPlatformTargets(sch.Targets)
	if err := validateTargetsInScope(targets, project.Scope); err != nil {
		return "", fmt.Errorf("schedule %q refused: %v", sch.Name, err)
	}
	return task.ID, nil
}

// ensureScheduleTask links a schedule to its schedule task at write time
// (POST /api/schedules create, PUT update): the startup importer only runs
// once, so a schedule created at runtime would otherwise have no task for
// the scheduler→run mount to resolve and every tick would skip it. The
// lookup is the idempotent find-or-create ImportScheduleTask contract, so a
// full re-link on PUT is harmless. Failure is logged, never fatal — the
// schedule file itself is already persisted, and the tick's skip-on-missing
// posture keeps an unlinked schedule from dispatching unscooped (charter
// §6.6 parity with the rest of the platform write paths).
func (s *Server) ensureScheduleTask(sch *ScanSchedule) {
	store := s.currentPlatformStore()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		log.Printf("[platform] schedule task link: resolve default project: %v", err)
		return
	}
	if _, _, err := store.ImportScheduleTask(ctx, project.ID, legacyScheduleToTaskInput(sch), sch.ID); err != nil {
		log.Printf("[platform] schedule task link for %s (%s): %v", sch.ID, sch.Name, err)
	}
}

// dispatchScheduledScan launches one schedule-driven scan and, when a task
// linkage exists, mounts the run record synchronously BEFORE the dispatch
// goroutine — the mount is part of the tick's /api/schedules-facing work and
// callers can assert it without racing the scan goroutine. The mount uses a
// detached context with a short timeout (handleTaskRunStart precedent): a
// wedged store must not cancel the dispatch, and a mount failure must not
// undo it — the scan runs, the failure is logged. The queue-ownership shape
// stays the legacy direct-dispatch one (registerMultiScan acquires it
// internally); the reserve/persist/saveQueueState sequence of POST
// /api/tasks/{id}/runs is deliberately not copied here.
func (s *Server) dispatchScheduledScan(req ScanRequest, scanCfg *config.Config, sch *ScanSchedule, taskID, trigger string) string {
	instanceID := randomSlug()
	if taskID != "" {
		if store := s.currentPlatformStore(); store != nil {
			mountCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := store.CreateRun(mountCtx, taskID, platform.RunMount{
				InstanceID: instanceID,
				Mode:       sch.ScanMode,
				Trigger:    trigger,
			})
			cancel()
			if err != nil {
				log.Printf("[platform] scheduled scan %s accepted but run mount on task %s failed: %v", instanceID, taskID, err)
			}
		}
	}
	go s.runMultiScan(req, scanCfg, instanceID)
	return instanceID
}
