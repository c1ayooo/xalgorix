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

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/safe"
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

	// ── project binding & target mode (v4.6) ────────────────────────────────
	// ProjectID binds the schedule to a project. Empty keeps the legacy
	// behavior: the implicit default project governs the scope check.
	ProjectID string `json:"project_id,omitempty"`
	// Mode selects what a tick does:
	//   ""/"scan"      launch a scan from Targets (legacy, default);
	//   "create_task"  create a NEW task in ProjectID on every tick, then run it;
	//   "trigger_task" run the EXISTING task TaskID.
	// All three stay inside the project scope (D7) — the mode only changes
	// which task record the run mounts on.
	Mode string `json:"mode,omitempty"`
	// TaskID is the existing task a trigger_task schedule drives.
	TaskID string `json:"task_id,omitempty"`
	// TaskGoal is the goal of the task created by a create_task tick (falls
	// back to Instruction so legacy schedules keep a meaningful goal).
	TaskGoal string `json:"task_goal,omitempty"`
	// ContextPaths are the per-project task files the tick attaches to a
	// created task (upload landing zone: <dataDir>/projects/<id>/tasks/...).
	ContextPaths []string `json:"context_paths,omitempty"`

	// ── execution window (v4.6) ─────────────────────────────────────────────
	// WindowStart/WindowEnd bound the daily execution window as "HH:MM" in
	// Timezone. Both empty = no window (always eligible). A start later than
	// the end is a cross-midnight window. When the end is reached,
	// WindowEndAction decides what happens to the running dispatch:
	//   "pause"  (default) pause the run, resume on the next window;
	//   "finish" stop the run and let the schedule wait for the next window.
	WindowStart     string `json:"window_start,omitempty"`
	WindowEnd       string `json:"window_end,omitempty"`
	WindowEndAction string `json:"window_end_action,omitempty"`
	// RuntimeInstanceID is the instance the last tick dispatched; the window
	// closer looks it up to pause/stop exactly that run.
	RuntimeInstanceID string `json:"runtime_instance_id,omitempty"`
	// WindowClosedOn is the YYYY-MM-DD (in Timezone) of the last window close
	// already handled, so one window end is acted on exactly once.
	WindowClosedOn string `json:"window_closed_on,omitempty"`
}

// runAtPattern validates ScanSchedule.RunAt as a 24h "HH:MM" time of day.
var runAtPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

// scheduleTiming is the comparable subset of a schedule that determines when it
// fires. On update, a change here means NextRun has to be recomputed.
type scheduleTiming struct {
	Interval    string
	RunAt       string
	RunDay      int
	Timezone    string
	WindowStart string
	WindowEnd   string
}

