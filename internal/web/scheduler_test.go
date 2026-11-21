package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

func TestCalculateNextRun(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		interval string
		want     time.Time
	}{
		{"hourly", now.Add(time.Hour)},
		{"daily", now.AddDate(0, 0, 1)},
		{"weekly", now.AddDate(0, 0, 7)},
		{"monthly", now.AddDate(0, 1, 0)},
		{"unknown", now.AddDate(0, 0, 1)}, // fallback to daily
	}

	for _, tc := range cases {
		t.Run(tc.interval, func(t *testing.T) {
			got := calculateNextRun(&ScanSchedule{Interval: tc.interval}, now)
			if !got.Equal(tc.want) {
				t.Errorf("calculateNextRun(%q) = %v, want %v", tc.interval, got, tc.want)
			}
		})
	}
}

func TestCalculateNextRunAnchored(t *testing.T) {
	utc := time.UTC
	// Buenos Aires is UTC-3 year round, so 09:00 local == 12:00 UTC.
	buenosAires, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	cases := []struct {
		name string
		sch  ScanSchedule
		from time.Time
		want time.Time
	}{
		{
			name: "daily later today",
			sch:  ScanSchedule{Interval: "daily", RunAt: "23:30", Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 5, 22, 23, 30, 0, 0, utc),
		},
		{
			name: "daily rolls to tomorrow once passed",
			sch:  ScanSchedule{Interval: "daily", RunAt: "09:00", Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 5, 23, 9, 0, 0, 0, utc),
		},
		{
			name: "daily exactly at the slot rolls forward",
			sch:  ScanSchedule{Interval: "daily", RunAt: "12:00", Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 5, 23, 12, 0, 0, 0, utc),
		},
		{
			name: "hourly uses only the minutes",
			sch:  ScanSchedule{Interval: "hourly", RunAt: "07:15", Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 5, 22, 12, 15, 0, 0, utc),
		},
		{
			name: "hourly rolls to the next hour",
			sch:  ScanSchedule{Interval: "hourly", RunAt: "07:15", Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 30, 0, 0, utc),
			want: time.Date(2026, 5, 22, 13, 15, 0, 0, utc),
		},
		{
			// 2026-05-22 is a Friday; RunDay 1 = Monday.
			name: "weekly picks the configured weekday",
			sch:  ScanSchedule{Interval: "weekly", RunAt: "03:00", RunDay: 1, Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 5, 25, 3, 0, 0, 0, utc),
		},
		{
			name: "weekly on today rolls a full week when the slot passed",
			sch:  ScanSchedule{Interval: "weekly", RunAt: "03:00", RunDay: int(time.Friday), Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 5, 29, 3, 0, 0, 0, utc),
		},
		{
			name: "weekly sunday is run_day zero",
			sch:  ScanSchedule{Interval: "weekly", RunAt: "03:00", RunDay: int(time.Sunday), Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 5, 24, 3, 0, 0, 0, utc),
		},
		{
			// Persisted schedules are not re-validated on load, so an
			// out-of-range weekday has to wrap instead of hanging the tick.
			name: "weekly wraps an out of range run_day",
			sch:  ScanSchedule{Interval: "weekly", RunAt: "03:00", RunDay: 8, Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 5, 25, 3, 0, 0, 0, utc), // 8 % 7 == Monday
		},
		{
			name: "monthly later this month",
			sch:  ScanSchedule{Interval: "monthly", RunAt: "02:00", RunDay: 28, Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 5, 28, 2, 0, 0, 0, utc),
		},
		{
			name: "monthly rolls to next month once passed",
			sch:  ScanSchedule{Interval: "monthly", RunAt: "02:00", RunDay: 10, Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 6, 10, 2, 0, 0, 0, utc),
		},
		{
			// 2026 is not a leap year: day 31 clamps to February 28.
			name: "monthly clamps to the last day of a short month",
			sch:  ScanSchedule{Interval: "monthly", RunAt: "02:00", RunDay: 31, Timezone: "UTC"},
			from: time.Date(2026, 2, 1, 12, 0, 0, 0, utc),
			want: time.Date(2026, 2, 28, 2, 0, 0, 0, utc),
		},
		{
			name: "timezone is honored",
			sch:  ScanSchedule{Interval: "daily", RunAt: "09:00", Timezone: "America/Argentina/Buenos_Aires"},
			from: time.Date(2026, 5, 22, 6, 0, 0, 0, utc),
			want: time.Date(2026, 5, 22, 9, 0, 0, 0, buenosAires),
		},
		{
			name: "unknown timezone falls back instead of failing",
			sch:  ScanSchedule{Interval: "daily", RunAt: "23:30", Timezone: "Mars/Olympus_Mons"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, time.Local),
			want: time.Date(2026, 5, 22, 23, 30, 0, 0, time.Local),
		},
		{
			name: "malformed run_at falls back to the relative interval",
			sch:  ScanSchedule{Interval: "daily", RunAt: "9am", Timezone: "UTC"},
			from: time.Date(2026, 5, 22, 12, 0, 0, 0, utc),
			want: time.Date(2026, 5, 23, 12, 0, 0, 0, utc),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := calculateNextRun(&tc.sch, tc.from)
			if !got.Equal(tc.want) {
				t.Errorf("calculateNextRun(%+v, %v) = %v, want %v", tc.sch, tc.from, got, tc.want)
			}
			if !got.After(tc.from) {
				t.Errorf("next run %v is not strictly after %v", got, tc.from)
			}
		})
	}
}

