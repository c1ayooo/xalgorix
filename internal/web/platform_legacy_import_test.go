package web

// Tests for the R1 legacy importer (platform_legacy_import.go): existing
// scan.json records mount under the default project's umbrella task, schedules
// convert to tasks, everything is idempotent, and the on-disk fixtures are
// never modified (REDESIGN §8: 只读保留原目录).

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// snapshotTreeHashes maps every regular file under root (relative path) to
// its content + mtime so the test can prove the importer left the originals
// untouched. The platform's own snapshot dir (_platform) is excluded: it is
// the write target, not a fixture.
func snapshotTreeHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	platformDir := filepath.Join(root, "_platform")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == platformDir {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(strings.TrimPrefix(path, root), string(filepath.Separator))] =
			info.ModTime().Format(time.RFC3339Nano) + "|" + string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPlatformLegacyImportScansAndSchedules(t *testing.T) {
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	ctx := context.Background()

	// Fixtures: two legacy scans (one without an instance id) and one
	// schedule — laid out exactly like the live data dir.
	writeScanFixture(t, s.dataDir, "acme.example", "2026-05-01", ScanRecord{
		ID: "legacy-acme-1", Target: "https://acme.example",
		StartedAt: "2026-05-01T10:00:00Z", FinishedAt: "2026-05-01T11:00:00Z",
		Status: "finished", ScanMode: "wildcard", TotalTokens: 500,
		Vulns: []VulnSummary{{ID: "v-a", Title: "Legacy finding A", Severity: "high"}},
	})
	writeScanFixture(t, s.dataDir, "beta.example", "2026-05-02", ScanRecord{
		ID: "legacy-beta-1", InstanceID: "legacy-beta-inst", Target: "https://beta.example",
		StartedAt: "2026-05-02T10:00:00Z", Status: "stopped", ScanMode: "single",
	})
	schedulesDir := filepath.Join(s.dataDir, "_schedules")
	if err := os.MkdirAll(schedulesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	scheduleFixture := ScanSchedule{
		ID: "sched-1", Name: "Nightly perimeter sweep", Interval: "weekly", RunAt: "02:30",
		Timezone: "Europe/Berlin", Enabled: true, RunDay: 3,
		Targets:        []string{"perimeter.example"},
		Instruction:    "sweep the perimeter",
		ScanMode:       "wildcard",
		SeverityFilter: []string{"critical"},
		CompanyName:    "Acme Corp", LogoPath: "/uploads/logos/acme.png",
		DiscordWebhook: "https://discord.com/api/webhooks/legacy", Model: "openai/gpt-5.6",
	}
	schedBytes, err := json.MarshalIndent(scheduleFixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	schedulePath := filepath.Join(schedulesDir, "sched-1.json")
	if err := os.WriteFile(schedulePath, schedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	// Reload schedules from the swapped data dir (NewServer loaded the empty
	// pre-swap dir).
	s.loadSchedulesFromDisk()

	beforeScans := snapshotTreeHashes(t, s.dataDir)

	s.importLegacyPlatformRecords()

	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// ① Umbrella task exists exactly once, runs are mounted with the right keys.
	if _, err := store.EnsureImportTask(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListRunsByProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("mounted runs = %d, want 2", len(runs))
	}
	byLegacy := map[string]platform.Run{}
	for _, r := range runs {
		byLegacy[r.LegacyScanID] = r
	}
	runA, ok := byLegacy["legacy-acme-1"]
	if !ok {
		t.Fatalf("scan A not mounted: %+v", runs)
	}
	if runA.InstanceID != "legacy-acme-1" {
		t.Errorf("scan A instance fallback = %q, want record ID", runA.InstanceID)
	}
	if runA.Mode != "wildcard" {
		t.Errorf("scan A mode = %q", runA.Mode)
	}
	runB, ok := byLegacy["legacy-beta-1"]
	if !ok {
		t.Fatalf("scan B not mounted: %+v", runs)
	}
	if runB.InstanceID != "legacy-beta-inst" {
		t.Errorf("scan B instance = %q, want record's instance id", runB.InstanceID)
	}

	// ② Schedule → task conversion with legacy context hints.
	tasks, err := store.ListTasks(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var schedTask *platform.Task
	for i := range tasks {
		if tasks[i].Context["legacy_schedule_id"] == "sched-1" {
			schedTask = &tasks[i]
		}
	}
	if schedTask == nil {
		t.Fatalf("schedule task missing; tasks = %+v", tasks)
	}
	if schedTask.Name != "Nightly perimeter sweep" {
		t.Errorf("schedule task name = %q", schedTask.Name)
	}
	if schedTask.Instruction != "sweep the perimeter" {
		t.Errorf("schedule task instruction = %q", schedTask.Instruction)
	}
	if schedTask.Schedule == nil || schedTask.Schedule.Interval != "weekly" ||
		schedTask.Schedule.Time != "02:30" || schedTask.Schedule.Timezone != "Europe/Berlin" {
		t.Errorf("schedule attribute = %+v", schedTask.Schedule)
	}
	if schedTask.PhasePolicy == nil || schedTask.PhasePolicy.ScanMode != "wildcard" ||
		len(schedTask.PhasePolicy.SeverityFilter) != 1 {
		t.Errorf("phase policy = %+v", schedTask.PhasePolicy)
	}
	// RunDay has no contract slot — it lands in context as a legacy hint.
	if schedTask.Context["legacy_run_day"] != float64(3) && schedTask.Context["legacy_run_day"] != 3 {
		t.Errorf("legacy_run_day = %v", schedTask.Context["legacy_run_day"])
	}
	if schedTask.Context["legacy_model"] != "openai/gpt-5.6" {
		t.Errorf("legacy_model = %v", schedTask.Context["legacy_model"])
	}

	// ③ Non-destructive: original files byte-identical with identical mtimes.
	afterScans := snapshotTreeHashes(t, s.dataDir)
	if len(afterScans) != len(beforeScans) {
		var keysA, keysB []string
		for k := range beforeScans {
			keysA = append(keysA, k)
		}
		for k := range afterScans {
			keysB = append(keysB, k)
		}
		sort.Strings(keysA)
		sort.Strings(keysB)
		t.Fatalf("file set changed by import:\nbefore=%v\nafter=%v", keysA, keysB)
	}
	for path, want := range beforeScans {
		if afterScans[path] != want {
			t.Errorf("file %s modified by import (content or mtime)", path)
		}
	}

	// ④ Idempotence: a second import adds nothing.
	s.importLegacyPlatformRecords()
	runs, err = store.ListRunsByProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("second import re-mounted runs: %d, want 2", len(runs))
	}
	tasks, err = store.ListTasks(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	schedTasks := 0
	for i := range tasks {
		if tasks[i].Context["legacy_schedule_id"] == "sched-1" {
			schedTasks++
		}
	}
	if schedTasks != 1 {
		t.Fatalf("second import duplicated the schedule task (%d)", schedTasks)
	}
}

func TestPlatformLegacyImportWithNoStoreIsNoop(t *testing.T) {
	s := newTestServer(t, nil) // no platform store wired
	s.importLegacyPlatformRecords()
	if s.currentPlatformStore() != nil {
		t.Fatal("test setup broken: store unexpectedly present")
	}
}

func TestPlatformLegacyImportScanWithoutIDSkipped(t *testing.T) {
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	// A scan.json without an ID cannot be keyed — skipped, not fatal.
	writeScanFixture(t, s.dataDir, "idless.example", "2026-05-03", ScanRecord{
		Target: "https://idless.example", StartedAt: "2026-05-03T10:00:00Z", Status: "finished",
	})
	s.importLegacyPlatformRecords()
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListRunsByProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("id-less scan mounted: %+v", runs)
	}
}