// timing returns the comparable subset of the schedule that decides when it
// fires, so an update can tell whether NextRun has to be recomputed.
func (sch *ScanSchedule) timing() scheduleTiming {
	return scheduleTiming{
		Interval:    sch.Interval,
		RunAt:       sch.RunAt,
		RunDay:      sch.RunDay,
		Timezone:    sch.Timezone,
		WindowStart: sch.WindowStart,
		WindowEnd:   sch.WindowEnd,
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
	sch.ProjectID = strings.TrimSpace(sch.ProjectID)
	sch.TaskID = strings.TrimSpace(sch.TaskID)
	sch.Mode = normalizeScheduleMode(sch.Mode)

	if sch.RunAt != "" && !runAtPattern.MatchString(sch.RunAt) {
		return fmt.Errorf("run_at must be a 24h time of day such as 09:30, got %q", sch.RunAt)
	}
	if sch.Timezone != "" {
		if _, err := time.LoadLocation(sch.Timezone); err != nil {
			return fmt.Errorf("unknown timezone %q", sch.Timezone)
		}
	}
	if err := normalizeScheduleWindow(sch); err != nil {
		return err
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

// Schedule target modes (v4.6): what a due tick does.
const (
	ScheduleModeScan        = "scan"
	ScheduleModeCreateTask  = "create_task"
	ScheduleModeTriggerTask = "trigger_task"
)

// Window end actions (v4.6).
const (
	ScheduleWindowPause  = "pause"
	ScheduleWindowFinish = "finish"
)

// normalizeScheduleMode canonicalizes the mode field; unknown values fall back
// to the legacy direct-scan behavior instead of failing a persisted schedule.
func normalizeScheduleMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ScheduleModeCreateTask:
		return ScheduleModeCreateTask
	case ScheduleModeTriggerTask:
		return ScheduleModeTriggerTask
	default:
		return ScheduleModeScan
	}
}

// normalizeScheduleWindow validates and canonicalizes the execution window.
// Both bounds are required together; a start after the end is a valid
// cross-midnight window.
func normalizeScheduleWindow(sch *ScanSchedule) error {
	sch.WindowStart = strings.TrimSpace(sch.WindowStart)
	sch.WindowEnd = strings.TrimSpace(sch.WindowEnd)
	sch.WindowEndAction = strings.ToLower(strings.TrimSpace(sch.WindowEndAction))
	if sch.WindowStart == "" && sch.WindowEnd == "" {
		sch.WindowEndAction = ""
		return nil
	}
	if sch.WindowStart == "" || sch.WindowEnd == "" {
		return fmt.Errorf("window_start and window_end must be set together")
	}
	if !runAtPattern.MatchString(sch.WindowStart) || !runAtPattern.MatchString(sch.WindowEnd) {
		return fmt.Errorf("window_start/window_end must be 24h times of day such as 09:30")
	}
	if sch.WindowStart == sch.WindowEnd {
		return fmt.Errorf("window_start and window_end must differ")
	}
	switch sch.WindowEndAction {
	case "":
		sch.WindowEndAction = ScheduleWindowPause
	case ScheduleWindowPause, ScheduleWindowFinish:
	default:
		return fmt.Errorf("window_end_action must be pause or finish, got %q", sch.WindowEndAction)
	}
	return nil
}

// parseClockMinutes turns an "HH:MM" time of day into minutes since midnight.
func parseClockMinutes(value string) (int, bool) {
	parts := runAtPattern.FindStringSubmatch(strings.TrimSpace(value))
	if parts == nil {
		return 0, false
	}
	hour, _ := strconv.Atoi(parts[1])
	minute, _ := strconv.Atoi(parts[2])
	return hour*60 + minute, true
}

// hasExecutionWindow reports whether the schedule bounds execution hours.
func hasExecutionWindow(sch *ScanSchedule) bool {
	return sch != nil && sch.WindowStart != "" && sch.WindowEnd != ""
}

// inExecutionWindow reports whether now falls inside [WindowStart, WindowEnd).
// A start later than the end is a cross-midnight window (e.g. 22:00-06:00).
func inExecutionWindow(sch *ScanSchedule, now time.Time) bool {
	if !hasExecutionWindow(sch) {
		return true
	}
	start, okStart := parseClockMinutes(sch.WindowStart)
	end, okEnd := parseClockMinutes(sch.WindowEnd)
	if !okStart || !okEnd {
		return true // persisted garbage: never block a tick on it
	}
	local := now.In(scheduleLocation(sch.Timezone))
	minute := local.Hour()*60 + local.Minute()
	if start <= end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

// nextWindowStart returns the first window opening strictly after from.
func nextWindowStart(sch *ScanSchedule, from time.Time) time.Time {
	start, ok := parseClockMinutes(sch.WindowStart)
	if !ok {
		return from.Add(time.Hour)
	}
	loc := scheduleLocation(sch.Timezone)
	base := from.In(loc)
	hour, minute := start/60, start%60
	next := time.Date(base.Year(), base.Month(), base.Day(), hour, minute, 0, 0, loc)
	if !next.After(from) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// currentWindowEnd returns today's window end instant in the schedule's
// timezone. For a cross-midnight window the "end" is the closing instant of the
// window that opened earlier (possibly yesterday).
func currentWindowEnd(sch *ScanSchedule, now time.Time) time.Time {
	end, ok := parseClockMinutes(sch.WindowEnd)
	if !ok {
		return time.Time{}
	}
	loc := scheduleLocation(sch.Timezone)
	base := now.In(loc)
	hour, minute := end/60, end%60
	return time.Date(base.Year(), base.Month(), base.Day(), hour, minute, 0, 0, loc)
}

// adjustNextRunForWindow moves a candidate NextRun into the execution window:
// if it lands outside, the tick is postponed to the next window opening rather
// than silently skipped for a whole day.
func adjustNextRunForWindow(sch *ScanSchedule, next time.Time) time.Time {
	if !hasExecutionWindow(sch) {
		return next
	}
	if inExecutionWindow(sch, next) {
		return next
	}
	return nextWindowStart(sch, next)
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
	// Scope observer baseline (v4.6): an empty rules table is the migrated
	// default, so materialize the preset rules once before the first tick.
	s.seedAllScopeApprovalRules()
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
	now := time.Now()
	// Window closes first: a run that outlived its window is paused/stopped
	// before the same tick may dispatch the next interval.
	s.enforceScheduleWindows(now)
	due, rescheduled := s.collectDueSchedules(now)
	for _, sch := range rescheduled {
		if err := s.saveScheduleToDisk(&sch); err != nil {
			log.Printf("[SCHEDULER] Error persisting window-deferred schedule %s: %v", sch.ID, err)
		}
	}
	for _, sch := range due {
		s.launchDueSchedule(sch)
	}
}

// collectDueSchedules snapshots and advances every enabled schedule whose
// NextRun has arrived. Callers process each returned copy outside
// schedulesMu. The struct copy (diskCopy := *sch pattern) detaches the
// snapshot from concurrent PUT mutations; the Targets backing array is never
// mutated in place, so reading it lock-free is safe.
// rescheduled carries schedules whose NextRun moved without firing (execution
// window not open yet) so the caller can persist the advance outside the lock.
func (s *Server) collectDueSchedules(now time.Time) (due []ScanSchedule, rescheduled []ScanSchedule) {
	s.schedulesMu.Lock()
	defer s.schedulesMu.Unlock()
	for _, sch := range s.schedules {
		if sch == nil || !sch.Enabled {
			continue
		}
		if now.Before(sch.NextRun) {
			continue
		}
		if !inExecutionWindow(sch, now) {
			// Outside the window: postpone to the next opening instead of
			// skipping this interval entirely (a daily 20:00 run inside a
			// 09:00-18:00 window must still run at 09:00).
			sch.NextRun = nextWindowStart(sch, now)
			log.Printf("[SCHEDULER] %s outside execution window %s-%s — next attempt %s",
				sch.Name, sch.WindowStart, sch.WindowEnd, sch.NextRun.Format(time.RFC3339))
			rescheduled = append(rescheduled, *sch)
			continue
		}
		log.Printf("[SCHEDULER] Triggering scheduled scan: %s (Targets: %v, mode=%s)", sch.Name, sch.Targets, sch.mode())
		sch.LastRun = now
		sch.NextRun = adjustNextRunForWindow(sch, calculateNextRun(sch, now))
		due = append(due, *sch)
	}
	return due, rescheduled
}

// mode returns the effective target mode (defaults to legacy direct scan).
func (sch *ScanSchedule) mode() string {
	if sch == nil {
		return ScheduleModeScan
	}
	return normalizeScheduleMode(sch.Mode)
}

// enforceScheduleWindows closes the daily execution window of every enabled
// schedule whose end instant has passed. The close is acted on once per day
// (WindowClosedOn) and only affects the dispatch this schedule started.
func (s *Server) enforceScheduleWindows(now time.Time) {
	s.schedulesMu.Lock()
	closing := make([]ScanSchedule, 0, 1)
	for _, sch := range s.schedules {
		if sch == nil || !sch.Enabled || !hasExecutionWindow(sch) {
			continue
		}
		if inExecutionWindow(sch, now) {
			continue
		}
		end := currentWindowEnd(sch, now)
		if end.IsZero() || now.Before(end) {
			continue
		}
		day := now.In(scheduleLocation(sch.Timezone)).Format("2006-01-02")
		if sch.WindowClosedOn == day {
			continue
		}
		sch.WindowClosedOn = day
		sch.NextRun = nextWindowStart(sch, now)
		closing = append(closing, *sch)
	}
	s.schedulesMu.Unlock()

	for _, sch := range closing {
		s.closeScheduleWindow(&sch)
		if err := s.saveScheduleToDisk(&sch); err != nil {
			log.Printf("[SCHEDULER] Error persisting window close for %s: %v", sch.ID, err)
		}
	}
}

// closeScheduleWindow applies the schedule's window_end_action to the dispatch
// it started, then clears the runtime pointer.
func (s *Server) closeScheduleWindow(sch *ScanSchedule) {
	if sch == nil {
		return
	}
	action := sch.WindowEndAction
	if action == "" {
		action = ScheduleWindowPause
	}
	if inst := strings.TrimSpace(sch.RuntimeInstanceID); inst != "" {
		switch action {
		case ScheduleWindowFinish:
			log.Printf("[SCHEDULER] window end for %s — stopping run %s", sch.Name, inst)
			s.stopInstanceForSchedule(inst, "schedule execution window ended")
		default:
			log.Printf("[SCHEDULER] window end for %s — pausing run %s", sch.Name, inst)
			s.pauseInstanceForSchedule(inst, "schedule execution window ended")
		}
	}
	// Clear the runtime pointer under the lock and mirror it on the disk copy
	// the caller persists.
	s.schedulesMu.Lock()
	if live := s.schedules[sch.ID]; live != nil {
		live.RuntimeInstanceID = ""
	}
	s.schedulesMu.Unlock()
	sch.RuntimeInstanceID = ""
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
	target, authErr := s.authorizeScheduledScan(authCtx, &sch)
	cancel()
	if authErr != nil {
		// NextRun already advanced in collectDueSchedules — nothing left to
		// bookkeep, the refusal only needs to be visible in the log.
		log.Printf("[SCHEDULER] %v (NextRun already advanced)", authErr)
		return
	}
	taskID := target.TaskID
	// create_task mode mints a fresh task per tick inside the bound project;
	// the run then mounts on it exactly like a trigger_task dispatch.
	if sch.mode() == ScheduleModeCreateTask && target.Project != nil {
		created, err := s.createTaskForSchedule(&sch, target.Project.ID)
		if err != nil {
			log.Printf("[SCHEDULER] schedule %s: create task in project %s: %v", sch.ID, target.Project.ID, err)
			return
		}
		taskID = created.ID
		log.Printf("[SCHEDULER] schedule %s created task %s (%s)", sch.ID, created.ID, created.Name)
	}

	// One-shot guard (same rule as POST /api/tasks/{id}/runs): a task runs one
	// execution at a time. If the previous run is still active, skip this
	// cycle instead of stacking a concurrent run on the same goal — NextRun
	// already advanced, so the schedule simply waits for the next tick.
	if taskID != "" {
		if store := s.currentPlatformStore(); store != nil {
			guardCtx, guardCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if active, hasActive := s.taskActiveRun(guardCtx, store, taskID); hasActive {
				guardCancel()
				log.Printf("[SCHEDULER] schedule %s skipped: task %s already has an active run (%s)", sch.ID, taskID, active.InstanceID)
				return
			}
			guardCancel()
		}
	}

	req := scanRequestForSchedule(&sch)
	// Schedule-level context files (create_task carries them onto the task; for
	// scan mode they are the schedule's own attachments).
	if len(req.ScanContextPaths) == 0 && len(sch.ContextPaths) > 0 {
		req.ScanContextPaths = append([]string(nil), sch.ContextPaths...)
	}
	// Project-scope fallback: a bound schedule without explicit targets drives
	// the project's authorized targets (the dispatch must still name what it
	// scans — the run's briefing and the scope guard read this list).
	if len(req.Targets) == 0 && target.Project != nil && len(target.Project.Scope.Targets) > 0 {
		req.Targets = append([]string(nil), target.Project.Scope.Targets...)
	}
	// Objective coordinates (R-goal): a scheduled run rides its linked task's
	// structured objective exactly like POST /api/tasks/{id}/runs, and carries
	// the raw goal/instruction so round-0 analysis (goal_analysis.go) can
	// structure an unstructured goal before the agent starts. Lookup failure
	// only loses the objective, never the dispatch.
	if taskID != "" {
		if store := s.currentPlatformStore(); store != nil {
			objCtx, objCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if task, err := store.GetTask(objCtx, taskID); err == nil {
				req.Objective = task.Objective
				req.ObjectiveProjectID = task.ProjectID
				req.ObjectiveTaskID = task.ID
				req.ObjectiveGoal = task.Goal
				req.ObjectiveInstruction = task.Instruction
				// Task context files (the project/task upload landing zone)
				// ride the dispatch exactly like POST /api/tasks/{id}/runs.
				if len(task.ContextPaths) > 0 {
					req.ScanContextPaths = append([]string(nil), task.ContextPaths...)
				}
			} else {
				log.Printf("[SCHEDULER] schedule %s: task %s objective lookup: %v", sch.ID, taskID, err)
			}
			objCancel()
		}
	}
	scanCfg := s.configSnapshot()
	if scanCfg == nil {
		log.Printf("[SCHEDULER] schedule %s skipped: config snapshot unavailable", sch.ID)
		return
	}
	if sch.Model != "" {
		scanCfg.LLM = sch.Model
	}
	instanceID := s.dispatchScheduledScan(req, scanCfg, &sch, taskID, "schedule")
	// Window enforcement needs the exact dispatch: record it so the closer can
	// pause/stop this run when WindowEnd arrives.
	if hasExecutionWindow(&sch) && instanceID != "" {
		sch.RuntimeInstanceID = instanceID
		s.schedulesMu.Lock()
		if live := s.schedules[sch.ID]; live != nil {
			live.RuntimeInstanceID = instanceID
		}
		s.schedulesMu.Unlock()
		if err := s.saveScheduleToDisk(&sch); err != nil {
			log.Printf("[SCHEDULER] Error recording runtime instance for %s: %v", sch.ID, err)
		}
	}
}

// createTaskForSchedule mints the task a create_task tick runs. The name is
// time-stamped so successive ticks remain distinguishable in the task list; the
// goal comes from TaskGoal, falling back to Instruction (the legacy schedule
// field) so an imported schedule still produces a meaningful task.
func (s *Server) createTaskForSchedule(sch *ScanSchedule, projectID string) (*platform.Task, error) {
	store := s.currentPlatformStore()
	if store == nil {
		return nil, fmt.Errorf("platform store unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	goal := strings.TrimSpace(sch.TaskGoal)
	if goal == "" {
		goal = strings.TrimSpace(sch.Instruction)
	}
	name := strings.TrimSpace(sch.Name)
	if name == "" {
		name = "Scheduled task"
	}
	name = fmt.Sprintf("%s · %s", name, time.Now().Format("2006-01-02 15:04"))
	policy := &platform.PhasePolicy{
		Phases:         append([]int(nil), sch.Phases...),
		SeverityFilter: append([]string(nil), sch.SeverityFilter...),
		ScanMode:       sch.ScanMode,
		Intensity:      sch.ScanIntensity,
	}
	return store.CreateTask(ctx, projectID, platform.TaskInput{
		Name:         name,
		Goal:         goal,
		Instruction:  sch.Instruction,
		PhasePolicy:  policy,
		ContextPaths: append([]string(nil), sch.ContextPaths...),
	})
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
// It returns the task/project the dispatch is authorized against ("" task only
// for the nil-store direct dispatch) and a non-nil error when the dispatch must
// be skipped (scope violation, broken linkage, transient store failure).
func (s *Server) authorizeScheduledScan(ctx context.Context, sch *ScanSchedule) (scheduleAuthTarget, error) {
	store := s.currentPlatformStore()
	if store == nil {
		return scheduleAuthTarget{}, nil
	}

	// Resolve the bound project. Explicit ProjectID wins; empty keeps the
	// legacy default-project linkage so pre-v4.6 schedules are untouched.
	var project *platform.Project
	if projectID := strings.TrimSpace(sch.ProjectID); projectID != "" {
		p, err := store.GetProject(ctx, projectID)
		if errors.Is(err, platform.ErrNotFound) {
			return scheduleAuthTarget{}, fmt.Errorf("schedule %q bound project %s is missing — run skipped", sch.Name, projectID)
		}
		if err != nil {
			return scheduleAuthTarget{}, fmt.Errorf("%w: project lookup %s: %w", errScheduleLinkUnavailable, projectID, err)
		}
		project = p
	} else {
		p, err := store.EnsureDefaultProject(ctx)
		if err != nil {
			log.Printf("[platform] scheduler scope check: resolve default project: %v", err)
			return scheduleAuthTarget{}, fmt.Errorf("%w: resolve default project: %w", errScheduleLinkUnavailable, err)
		}
		project = p
	}

	target := scheduleAuthTarget{Project: project}
	switch sch.mode() {
	case ScheduleModeTriggerTask:
		// Trigger-an-existing-task: the task IS the authorization subject, and
		// it must live in the bound project (a schedule cannot drive a task
		// from another project's scope).
		if strings.TrimSpace(sch.TaskID) == "" {
			return scheduleAuthTarget{}, fmt.Errorf("schedule %q has mode=trigger_task but no task_id — run skipped", sch.Name)
		}
		task, err := store.GetTask(ctx, sch.TaskID)
		if errors.Is(err, platform.ErrNotFound) {
			return scheduleAuthTarget{}, fmt.Errorf("schedule %q target task %s is missing — run skipped", sch.Name, sch.TaskID)
		}
		if err != nil {
			return scheduleAuthTarget{}, fmt.Errorf("%w: task lookup %s: %w", errScheduleLinkUnavailable, sch.TaskID, err)
		}
		if strings.TrimSpace(sch.ProjectID) != "" && task.ProjectID != project.ID {
			return scheduleAuthTarget{}, fmt.Errorf("schedule %q target task %s belongs to project %s, not the bound project %s — run skipped",
				sch.Name, task.ID, task.ProjectID, project.ID)
		}
		// Follow the task's own project for the scope check (it is the
		// authority the run will actually live under).
		if taskProject, perr := store.GetProject(ctx, task.ProjectID); perr == nil {
			project = taskProject
			target.Project = taskProject
		}
		target.TaskID = task.ID
	case ScheduleModeCreateTask:
		// A new task is minted per tick in the bound project; authorization
		// source is the project scope.
	default:
		// Legacy direct scan: the schedule task (tasks.context.legacy_schedule_id)
		// ties the schedule to its project.
		task, err := store.TaskByLegacyScheduleID(ctx, project.ID, sch.ID)
		if errors.Is(err, platform.ErrNotFound) {
			log.Printf("[platform] scheduler scope check: schedule %s task missing — run skipped, will retry next cycle", sch.ID)
			return scheduleAuthTarget{}, fmt.Errorf("schedule %q has no linked schedule task (scope cannot be validated) — run skipped", sch.Name)
		}
		if err != nil {
			log.Printf("[platform] scheduler scope check: task lookup for schedule %s: %v", sch.ID, err)
			return scheduleAuthTarget{}, fmt.Errorf("%w: task lookup for schedule %s: %w", errScheduleLinkUnavailable, sch.ID, err)
		}
		target.TaskID = task.ID
	}

	if len(project.Scope.Targets) == 0 {
		// Default-import state: the schedule's targets are the operator's
		// own authorization fact (empty-scope compatibility ruling).
		return target, nil
	}
	targets := flattenPlatformTargets(sch.Targets)
	if len(targets) == 0 {
		// No explicit targets: the project's authorized targets are the
		// dispatch's subject, so nothing to validate here.
		return target, nil
	}
	if err := validateTargetsInScope(targets, project.Scope); err != nil {
		return scheduleAuthTarget{}, fmt.Errorf("schedule %q refused: %w", sch.Name, err)
	}
	return target, nil
}

// validateScheduleTargets enforces the per-mode requirement: a legacy scan
// schedule must name targets, trigger_task must name a task, and create_task may
// omit both (the bound project's scope supplies the targets at dispatch time).
func validateScheduleTargets(sch *ScanSchedule) error {
	if sch == nil {
		return nil
	}
	switch sch.mode() {
	case ScheduleModeTriggerTask:
		if strings.TrimSpace(sch.TaskID) == "" {
			return fmt.Errorf("task_id is required for trigger_task schedules")
		}
		return nil
	case ScheduleModeCreateTask:
		if strings.TrimSpace(sch.ProjectID) == "" && len(sch.Targets) == 0 {
			return fmt.Errorf("create_task schedules require project_id (or explicit targets)")
		}
		return nil
	default:
		if len(sch.Targets) == 0 {
			return fmt.Errorf("targets are required")
		}
		return nil
	}
}

// scheduleAuthTarget is the scheduler's authorization answer: which task the
// run mounts on and which project scope governs it.
type scheduleAuthTarget struct {
	TaskID  string
	Project *platform.Project
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
	// trigger_task drives an operator-chosen task; minting a schedule task too
	// would create a second, unused task per schedule.
	if sch.mode() == ScheduleModeTriggerTask {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var project *platform.Project
	var err error
	if projectID := strings.TrimSpace(sch.ProjectID); projectID != "" {
		project, err = store.GetProject(ctx, projectID)
	} else {
		project, err = store.EnsureDefaultProject(ctx)
	}
	if err != nil {
		log.Printf("[platform] schedule task link: resolve project: %v", err)
		return
	}
	if _, _, err := store.ImportScheduleTask(ctx, project.ID, legacyScheduleToTaskInput(sch), sch.ID); err != nil {
		log.Printf("[platform] schedule task link for %s (%s): %v", sch.ID, sch.Name, err)
	}
}

// pauseInstanceForSchedule pauses the dispatch a schedule started when its
// execution window closes (window_end_action=pause, the default). Mirrors the
// per-instance pause endpoint: status flip + context cancel + pending-approval
// batch cancel so no approval outlives the paused run.
func (s *Server) pauseInstanceForSchedule(instanceID, reason string) {
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		return
	}
	inst.mu.Lock()
	if inst.Status != "running" && inst.Status != "pending" {
		inst.mu.Unlock()
		return
	}
	inst.Status = "paused"
	inst.StopReason = "schedule_window_paused"
	cancel := inst.cancel
	inst.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.cancelPendingApprovalsForInstance(instanceID, "run paused by schedule execution window")
	s.broadcastToInstance(instanceID, WSEvent{Type: "paused", Content: reason})
	s.broadcastDashboard(WSEvent{Type: "instance_updated", Content: instanceID})
	s.notifyAdmissionWake()
}

// stopInstanceForSchedule ends the dispatch a schedule started when its
// execution window closes with window_end_action=finish.
func (s *Server) stopInstanceForSchedule(instanceID, reason string) {
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		return
	}
	inst.mu.Lock()
	if inst.Status != "running" && inst.Status != "pending" && inst.Status != "paused" {
		inst.mu.Unlock()
		return
	}
	inst.Status = "stopped"
	inst.StopReason = "schedule_window_end"
	inst.FinishedAt = time.Now().Format(time.RFC3339Nano)
	cancel := inst.cancel
	inst.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.cancelPendingApprovalsForInstance(instanceID, "run stopped by schedule execution window")
	s.persistIdleInstanceStop(inst)
	s.broadcastToInstance(instanceID, WSEvent{Type: "stopped", Content: reason})
	s.broadcastDashboard(WSEvent{Type: "instance_updated", Content: instanceID})
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
			// Title the conversation after its task so the chat sidebar has
			// something readable before the first human message arrives; a
			// best-effort GetTask keeps the dispatch path unchanged on error.
			title := ""
			if task, getErr := store.GetTask(mountCtx, taskID); getErr == nil && task != nil {
				title = conversationTitle(task.Name, time.Now())
			}
			_, err := store.CreateRun(mountCtx, taskID, platform.RunMount{
				InstanceID: instanceID,
				Mode:       sch.ScanMode,
				Trigger:    trigger,
				Title:      title,
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