func TestNormalizeScheduleTiming(t *testing.T) {
	cases := []struct {
		name       string
		sch        ScanSchedule
		wantErr    bool
		wantRunDay int
	}{
		{name: "empty is valid", sch: ScanSchedule{Interval: "daily"}},
		{name: "valid time", sch: ScanSchedule{Interval: "daily", RunAt: "09:30"}},
		{name: "trims whitespace", sch: ScanSchedule{Interval: "daily", RunAt: "  09:30  "}},
		{name: "rejects 24h", sch: ScanSchedule{Interval: "daily", RunAt: "24:00"}, wantErr: true},
		{name: "rejects bad minutes", sch: ScanSchedule{Interval: "daily", RunAt: "09:60"}, wantErr: true},
		{name: "rejects free text", sch: ScanSchedule{Interval: "daily", RunAt: "9am"}, wantErr: true},
		{name: "rejects unknown timezone", sch: ScanSchedule{Interval: "daily", Timezone: "Mars/Olympus_Mons"}, wantErr: true},
		{name: "accepts known timezone", sch: ScanSchedule{Interval: "daily", Timezone: "America/Argentina/Buenos_Aires"}},
		{name: "weekly sunday", sch: ScanSchedule{Interval: "weekly", RunDay: 0}},
		{name: "weekly rejects out of range", sch: ScanSchedule{Interval: "weekly", RunDay: 7}, wantErr: true},
		{name: "monthly defaults to the first", sch: ScanSchedule{Interval: "monthly", RunDay: 0}, wantRunDay: 1},
		{name: "monthly rejects day 32", sch: ScanSchedule{Interval: "monthly", RunDay: 32}, wantErr: true},
		{name: "daily drops the day component", sch: ScanSchedule{Interval: "daily", RunDay: 4}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := normalizeScheduleTiming(&tc.sch)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil (schedule: %+v)", tc.sch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.sch.RunDay != tc.wantRunDay {
				t.Errorf("RunDay = %d, want %d", tc.sch.RunDay, tc.wantRunDay)
			}
			if strings.TrimSpace(tc.sch.RunAt) != tc.sch.RunAt {
				t.Errorf("RunAt was not trimmed: %q", tc.sch.RunAt)
			}
		})
	}
}

func TestSchedulesDiskIO(t *testing.T) {
	s := newTestServer(t, nil)

	sch := &ScanSchedule{
		ID:       "test-sch-1",
		Name:     "Test Daily Scan",
		Interval: "daily",
		Enabled:  true,
		Targets:  []string{"localhost"},
	}

	// Test saving
	err := s.saveScheduleToDisk(sch)
	if err != nil {
		t.Fatalf("saveScheduleToDisk failed: %v", err)
	}

	// Verify file is created
	filePath := filepath.Join(s.dataDir, "_schedules", sch.ID+".json")
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("schedule file not created: %v", err)
	}

	// Test loading
	// First clear memory map
	s.schedulesMu.Lock()
	s.schedules = make(map[string]*ScanSchedule)
	s.schedulesMu.Unlock()

	s.loadSchedulesFromDisk()

	s.schedulesMu.RLock()
	loaded, ok := s.schedules[sch.ID]
	s.schedulesMu.RUnlock()

	if !ok {
		t.Fatalf("schedule not found in memory after loading from disk")
	}
	if loaded.Name != sch.Name || loaded.Interval != sch.Interval {
		t.Errorf("loaded schedule mismatch: %+v", loaded)
	}

	// Test deleting
	err = s.deleteScheduleFromDisk(sch.ID)
	if err != nil {
		t.Fatalf("deleteScheduleFromDisk failed: %v", err)
	}

	// Verify file is gone
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("schedule file still exists after deletion")
	}
}

