package platform

import (
	"context"
	"errors"
	"os"
	"testing"
)

func newTestFileStore(t *testing.T) *FileStore {
	t.Helper()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestFileStoreEnsureDefaultProjectIdempotent(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()

	first, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != DefaultProjectID {
		t.Errorf("default project id = %q, want fixed %q", first.ID, DefaultProjectID)
	}
	if first.Slug != DefaultProjectSlug {
		t.Errorf("default project slug = %q, want %q", first.Slug, DefaultProjectSlug)
	}
	second, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Errorf("EnsureDefaultProject created a second project (%q != %q)", second.ID, first.ID)
	}
	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("ListProjects returned %d projects, want 1", len(projects))
	}
}

func TestFileStoreProjectCRUDAndArchiveFilter(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()

	// The implicit default project exists from the start in file mode.
	if _, err := store.EnsureDefaultProject(ctx); err != nil {
		t.Fatal(err)
	}

	created, err := store.CreateProject(ctx, "Alpha Team", Scope{Targets: []string{"acme.example"}, AllowPorts: []int{443}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Slug != "alpha-team" {
		t.Errorf("slug = %q, want %q", created.Slug, "alpha-team")
	}
	if created.Scope.Targets[0] != "acme.example" || created.Scope.AllowPorts[0] != 443 {
		t.Errorf("scope round-trip mismatch: %+v", created.Scope)
	}

	got, err := store.GetProject(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Alpha Team" {
		t.Errorf("name = %q", got.Name)
	}

	// Slug conflict gets the -2 suffix (mock rule).
	dup, err := store.CreateProject(ctx, "Alpha Team!", Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if dup.Slug != "alpha-team-2" {
		t.Errorf("conflict slug = %q, want %q", dup.Slug, "alpha-team-2")
	}

	// Update: rename + archive.
	newName := "Alpha Renamed"
	archived := true
	if _, err := store.UpdateProject(ctx, created.ID, ProjectUpdate{Name: &newName, Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.ID == created.ID {
			t.Errorf("archived project %s still listed", p.ID)
		}
	}
	// Point lookup still returns the archived row (with the timestamp set).
	archivedRow, err := store.GetProject(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archivedRow.ArchivedAt == nil {
		t.Error("archived_at not set after archiving")
	}

	// Unarchive restores visibility.
	unarchived := false
	if _, err := store.UpdateProject(ctx, created.ID, ProjectUpdate{Archived: &unarchived}); err != nil {
		t.Fatal(err)
	}
	projects, err = store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range projects {
		if p.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Error("unarchived project missing from list")
	}

	// Delete works for a normal project; default is protected.
	if err := store.DeleteProject(ctx, dup.ID); err != nil {
		t.Fatalf("delete normal project: %v", err)
	}
	if _, err := store.GetProject(ctx, dup.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted project lookup = %v, want ErrNotFound", err)
	}
	if err := store.DeleteProject(ctx, DefaultProjectID); !errors.Is(err, ErrConflict) {
		t.Errorf("delete default project = %v, want ErrConflict", err)
	}
	if _, err := store.CreateProject(ctx, "   ", Scope{}); !errors.Is(err, ErrValidation) {
		t.Errorf("empty-name create = %v, want ErrValidation", err)
	}
}

func TestFileStoreTaskCRUDSlugConflictsAndArchive(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Tasks Project", Scope{Targets: []string{"tasks.example"}})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.CreateTask(ctx, project.ID, TaskInput{
		Name:        "Perimeter review",
		Goal:        "Assess the perimeter",
		Instruction: "Focus on the edge",
		PhasePolicy: &PhasePolicy{ScanMode: "wildcard", SeverityFilter: []string{"critical"}, Phases: []int{1, 2}},
		Schedule:    &TaskSchedule{Interval: "daily", Time: "09:30", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Context["slug"] != "perimeter-review" {
		t.Errorf("context slug = %v, want perimeter-review", first.Context["slug"])
	}
	second, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "Perimeter review (weekly)"})
	if err != nil {
		t.Fatal(err)
	}
	// "perimeter-review-weekly" is derived then conflicts are impossible here,
	// but the same-name slug must get the -2 suffix.
	third, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "Perimeter review"})
	if err != nil {
		t.Fatal(err)
	}
	if third.Context["slug"] != "perimeter-review-2" {
		t.Errorf("task slug conflict = %v, want perimeter-review-2", third.Context["slug"])
	}
	_ = second

	tasks, err := store.ListTasks(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("ListTasks = %d tasks, want 3", len(tasks))
	}
	if tasks[0].PhasePolicy == nil || tasks[0].PhasePolicy.ScanMode != "wildcard" {
		t.Errorf("phase policy lost on create: %+v", tasks[0].PhasePolicy)
	}
	if tasks[0].Schedule == nil || tasks[0].Schedule.Time != "09:30" {
		t.Errorf("schedule lost on create: %+v", tasks[0].Schedule)
	}

	// Archive filters the list; point lookup keeps the row.
	arch := true
	if _, err := store.UpdateTask(ctx, first.ID, TaskUpdate{Archived: &arch}); err != nil {
		t.Fatal(err)
	}
	tasks, err = store.ListTasks(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("archived task still listed (%d tasks)", len(tasks))
	}

	// Partial update: policy replacement, goal change.
	updated, err := store.UpdateTask(ctx, second.ID, TaskUpdate{
		Goal:        strPtr("Updated goal"),
		PhasePolicy: &PhasePolicy{ScanMode: "single"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Goal != "Updated goal" {
		t.Errorf("goal = %q", updated.Goal)
	}
	if updated.PhasePolicy == nil || updated.PhasePolicy.ScanMode != "single" {
		t.Errorf("policy update = %+v", updated.PhasePolicy)
	}
	if updated.UpdatedAt == nil {
		t.Error("updated_at not stamped")
	}

	// Delete cascades runs.
	if _, err := store.CreateRun(ctx, third.ID, RunMount{InstanceID: "taskrun-1", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTask(ctx, third.ID); err != nil {
		t.Fatal(err)
	}
	runs, total, err := store.ListRuns(ctx, third.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(runs) != 0 {
		t.Errorf("runs survived task delete: %d/%d", len(runs), total)
	}

	// Creating tasks under a missing project fails.
	if _, err := store.CreateTask(ctx, "no-such-project", TaskInput{Name: "X"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("create task on missing project = %v, want ErrNotFound", err)
	}
}

func strPtr(v string) *string { return &v }

func TestFileStoreRunMountsPaginationAndConflicts(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()
	project, _ := store.CreateProject(ctx, "Runs Project", Scope{})
	task, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "Mounts"})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.CreateRun(ctx, task.ID, RunMount{InstanceID: "inst-1", Mode: "single", Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, RunMount{InstanceID: "inst-1"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate instance mount = %v, want ErrConflict", err)
	}
	if _, err := store.CreateRun(ctx, task.ID, RunMount{InstanceID: ""}); !errors.Is(err, ErrValidation) {
		t.Errorf("empty instance mount = %v, want ErrValidation", err)
	}
	for _, id := range []string{"inst-2", "inst-3"} {
		if _, err := store.CreateRun(ctx, task.ID, RunMount{InstanceID: id, Mode: "wildcard"}); err != nil {
			t.Fatal(err)
		}
	}

	runs, total, err := store.ListRuns(ctx, task.ID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(runs) != 2 {
		t.Fatalf("page size = %d, want 2", len(runs))
	}
	if runs[0].InstanceID != "inst-3" {
		t.Errorf("newest-first order broken: first page %v", runInstanceIDs(runs))
	}
	page2, _, err := store.ListRuns(ctx, task.ID, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].InstanceID != "inst-1" {
		t.Errorf("second page = %v, want [inst-1]", runInstanceIDs(page2))
	}
	if first.ID == "" {
		t.Error("run id not generated")
	}

	byInstance, err := store.RunsByInstanceID(ctx, "inst-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(byInstance) != 1 || byInstance[0].TaskID != task.ID {
		t.Errorf("RunsByInstanceID = %+v", byInstance)
	}

	other, _ := store.CreateProject(ctx, "Other Runs", Scope{})
	otherTask, _ := store.CreateTask(ctx, other.ID, TaskInput{Name: "Island"})
	_, err = store.CreateRun(ctx, otherTask.ID, RunMount{InstanceID: "inst-9", Mode: "single"})
	if err != nil {
		t.Fatal(err)
	}

	projectRuns, err := store.ListRunsByProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projectRuns) != 3 {
		t.Errorf("ListRunsByProject = %d, want 3 (row-level isolation)", len(projectRuns))
	}
	ids, err := store.InstanceIDsForProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Errorf("InstanceIDsForProject = %v, want 3 ids", ids)
	}
}

func runInstanceIDs(runs []Run) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.InstanceID)
	}
	return out
}

func TestFileStoreLegacyImportWritesIdempotent(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnsureImportTask(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Name != "导入扫描" {
		t.Errorf("umbrella task name = %q", task.Name)
	}
	again, err := store.EnsureImportTask(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != task.ID {
		t.Errorf("EnsureImportTask created a duplicate task (%q vs %q)", again.ID, task.ID)
	}

	run, created, err := store.ImportLegacyRun(ctx, task.ID, RunMount{
		LegacyScanID: "legacy-1", InstanceID: "legacy-1", Mode: "wildcard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("first import must report created=true")
	}
	replay, created2, err := store.ImportLegacyRun(ctx, task.ID, RunMount{
		LegacyScanID: "legacy-1", InstanceID: "legacy-1", Mode: "wildcard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created2 || replay.ID != run.ID {
		t.Errorf("replay created=%v run=%v; want created=false with original %v", created2, replay.ID, run.ID)
	}

	ids, err := store.LegacyScanIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ids["legacy-1"]; !ok {
		t.Errorf("LegacyScanIDs missing legacy-1: %v", ids)
	}

	// Schedule conversion is idempotent by legacy_schedule_id.
	in := TaskInput{Name: "Nightly perimeter", Instruction: "check it", Schedule: &TaskSchedule{Interval: "daily"}}
	converted, createdS, err := store.ImportScheduleTask(ctx, project.ID, in, "sched-1")
	if err != nil {
		t.Fatal(err)
	}
	if !createdS {
		t.Error("first schedule conversion must report created=true")
	}
	if converted.Context["legacy_schedule_id"] != "sched-1" {
		t.Errorf("legacy_schedule_id = %v", converted.Context["legacy_schedule_id"])
	}
	if converted.Schedule == nil || converted.Schedule.Interval != "daily" {
		t.Errorf("schedule attribute lost: %+v", converted.Schedule)
	}
	reconverted, createdS2, err := store.ImportScheduleTask(ctx, project.ID, in, "sched-1")
	if err != nil {
		t.Fatal(err)
	}
	if createdS2 || reconverted.ID != converted.ID {
		t.Errorf("schedule re-import created=%v id=%v; want false/%v", createdS2, reconverted.ID, converted.ID)
	}
}

func TestFileStoreSnapshotPersistsAcrossReopen(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.EnsureDefaultProject(ctx); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateProject(ctx, "Persisted", Scope{Targets: []string{"persist.example"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, created.ID, TaskInput{Name: "Persisted task", Context: map[string]any{"note": "kept"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, RunMount{InstanceID: "persist-inst", Mode: "single"}); err != nil {
		t.Fatal(err)
	}

	// The snapshot file exists, is 0600, and lives in the reserved dir.
	info, err := os.Stat(FilePath(dataDir))
	if err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode = %v, want 0600", info.Mode().Perm())
	}

	reopened, err := NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetProject(ctx, created.ID)
	if err != nil {
		t.Fatalf("reopened store lost project: %v", err)
	}
	if got.Slug != "persisted" {
		t.Errorf("reopened slug = %q", got.Slug)
	}
	gotTask, err := reopened.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reopened store lost task: %v", err)
	}
	if gotTask.Context["note"] != "kept" {
		t.Errorf("context lost on reopen: %+v", gotTask.Context)
	}
	runs, total, err := reopened.ListRuns(ctx, task.ID, 1, 10)
	if err != nil || total != 1 || len(runs) != 1 {
		t.Fatalf("reopened runs = %d/%d err=%v", len(runs), total, err)
	}
}

func TestFileStoreConcurrentWritesStayConsistent(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()
	project, _ := store.CreateProject(ctx, "Racy", Scope{})
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			_, _ = store.CreateTask(ctx, project.ID, TaskInput{Name: "Concurrent task"})
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	tasks, err := store.ListTasks(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 8 {
		t.Fatalf("concurrent creates produced %d tasks, want 8", len(tasks))
	}
}

func TestNewFileStoreRequiresDataDir(t *testing.T) {
	if _, err := NewFileStore(""); err == nil {
		t.Fatal("NewFileStore with an empty data dir must fail")
	}
}

// TestFileStoreScopeRejectsPortEntries pins the fail-closed port policy
// (MEDIUM-4a): scope entries are hostnames only; an entry carrying its own
// ":port" is refused at write time because ports are governed exclusively by
// allow_ports (empty = unrestricted).
func TestFileStoreScopeRejectsPortEntries(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()

	if _, err := store.CreateProject(ctx, "Porty", Scope{Targets: []string{"example.com:8080"}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("port-carrying scope create = %v, want ErrValidation", err)
	}
	project, err := store.CreateProject(ctx, "Portless", Scope{Targets: []string{"clean.example"}})
	if err != nil {
		t.Fatal(err)
	}
	porty := Scope{Targets: []string{"patched.example:9443"}}
	if _, err := store.UpdateProject(ctx, project.ID, ProjectUpdate{Scope: &porty}); !errors.Is(err, ErrValidation) {
		t.Fatalf("port-carrying scope patch = %v, want ErrValidation", err)
	}
	got, err := store.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope.Targets[0] != "clean.example" {
		t.Errorf("refused patch mutated the scope: %+v", got.Scope)
	}
	// A bracketed IPv6 host with a port is caught; a bare IPv6 host is not.
	if _, err := store.CreateProject(ctx, "V6bracket", Scope{Targets: []string{"[2001:db8::1]:443"}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("bracketed IPv6 with port = %v, want ErrValidation", err)
	}
	if _, err := store.CreateProject(ctx, "V6bare", Scope{Targets: []string{"2001:db8::1"}}); err != nil {
		t.Fatalf("bare IPv6 scope entry rejected: %v", err)
	}
}

func TestFileStoreProjectScanIDsUnionsInstanceAndLegacyIDs(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Info Scope", Scope{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "Info"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, RunMount{InstanceID: "inst-info", LegacyScanID: "legacy-info"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, RunMount{InstanceID: "inst-only"}); err != nil {
		t.Fatal(err)
	}
	ids, err := store.ProjectScanIDs(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"inst-info": true, "legacy-info": true, "inst-only": true}
	if len(ids) != len(want) {
		t.Fatalf("ProjectScanIDs = %v, want %d ids", ids, len(want))
	}
	for _, id := range ids {
		if !want[id] {
			t.Fatalf("unexpected scan id %q in %v", id, ids)
		}
	}
}

func TestFileStoreTaskContextFieldsAndRunCleanup(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Context Host", Scope{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, TaskInput{
		Name:         "Context task",
		Instruction:  "use the uploaded API inventory",
		ContextPaths: []string{" /ctx/a ", "/ctx/a", "/ctx/b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Instruction != "use the uploaded API inventory" {
		t.Fatalf("instruction lost on create: %+v", task)
	}
	if got := task.ContextPaths; len(got) != 2 || got[0] != "/ctx/a" || got[1] != "/ctx/b" {
		t.Fatalf("context_paths not normalized/persisted: %v", got)
	}
	updated, err := store.UpdateTask(ctx, task.ID, TaskUpdate{ContextPaths: []string{"/ctx/c"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.ContextPaths) != 1 || updated.ContextPaths[0] != "/ctx/c" {
		t.Fatalf("context_paths update lost: %v", updated.ContextPaths)
	}
	run, err := store.CreateRun(ctx, task.ID, RunMount{InstanceID: "ctx-inst", Mode: "single"})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := store.DeleteRunsByScanIdentity(ctx, "ctx-inst", "")
	if err != nil || removed != 1 {
		t.Fatalf("DeleteRunsByScanIdentity = %d, %v; want 1, nil", removed, err)
	}
	if runs, err := store.RunsByInstanceID(ctx, "ctx-inst"); err != nil || len(runs) != 0 {
		t.Fatalf("run survived cleanup: %+v, %v (run id %s)", runs, err, run.ID)
	}
}

func TestFileStoreImportIdempotentByInstanceOrLegacyID(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "Modern mount"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, task.ID, RunMount{InstanceID: "modern-inst", Mode: "single"})
	if err != nil {
		t.Fatal(err)
	}
	// The importer has no legacy ID for this scan; the instance key alone must
	// prevent a second mount in the default import task.
	importTask, err := store.EnsureImportTask(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, created, err := store.ImportLegacyRun(ctx, importTask.ID, RunMount{
		LegacyScanID: "legacy-new", InstanceID: "modern-inst", Mode: "single",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created || got.ID != run.ID {
		t.Fatalf("instance-key import duplicated the run: created=%v got=%s want=%s", created, got.ID, run.ID)
	}
	if runs, _, err := store.ListRuns(ctx, importTask.ID, 1, 10); err != nil || len(runs) != 0 {
		t.Fatalf("modern run remounted under import task: %+v, %v", runs, err)
	}
}

// TestFileStoreTaskByLegacyScheduleID pins the read-only reverse lookup the
// scheduler→run mount depends on: hit returns the imported task with its
// context, a miss and a same-key task in another project both yield
// ErrNotFound (D7 project isolation), and the lookup never creates anything.
func TestFileStoreTaskByLegacyScheduleID(t *testing.T) {
	store := newTestFileStore(t)
	ctx := context.Background()
	projectA, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := store.CreateProject(ctx, "Other", Scope{Targets: []string{"other.example"}})
	if err != nil {
		t.Fatal(err)
	}

	converted, _, err := store.ImportScheduleTask(ctx, projectA.ID,
		TaskInput{Name: "Nightly perimeter", Schedule: &TaskSchedule{Interval: "daily"}}, "sched-lookup-1")
	if err != nil {
		t.Fatal(err)
	}

	hit, err := store.TaskByLegacyScheduleID(ctx, projectA.ID, "sched-lookup-1")
	if err != nil {
		t.Fatalf("hit: %v", err)
	}
	if hit.ID != converted.ID {
		t.Errorf("hit task = %s, want %s", hit.ID, converted.ID)
	}
	if hit.Context["legacy_schedule_id"] != "sched-lookup-1" {
		t.Errorf("hit context lost: %+v", hit.Context)
	}

	if _, err := store.TaskByLegacyScheduleID(ctx, projectA.ID, "sched-lookup-miss"); !errors.Is(err, ErrNotFound) {
		t.Errorf("miss = %v, want ErrNotFound", err)
	}
	// Same key in another project must not resolve (row-level isolation).
	if _, err := store.TaskByLegacyScheduleID(ctx, projectB.ID, "sched-lookup-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-project lookup = %v, want ErrNotFound", err)
	}
}