func TestCheckAndRunSchedules(t *testing.T) {
	s := newTestServer(t, nil)

	// Create an enabled schedule whose NextRun is in the past
	dueSch := &ScanSchedule{
		ID:       "due-1",
		Name:     "Due Daily Scan",
		Interval: "daily",
		Enabled:  true,
		NextRun:  time.Now().Add(-10 * time.Minute),
		Targets:  []string{"localhost"},
	}

	// Create an enabled schedule whose NextRun is in the future
	futureSch := &ScanSchedule{
		ID:       "future-1",
		Name:     "Future Daily Scan",
		Interval: "daily",
		Enabled:  true,
		NextRun:  time.Now().Add(10 * time.Minute),
		Targets:  []string{"localhost"},
	}

	// Create a disabled schedule whose NextRun is in the past
	disabledSch := &ScanSchedule{
		ID:       "disabled-1",
		Name:     "Disabled Daily Scan",
		Interval: "daily",
		Enabled:  false,
		NextRun:  time.Now().Add(-10 * time.Minute),
		Targets:  []string{"localhost"},
	}

	s.schedulesMu.Lock()
	s.schedules[dueSch.ID] = dueSch
	s.schedules[futureSch.ID] = futureSch
	s.schedules[disabledSch.ID] = disabledSch
	s.schedulesMu.Unlock()

	// Clear any historical instances loaded on startup
	s.instancesMu.Lock()
	s.instances = make(map[string]*ScanInstance)
	s.instancesMu.Unlock()

	// Call checkAndRunSchedules
	s.checkAndRunSchedules()

	s.schedulesMu.RLock()
	// Check dueSch: should have run, so LastRun should be updated and NextRun pushed forward
	if dueSch.LastRun.IsZero() {
		t.Errorf("due schedule did not run (LastRun is zero)")
	}
	if !dueSch.NextRun.After(time.Now()) {
		t.Errorf("due schedule NextRun not updated correctly: %v", dueSch.NextRun)
	}

	// Check futureSch: should not have run, LastRun should be zero, NextRun remains unchanged
	if !futureSch.LastRun.IsZero() {
		t.Errorf("future schedule ran unexpectedly")
	}

	// Check disabledSch: should not have run, LastRun should be zero
	if !disabledSch.LastRun.IsZero() {
		t.Errorf("disabled schedule ran unexpectedly")
	}
	s.schedulesMu.RUnlock()

	// Wait briefly to let the goroutine register the scan instance
	time.Sleep(100 * time.Millisecond)

	s.instancesMu.RLock()
	defer s.instancesMu.RUnlock()

	// Check that we have exactly one instance registered (from the due schedule)
	if len(s.instances) != 1 {
		t.Errorf("expected 1 registered scan instance, got %d", len(s.instances))
	}

	// The schedule-launched scan keeps writing records into the TempDir-backed
	// data directory from its own goroutine. t.TempDir's RemoveAll cleanup races
	// with those writes ("unlinkat ... directory not empty" flakes in CI), so
	// drain the instance to a terminal state before cleanup is allowed to run.
	// Status is guarded by the per-instance mutex (not instancesMu), so each
	// read takes instance.mu — reading it under instancesMu alone trips the race
	// detector against runMultiScan's terminal write.
	s.instancesMu.RUnlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.instancesMu.RLock()
		insts := make([]*ScanInstance, 0, len(s.instances))
		for _, inst := range s.instances {
			insts = append(insts, inst)
		}
		s.instancesMu.RUnlock()
		terminal := len(insts) > 0
		for _, inst := range insts {
			inst.mu.Lock()
			st := inst.Status
			inst.mu.Unlock()
			if st == "running" || st == "queued" || st == "" {
				terminal = false
				break
			}
		}
		if terminal {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.instancesMu.RLock()
}

// ── scheduler→run closure (REDESIGN §3.3, R2) ────────────────────────────────
//
// The legacy scheduler used to dispatch due schedules straight into
// runMultiScan with no platform linkage. These tests pin the closed loop:
// schedule targets are validated against the linked project's scope, the run
// mounts onto the imported schedule task with Trigger "schedule", the empty
// default-import scope keeps the schedule's own targets as the authorization
// source, and a nil platform store keeps the legacy direct dispatch.

// dueScheduleFixture returns an enabled schedule whose NextRun already passed.
func dueScheduleFixture(id string, targets ...string) *ScanSchedule {
	return &ScanSchedule{
		ID:       id,
		Name:     "Scheduled " + id,
		Interval: "daily",
		Enabled:  true,
		NextRun:  time.Now().Add(-time.Minute),
		Targets:  targets,
	}
}

// importScheduleFixture mirrors the startup importer: convert the schedule
// into a task keyed by tasks.context.legacy_schedule_id under projectID.
func importScheduleFixture(t *testing.T, store platform.Repository, projectID string, sch *ScanSchedule) *platform.Task {
	t.Helper()
	task, _, err := store.ImportScheduleTask(context.Background(), projectID, legacyScheduleToTaskInput(sch), sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// addScheduleFixture inserts the schedule into the server's in-memory map.
func addScheduleFixture(s *Server, sch *ScanSchedule) {
	s.schedulesMu.Lock()
	s.schedules[sch.ID] = sch
	s.schedulesMu.Unlock()
}

// clearInstancesFixture empties the instances the constructor rebuilt from
// disk, so tick assertions start from a known-zero live set.
func clearInstancesFixture(s *Server) {
	s.instancesMu.Lock()
	s.instances = make(map[string]*ScanInstance)
	s.instancesMu.Unlock()
}

// drainInstancesForTest waits until every registered instance reached a
// terminal state (bounded 3s) so TempDir cleanup does not race the detached
// dispatch goroutine's writes. Status is guarded by the per-instance mutex,
// not instancesMu (same discipline as TestCheckAndRunSchedules).
func drainInstancesForTest(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.instancesMu.RLock()
		insts := make([]*ScanInstance, 0, len(s.instances))
		for _, inst := range s.instances {
			insts = append(insts, inst)
		}
		s.instancesMu.RUnlock()
		terminal := len(insts) > 0
		for _, inst := range insts {
			inst.mu.Lock()
			st := inst.Status
			inst.mu.Unlock()
			if st == "running" || st == "queued" || st == "" {
				terminal = false
				break
			}
		}
		if terminal {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// newSchedulerPlatformServer wires a platform store rooted at a best-effort
// data dir (the tick dispatches scans whose goroutines keep writing after the
// test returns).
func newSchedulerPlatformServer(t *testing.T) (*Server, platform.Repository) {
	t.Helper()
	s := newPlatformTestServer(t)
	bestEffortDataDir(t, s)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	return s, store
}

// scopedDefaultProject gives the implicit default project a non-empty scope —
// the shape an operator lands in after PATCHing the default project, and the
// state in which schedule targets become subject to scope validation.
func scopedDefaultProject(t *testing.T, store platform.Repository, targets ...string) *platform.Project {
	t.Helper()
	project, err := store.EnsureDefaultProject(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	scope := platform.Scope{Targets: targets}
	if _, err := store.UpdateProject(context.Background(), project.ID, platform.ProjectUpdate{Scope: &scope}); err != nil {
		t.Fatal(err)
	}
	return project
}

// TestSchedulerTickMountsInScopeScheduleRun (case a): a due schedule whose
// targets sit inside the linked project's scope dispatches and mounts exactly
// one run with Trigger "schedule". The mount is synchronous (CreateRun before
// the dispatch goroutine), so the run assertions need no sleep.
func TestSchedulerTickMountsInScopeScheduleRun(t *testing.T) {
	s, store := newSchedulerPlatformServer(t)
	ctx := context.Background()

	project := scopedDefaultProject(t, store, "acme.example")
	sch := dueScheduleFixture("tick-in-scope", "https://app.acme.example")
	task := importScheduleFixture(t, store, project.ID, sch)
	addScheduleFixture(s, sch)
	clearInstancesFixture(s)

	s.checkAndRunSchedules()

	runs, total, err := store.ListRuns(ctx, task.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("mounted runs = %d, want 1", total)
	}
	if runs[0].Trigger != "schedule" {
		t.Errorf("mount trigger = %q, want schedule", runs[0].Trigger)
	}
	if runs[0].InstanceID == "" {
		t.Error("mount instance_id is empty")
	}
	if runs[0].TaskID != task.ID {
		t.Errorf("mount task = %s, want %s", runs[0].TaskID, task.ID)
	}

	s.schedulesMu.RLock()
	lastRun, nextRun := sch.LastRun, sch.NextRun
	s.schedulesMu.RUnlock()
	if lastRun.IsZero() {
		t.Error("due schedule did not advance LastRun")
	}
	if !nextRun.After(time.Now()) {
		t.Errorf("NextRun = %v, want pushed forward", nextRun)
	}

	drainInstancesForTest(t, s)
}

// TestSchedulerTickRejectsOutOfScopeScheduleStillAdvances (case b): a due
// schedule targeting outside the project scope dispatches nothing and mounts
// nothing, but LastRun/NextRun still advance — the 30s tick must not
// retry-storm a permanently out-of-scope configuration.
func TestSchedulerTickRejectsOutOfScopeScheduleStillAdvances(t *testing.T) {
	s, store := newSchedulerPlatformServer(t)
	ctx := context.Background()

	project := scopedDefaultProject(t, store, "acme.example")
	sch := dueScheduleFixture("tick-out-of-scope", "https://evil.example")
	task := importScheduleFixture(t, store, project.ID, sch)
	addScheduleFixture(s, sch)
	clearInstancesFixture(s)

	s.checkAndRunSchedules()

	_, total, err := store.ListRuns(ctx, task.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("out-of-scope schedule mounted %d run(s)", total)
	}
	s.instancesMu.RLock()
	live := len(s.instances)
	s.instancesMu.RUnlock()
	if live != 0 {
		t.Fatalf("out-of-scope schedule dispatched %d live instance(s)", live)
	}

	s.schedulesMu.RLock()
	lastRun, nextRun := sch.LastRun, sch.NextRun
	s.schedulesMu.RUnlock()
	if lastRun.IsZero() {
		t.Error("refused schedule did not advance LastRun (retry storm guard)")
	}
	if !nextRun.After(time.Now()) {
		t.Errorf("refused schedule NextRun = %v, want advanced", nextRun)
	}
}

// TestSchedulerTickEmptyScopeAllowsScheduleTargets (case c): the default
// import state is an empty-scope project — there the schedule's own targets
// remain the authorization source (operator's explicit pre-platform
// configuration) and the run still mounts.
func TestSchedulerTickEmptyScopeAllowsScheduleTargets(t *testing.T) {
	s, store := newSchedulerPlatformServer(t)
	ctx := context.Background()

	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sch := dueScheduleFixture("tick-empty-scope", "https://anywhere.example")
	task := importScheduleFixture(t, store, project.ID, sch)
	addScheduleFixture(s, sch)
	clearInstancesFixture(s)

	s.checkAndRunSchedules()

	runs, total, err := store.ListRuns(ctx, task.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("empty-scope schedule mounted %d runs, want 1 (schedule targets are the authorization source)", total)
	}
	if runs[0].Trigger != "schedule" {
		t.Errorf("mount trigger = %q, want schedule", runs[0].Trigger)
	}

	drainInstancesForTest(t, s)
}

// TestSchedulerTickWithoutPlatformStoreKeepsDirectDispatch (case d): no
// platform store → no validation, no mount, legacy direct dispatch unchanged
// (charter §6.6 fail-open).
func TestSchedulerTickWithoutPlatformStoreKeepsDirectDispatch(t *testing.T) {
	s := newTestServer(t, nil)
	bestEffortDataDir(t, s)
	if s.currentPlatformStore() != nil {
		t.Fatal("precondition: platform store must be nil")
	}

	sch := dueScheduleFixture("tick-no-store", "https://nostore.example")
	addScheduleFixture(s, sch)
	clearInstancesFixture(s)

	s.checkAndRunSchedules()

	drainInstancesForTest(t, s)
	s.instancesMu.RLock()
	live := len(s.instances)
	s.instancesMu.RUnlock()
	if live != 1 {
		t.Fatalf("live instances = %d, want 1 (legacy direct dispatch preserved)", live)
	}
}

// TestScheduleTriggerEndpointScopeGate (case e): the manual trigger endpoint
// enforces the same closure as the tick — in-scope triggers return 200 with
// the instance id and mount a "schedule" run; out-of-scope triggers answer
// 400 and leave zero mounts and zero live instances.
func TestScheduleTriggerEndpointScopeGate(t *testing.T) {
	s, store := newSchedulerPlatformServer(t)
	ctx := context.Background()

	project := scopedDefaultProject(t, store, "acme.example")

	// Out-of-scope first: no goroutine can have spawned yet, so the zero
	// live-instance assertion is deterministic.
	outSch := dueScheduleFixture("trigger-out-of-scope", "https://evil.example")
	outTask := importScheduleFixture(t, store, project.ID, outSch)
	addScheduleFixture(s, outSch)
	clearInstancesFixture(s)

	rr := doJSON(t, s, http.MethodPost, "/api/schedules/"+outSch.ID+"/trigger", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("out-of-scope trigger code = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	_, total, err := store.ListRuns(ctx, outTask.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("out-of-scope trigger mounted %d run(s)", total)
	}
	s.instancesMu.RLock()
	live := len(s.instances)
	s.instancesMu.RUnlock()
	if live != 0 {
		t.Fatalf("out-of-scope trigger dispatched %d live instance(s)", live)
	}

	// In-scope control: 200 + instance id + a mounted "schedule" run.
	inSch := dueScheduleFixture("trigger-in-scope", "https://app.acme.example")
	inTask := importScheduleFixture(t, store, project.ID, inSch)
	addScheduleFixture(s, inSch)

	rr = doJSON(t, s, http.MethodPost, "/api/schedules/"+inSch.ID+"/trigger", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("in-scope trigger code = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeBody(t, rr)
	instanceID, _ := body["instance_id"].(string)
	if instanceID == "" {
		t.Fatalf("trigger ack missing instance_id: %v", body)
	}
	runs, total, err := store.ListRuns(ctx, inTask.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("in-scope trigger mounted %d runs, want 1", total)
	}
	if runs[0].Trigger != "schedule" {
		t.Errorf("mount trigger = %q, want schedule", runs[0].Trigger)
	}
	if runs[0].InstanceID != instanceID {
		t.Errorf("mount instance = %q, want %q", runs[0].InstanceID, instanceID)
	}
	s.schedulesMu.RLock()
	lastRun := inSch.LastRun
	s.schedulesMu.RUnlock()
	if lastRun.IsZero() {
		t.Error("trigger did not advance LastRun")
	}

	drainInstancesForTest(t, s)
}

// ── review closure: runtime-created schedules & broken linkages ──────────────
//
// The startup importer is the only automatic source of schedule tasks, so
// without import-on-create a runtime POST /api/schedules would have no task
// for the scheduler-entry check to resolve. These tests pin the closed loop:
// create/update re-links the task immediately, and a configured store with a
// missing task or a transient store failure skips the cycle instead of
// silently dispatching whatever the schedule says.

// TestSchedulerTickMountsRuntimeCreatedSchedule: POST /api/schedules links
// the task synchronously (no restart needed), so the first due tick mounts
// the run; a PUT update re-links idempotently without duplicating the task.
func TestSchedulerTickMountsRuntimeCreatedSchedule(t *testing.T) {
	s, store := newSchedulerPlatformServer(t)
	ctx := context.Background()
	project := scopedDefaultProject(t, store, "acme.example")

	rr := doJSON(t, s, http.MethodPost, "/api/schedules",
		`{"name":"Runtime created","interval":"daily","targets":["https://app.acme.example"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /api/schedules code = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var created ScanSchedule
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create response missing id: %v; body=%s", err, rr.Body.String())
	}

	// The linkage exists immediately — no restart, no importer pass.
	task, err := store.TaskByLegacyScheduleID(ctx, project.ID, created.ID)
	if err != nil {
		t.Fatalf("import-on-create did not link the schedule task: %v", err)
	}

	// PUT update re-links (idempotent): still exactly one schedule task.
	rr = doJSON(t, s, http.MethodPut, "/api/schedules/"+created.ID,
		`{"name":"Runtime created v2","interval":"daily","enabled":true,"targets":["https://app.acme.example"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT /api/schedules code = %d; body=%s", rr.Code, rr.Body.String())
	}
	tasks, err := store.ListTasks(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("PUT relink duplicated the schedule task: %+v (want only %s)", tasks, task.ID)
	}

	// First due tick mounts through the fresh linkage.
	s.schedulesMu.Lock()
	s.schedules[created.ID].NextRun = time.Now().Add(-time.Minute)
	s.schedulesMu.Unlock()
	clearInstancesFixture(s)
	s.checkAndRunSchedules()

	runs, total, err := store.ListRuns(ctx, task.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("runtime-created schedule mounted %d runs, want 1", total)
	}
	if runs[0].Trigger != "schedule" {
		t.Errorf("mount trigger = %q, want schedule", runs[0].Trigger)
	}

	drainInstancesForTest(t, s)
}

// TestSchedulerTickSkipsWhenScheduleTaskMissing: with a store configured, a
// schedule whose task disappeared is an anomaly — the tick skips the cycle
// (zero mounts, zero dispatches) instead of silently authorizing the raw
// schedule targets, while LastRun/NextRun still advance (no retry storm).
func TestSchedulerTickSkipsWhenScheduleTaskMissing(t *testing.T) {
	s, store := newSchedulerPlatformServer(t)
	ctx := context.Background()
	project := scopedDefaultProject(t, store, "acme.example")

	rr := doJSON(t, s, http.MethodPost, "/api/schedules",
		`{"name":"Unlinked","interval":"daily","targets":["https://app.acme.example"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /api/schedules code = %d; body=%s", rr.Code, rr.Body.String())
	}
	var created ScanSchedule
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create response missing id: %v; body=%s", err, rr.Body.String())
	}
	task, err := store.TaskByLegacyScheduleID(ctx, project.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Break the linkage the way an operator-side delete would.
	if err := store.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	s.schedulesMu.Lock()
	s.schedules[created.ID].NextRun = time.Now().Add(-time.Minute)
	s.schedulesMu.Unlock()
	clearInstancesFixture(s)

	s.checkAndRunSchedules()

	mounts, err := store.ListRunsByProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 0 {
		t.Fatalf("missing-task schedule mounted %d run(s), want 0", len(mounts))
	}
	s.instancesMu.RLock()
	live := len(s.instances)
	s.instancesMu.RUnlock()
	if live != 0 {
		t.Fatalf("missing-task schedule dispatched %d live instance(s)", live)
	}
	s.schedulesMu.RLock()
	lastRun, nextRun := s.schedules[created.ID].LastRun, s.schedules[created.ID].NextRun
	s.schedulesMu.RUnlock()
	if lastRun.IsZero() {
		t.Error("skipped schedule did not advance LastRun")
	}
	if !nextRun.After(time.Now()) {
		t.Errorf("skipped schedule NextRun = %v, want advanced (no retry storm)", nextRun)
	}
}

// erroringPlatformStore fails exactly the lookups the scheduler-entry check
// makes; every other interface call panics on the nil-embedded Repository,
// so any unexpected reach into the fake fails loudly instead of passing.
type erroringPlatformStore struct {
	platform.Repository
	failErr error
}

func (f *erroringPlatformStore) EnsureDefaultProject(context.Context) (*platform.Project, error) {
	return nil, f.failErr
}

// ListApprovals satisfies the SetPlatformStore startup reconciliation probe.
func (f *erroringPlatformStore) ListApprovals(context.Context, platform.ApprovalFilter) ([]platform.Approval, error) {
	return []platform.Approval{}, nil
}

// TestSchedulerTickSkipsOnTransientStoreError: a store failure inside the
// authorization path skips the cycle (fail-closed) — it never degrades into
// dispatching without validation.
func TestSchedulerTickSkipsOnTransientStoreError(t *testing.T) {
	s := newTestServer(t, nil)
	bestEffortDataDir(t, s)
	s.SetPlatformStore(&erroringPlatformStore{failErr: errors.New("injected store failure")})

	sch := dueScheduleFixture("tick-store-error", "https://anywhere.example")
	addScheduleFixture(s, sch)
	clearInstancesFixture(s)

	s.checkAndRunSchedules()

	s.instancesMu.RLock()
	live := len(s.instances)
	s.instancesMu.RUnlock()
	if live != 0 {
		t.Fatalf("store failure dispatched %d live instance(s), want 0 (fail-closed skip)", live)
	}
	s.schedulesMu.RLock()
	lastRun, nextRun := sch.LastRun, sch.NextRun
	s.schedulesMu.RUnlock()
	if lastRun.IsZero() {
		t.Error("store-error skip did not advance LastRun")
	}
	if !nextRun.After(time.Now()) {
		t.Errorf("store-error skip NextRun = %v, want advanced", nextRun)
	}
}
