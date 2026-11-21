package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	infopg "github.com/xalgord/xalgorix/v4/internal/infoitem/postgres"
)

// integrationRepo mirrors the infoitem/postgres integration pattern: tests
// skip unless XALGORIX_TEST_DATABASE_URL is set. Every fixture project uses a
// unique slug and cleans up by project_id (tasks/runs cascade).
func integrationRepo(t *testing.T) (Repository, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	repo, err := Open(context.Background(), Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	store, ok := repo.(*Store)
	if !ok {
		t.Fatalf("Open with a DSN returned %T, want *Store", repo)
	}
	t.Cleanup(store.Close)
	return store, store.pool
}

func cleanupProject(t *testing.T, pool *pgxpool.Pool, projectID string) {
	t.Helper()
	parsed, err := uuid.Parse(projectID)
	if err != nil {
		return
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, parsed)
	})
}

func TestPostgresDefaultProjectSeed(t *testing.T) {
	repo, pool := integrationRepo(t)
	ctx := context.Background()

	first, err := repo.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Errorf("default project reseeded (%q vs %q)", first.ID, second.ID)
	}
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM projects WHERE slug = $1`, DefaultProjectSlug).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("default slug rows = %d, want 1", count)
	}
	// The default project is protected.
	if err := repo.DeleteProject(ctx, first.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("delete default = %v, want ErrConflict", err)
	}
}

func TestPostgresProjectTaskRunCRUDAndIsolation(t *testing.T) {
	repo, pool := integrationRepo(t)
	ctx := context.Background()

	stamp := time.Now().UnixNano()
	nameA := fmt.Sprintf("Isolation A %d", stamp)
	nameB := fmt.Sprintf("Isolation B %d", stamp)

	projA, err := repo.CreateProject(ctx, nameA, Scope{Targets: []string{"a.example"}})
	if err != nil {
		t.Fatal(err)
	}
	cleanupProject(t, pool, projA.ID)
	projB, err := repo.CreateProject(ctx, nameB, Scope{Targets: []string{"b.example"}})
	if err != nil {
		t.Fatal(err)
	}
	cleanupProject(t, pool, projB.ID)

	// Unique slugs; the shared "Isolation A" name would collide, so verify
	// the -2 suffix path with a same-named second project.
	dup, err := repo.CreateProject(ctx, nameA, Scope{})
	if err != nil {
		t.Fatal(err)
	}
	cleanupProject(t, pool, dup.ID)
	if dup.Slug == projA.Slug {
		t.Errorf("duplicate name produced duplicate slug %q", dup.Slug)
	}
	if want := "isolation-a-" + fmt.Sprint(stamp); projA.Slug != want {
		t.Errorf("slug = %q, want %q", projA.Slug, want)
	}

	taskA, err := repo.CreateTask(ctx, projA.ID, TaskInput{
		Name:         "A task",
		Instruction:  "check A",
		ContextPaths: []string{"/ctx/a", "/ctx/b"},
		PhasePolicy:  &PhasePolicy{ScanMode: "wildcard"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if taskA.Instruction != "check A" || len(taskA.ContextPaths) != 2 {
		t.Fatalf("task instruction/context_paths lost on create: %+v", taskA)
	}
	if _, err := repo.CreateTask(ctx, projA.ID, TaskInput{Name: "A task"}); err != nil {
		t.Fatalf("task slug conflict: %v", err)
	}
	tasksA, err := repo.ListTasks(ctx, projA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasksA) != 2 {
		t.Fatalf("tasks of A = %d, want 2", len(tasksA))
	}
	if tasksA[0].Instruction != "check A" || len(tasksA[0].ContextPaths) != 2 {
		t.Fatalf("task fields lost on list: %+v", tasksA[0])
	}
	tasksB, err := repo.ListTasks(ctx, projB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasksB) != 0 {
		t.Fatalf("row-level isolation broken: B sees %d tasks of A", len(tasksB))
	}

	// Run mounts: unique instance_id enforced; project listing isolated.
	if _, err := repo.CreateRun(ctx, taskA.ID, RunMount{InstanceID: fmt.Sprintf("pg-inst-%d", stamp), Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRun(ctx, taskA.ID, RunMount{InstanceID: fmt.Sprintf("pg-inst-%d", stamp)}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate instance = %v, want ErrConflict", err)
	}
	runsB, err := repo.ListRunsByProject(ctx, projB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runsB) != 0 {
		t.Errorf("row-level isolation broken: B sees %d runs of A", len(runsB))
	}
	idsA, err := repo.InstanceIDsForProject(ctx, projA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(idsA) != 1 {
		t.Errorf("InstanceIDsForProject(A) = %v, want 1", idsA)
	}
	scanIDsA, err := repo.ProjectScanIDs(ctx, projA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanIDsA) != 1 || scanIDsA[0] != fmt.Sprintf("pg-inst-%d", stamp) {
		t.Errorf("ProjectScanIDs(A) = %v", scanIDsA)
	}
	scanIDsB, err := repo.ProjectScanIDs(ctx, projB.ID)
	if err != nil || len(scanIDsB) != 0 {
		t.Errorf("ProjectScanIDs(B) = %v, %v; want empty", scanIDsB, err)
	}

	// Project delete cascades tasks and runs.
	if _, err := repo.CreateRun(ctx, taskA.ID, RunMount{InstanceID: fmt.Sprintf("pg-del-%d", stamp)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteProject(ctx, projA.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	var taskCount, runCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE project_id = $1`, mustUUID(t, projA.ID)).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 0 {
		t.Errorf("tasks survived project delete: %d", taskCount)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id = $1`, mustUUID(t, taskA.ID)).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Errorf("runs survived project delete: %d", runCount)
	}
}

func TestPostgresLegacyImportIdempotency(t *testing.T) {
	repo, _ := integrationRepo(t)
	ctx := context.Background()

	project, err := repo.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := repo.EnsureImportTask(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacyID := fmt.Sprintf("pg-legacy-%d", time.Now().UnixNano())
	run, created, err := repo.ImportLegacyRun(ctx, task.ID, RunMount{LegacyScanID: legacyID, InstanceID: legacyID, Mode: "wildcard"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first import must create")
	}
	replay, created2, err := repo.ImportLegacyRun(ctx, task.ID, RunMount{LegacyScanID: legacyID, InstanceID: legacyID, Mode: "wildcard"})
	if err != nil {
		t.Fatal(err)
	}
	if created2 || replay.ID != run.ID {
		t.Errorf("replay created=%v id=%v; want false/%v", created2, replay.ID, run.ID)
	}
	// A second scan identity carrying the same instance_id must also resolve
	// to the original mount, never duplicate it under another task.
	byInstance, created3, err := repo.ImportLegacyRun(ctx, task.ID, RunMount{
		LegacyScanID: legacyID + "-two", InstanceID: legacyID, Mode: "wildcard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created3 || byInstance.ID != run.ID {
		t.Errorf("instance-key import created=%v id=%v; want false/%v", created3, byInstance.ID, run.ID)
	}

	ids, err := repo.LegacyScanIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ids[legacyID]; !ok {
		t.Error("LegacyScanIDs missing imported id")
	}

	// Schedule conversion keyed by context.legacy_schedule_id.
	stamp := time.Now().UnixNano()
	converted, createdS, err := repo.ImportScheduleTask(ctx, project.ID,
		TaskInput{Name: fmt.Sprintf("PG schedule %d", stamp), Schedule: &TaskSchedule{Interval: "hourly"}},
		fmt.Sprintf("pg-sched-%d", stamp))
	if err != nil {
		t.Fatal(err)
	}
	if !createdS {
		t.Fatal("first schedule conversion must create")
	}
	t.Cleanup(func() { _ = repo.DeleteTask(context.Background(), converted.ID) })
	again, createdS2, err := repo.ImportScheduleTask(ctx, project.ID,
		TaskInput{Name: fmt.Sprintf("PG schedule %d", stamp)}, fmt.Sprintf("pg-sched-%d", stamp))
	if err != nil {
		t.Fatal(err)
	}
	if createdS2 || again.ID != converted.ID {
		t.Errorf("schedule re-import created=%v id=%v; want false/%v", createdS2, again.ID, converted.ID)
	}
}

// TestPostgresOpenRunsSharedMigrations verifies the 0002 platform tables exist
// after platform.Open (the shared embedded migrator applied them).
func TestPostgresOpenRunsSharedMigrations(t *testing.T) {
	_, pool := integrationRepo(t)
	ctx := context.Background()
	for _, table := range []string{"projects", "tasks", "runs", "info_items"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("table %s missing after Open/migrations", table)
		}
	}
	if err := infopg.Migrate(ctx, pool); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
}

func mustUUID(t *testing.T, id string) uuid.UUID {
	t.Helper()
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("bad uuid %q: %v", id, err)
	}
	return parsed
}

func TestPostgresRunFindingsProjectionAndCascade(t *testing.T) {
	repo, pool := integrationRepo(t)
	ctx := context.Background()
	fs, ok := repo.(FindingsStore)
	if !ok {
		t.Fatalf("%T does not implement FindingsStore", repo)
	}
	stamp := time.Now().UnixNano()
	project, err := repo.CreateProject(ctx, fmt.Sprintf("Findings %d", stamp), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	cleanupProject(t, pool, project.ID)
	task, err := repo.CreateTask(ctx, project.ID, TaskInput{Name: "Task findings"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := repo.CreateRun(ctx, task.ID, RunMount{InstanceID: fmt.Sprintf("findings-a-%d", stamp)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.CreateRun(ctx, task.ID, RunMount{InstanceID: fmt.Sprintf("findings-b-%d", stamp)})
	if err != nil {
		t.Fatal(err)
	}
	oldPayload := json.RawMessage(`{"title":"old","severity":"high","scan_id":"old"}`)
	newPayload := json.RawMessage(`{"title":"new","severity":"high","scan_id":"new"}`)
	key := "example.com|/api|same title|high"
	if err := fs.ReplaceRunFindings(ctx, first.ID, time.Now().Add(-time.Hour), []RunFinding{
		{DedupKey: key, ScanStartedAt: time.Now().Add(-time.Hour), Finding: oldPayload},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fs.ReplaceRunFindings(ctx, second.ID, time.Now(), []RunFinding{
		{DedupKey: key, ScanStartedAt: time.Now(), Finding: newPayload},
	}); err != nil {
		t.Fatal(err)
	}
	items, err := fs.ListTaskFindings(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("projection rows = %d, want 1: %+v", len(items), items)
	}
	var got struct {
		Title  string `json:"title"`
		ScanID string `json:"scan_id"`
	}
	if err := json.Unmarshal(items[0].Finding, &got); err != nil {
		t.Fatalf("decode projected finding: %v", err)
	}
	// JSONB canonicalizes object key order, so compare the payload
	// semantically rather than by raw bytes.
	if got.Title != "new" || got.ScanID != "new" {
		t.Fatalf("newest projection not selected: %+v (%s)", got, items[0].Finding)
	}
	if count, err := fs.CountTaskFindings(ctx, task.ID); err != nil || count != 1 {
		t.Fatalf("CountTaskFindings = %d, %v; want 1, nil", count, err)
	}
	// Deleting the newest run cascades its row; the older projection for the
	// same key becomes the winner rather than disappearing.
	if _, err := repo.DeleteRunsByScanIdentity(ctx, second.InstanceID, second.LegacyScanID); err != nil {
		t.Fatal(err)
	}
	items, err = fs.ListTaskFindings(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("after newest run delete rows = %d, want 1", len(items))
	}
	if err := json.Unmarshal(items[0].Finding, &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "old" || got.ScanID != "old" {
		t.Fatalf("older projection did not take over: %+v", got)
	}
	// Deleting the remaining run leaves no finding rows.
	if _, err := repo.DeleteRunsByScanIdentity(ctx, first.InstanceID, first.LegacyScanID); err != nil {
		t.Fatal(err)
	}
	if count, err := fs.CountTaskFindings(ctx, task.ID); err != nil || count != 0 {
		t.Fatalf("findings survived run deletes: %d, %v", count, err)
	}
}

// TestPostgresEnsureImportTaskConcurrent pins the MEDIUM-3 race fix: two
// (here: eight) processes racing their first startup for the umbrella task
// must end with exactly one row — uq_tasks_legacy_import + ON CONFLICT DO
// NOTHING + re-read.
func TestPostgresEnsureImportTaskConcurrent(t *testing.T) {
	repo, pool := integrationRepo(t)
	ctx := context.Background()

	project, err := repo.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make([]string, 8)
	errs := make([]error, 8)
	for i := range ids {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			task, err := repo.EnsureImportTask(ctx, project.ID)
			if task != nil {
				ids[n] = task.ID
			}
			errs[n] = err
		}(i)
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if ids[i] == "" {
			t.Fatalf("goroutine %d returned no task", i)
		}
		if ids[i] != ids[0] {
			t.Fatalf("concurrent EnsureImportTask produced two tasks: %q vs %q", ids[i], ids[0])
		}
	}
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM tasks WHERE context->>'legacy_import' = 'scans'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("legacy_import tasks = %d, want 1", count)
	}
}

// TestPostgresScopeRejectsPortEntries pins MEDIUM-4a on the PG store.
func TestPostgresScopeRejectsPortEntries(t *testing.T) {
	repo, _ := integrationRepo(t)
	ctx := context.Background()
	if _, err := repo.CreateProject(ctx, "Porty", Scope{Targets: []string{"example.com:8080"}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("port-carrying scope create = %v, want ErrValidation", err)
	}
}

// ── R2a approvals + guidance (REDESIGN §5, API_CONTRACT §3/§3.1) ─────────────

// TestPostgresOpenRuns0003Migrations extends the migration smoke test to the
// R2a tables.
func TestPostgresOpenRuns0003Migrations(t *testing.T) {
	_, pool := integrationRepo(t)
	ctx := context.Background()
	for _, table := range []string{"approvals", "guidance"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("table %s missing after Open/migrations", table)
		}
	}
	// The partial unique index backing guidance idempotency must exist.
	var indexExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'uq_guidance_request_id')`).Scan(&indexExists); err != nil {
		t.Fatal(err)
	}
	if !indexExists {
		t.Fatal("uq_guidance_request_id index missing")
	}
}

// TestPostgresApprovalLifecycle walks the full state machine on the PG store:
// pending → decided (optimistic lock), slot accounting, batch cancel, summary.
func TestPostgresApprovalLifecycle(t *testing.T) {
	repo, pool := integrationRepo(t)
	ctx := context.Background()

	project, err := repo.CreateProject(ctx, fmt.Sprintf("Approval PG %d", time.Now().UnixNano()), Scope{Targets: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	cleanupProject(t, pool, project.ID)
	task, err := repo.CreateTask(ctx, project.ID, TaskInput{Name: "approval pg task"})
	if err != nil {
		t.Fatal(err)
	}

	row, err := repo.CreateApproval(ctx, ApprovalInput{
		ProjectID:  project.ID,
		TaskID:     task.ID,
		InstanceID: "pg-approval-inst",
		Kind:       ApprovalKindCommand,
		Payload:    ApprovalPayload{Command: "nmap -sV example.com", Reason: "shell command execution requires approval"},
		DeadlineAt: time.Now().Add(time.Minute).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != ApprovalPending {
		t.Fatalf("status = %s, want pending", row.Status)
	}
	if n, _ := repo.CountPendingByInstance(ctx, "pg-approval-inst"); n != 1 {
		t.Fatalf("pending count = %d, want 1", n)
	}

	// Optimistic lock: the first decision wins, the loser gets ErrConflict.
	final, err := repo.DecideApproval(ctx, row.ID, ApprovalDecision{Decision: ApprovalApproved, DecidedBy: "operator:admin"})
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != ApprovalApproved || final.DecidedAt == nil {
		t.Fatalf("decision lost: %+v", final)
	}
	if _, err := repo.DecideApproval(ctx, row.ID, ApprovalDecision{Decision: ApprovalRejected}); !errors.Is(err, ErrConflict) {
		t.Errorf("second decision = %v, want ErrConflict", err)
	}

	// Batch cancel semantics on a second instance.
	for i := 0; i < 2; i++ {
		if _, err := repo.CreateApproval(ctx, ApprovalInput{
			ProjectID:  project.ID,
			TaskID:     task.ID,
			InstanceID: "pg-cancel-inst",
			Kind:       ApprovalKindHTTP,
			Payload:    ApprovalPayload{Method: "POST", URL: "http://example.com/admin"},
			DeadlineAt: time.Now().Add(time.Minute).UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	cancelled, err := repo.CancelPendingByInstance(ctx, "pg-cancel-inst", "run stopped by operator")
	if err != nil {
		t.Fatal(err)
	}
	if len(cancelled) != 2 {
		t.Fatalf("cancelled = %d rows, want 2", len(cancelled))
	}
	for _, row := range cancelled {
		if row.Status != ApprovalCancelled || row.DecidedBy != "system" {
			t.Errorf("cancel semantics lost: %+v", row)
		}
	}

	// Summary counts only pending rows.
	summary, err := repo.PendingSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total < 0 {
		t.Fatal("negative pending total")
	}
	var pendingRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE status = 'pending'`).Scan(&pendingRows); err != nil {
		t.Fatal(err)
	}
	if summary.Total != pendingRows {
		t.Errorf("PendingSummary %d != count %d", summary.Total, pendingRows)
	}

	// Filters: decided = non-pending (approved + cancelled from the batch
	// above); per-project isolation.
	decided, err := repo.ListApprovals(ctx, ApprovalFilter{Status: "decided", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(decided) != 3 {
		t.Fatalf("decided filter = %d rows, want 3 (1 approved + 2 cancelled)", len(decided))
	}
	statuses := map[string]int{}
	for _, d := range decided {
		statuses[d.Status]++
	}
	if statuses[ApprovalApproved] != 1 || statuses[ApprovalCancelled] != 2 {
		t.Fatalf("decided statuses = %v, want 1 approved + 2 cancelled", statuses)
	}
}

// TestPostgresGuidanceIdempotentConcurrent pins the contract §3.1 idempotency
// on the partial unique index: two concurrent creators with the same
// request_id end with exactly one row and both callers see created semantics
// consistent with the winner.
func TestPostgresGuidanceIdempotentConcurrent(t *testing.T) {
	repo, pool := integrationRepo(t)
	ctx := context.Background()

	project, err := repo.CreateProject(ctx, fmt.Sprintf("Guidance PG %d", time.Now().UnixNano()), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	cleanupProject(t, pool, project.ID)
	task, err := repo.CreateTask(ctx, project.ID, TaskInput{Name: "guidance pg task"})
	if err != nil {
		t.Fatal(err)
	}

	in := GuidanceInput{
		TaskID:    task.ID,
		Message:   "concentrate on the auth bypass first",
		RequestID: fmt.Sprintf("pg-guidance-%d", time.Now().UnixNano()),
	}
	const racers = 8
	var wg sync.WaitGroup
	ids := make([]string, racers)
	createdFlags := make([]bool, racers)
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			g, created, err := repo.CreateGuidance(ctx, in)
			if g != nil {
				ids[n] = g.ID
				createdFlags[n] = created
			}
			errs[n] = err
		}(i)
	}
	wg.Wait()
	for i := 0; i < racers; i++ {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if ids[i] == "" || ids[i] != ids[0] {
			t.Fatalf("racer %d got row %q, want the single winner %q", i, ids[i], ids[0])
		}
	}
	var createdCount int
	for i := range createdFlags {
		if createdFlags[i] {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Errorf("created=true seen %d times, want exactly 1", createdCount)
	}
	var rows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM guidance WHERE task_id = $1 AND request_id = $2`,
		mustUUID(t, task.ID), in.RequestID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("guidance rows for the request = %d, want 1", rows)
	}

	// Sequential replay keeps the first payload and never duplicates.
	replay, created, err := repo.CreateGuidance(ctx, GuidanceInput{TaskID: task.ID, Message: "different text", RequestID: in.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	if created || replay.ID != ids[0] || replay.Message != in.Message {
		t.Errorf("replay created=%v row=%v message=%q; want false/%s/%q", created, replay.ID, replay.Message, ids[0], in.Message)
	}
}

// TestPostgresTaskByLegacyScheduleID mirrors the file-store reverse-lookup
// contract on PG: hit, miss → ErrNotFound, and cross-project isolation.
func TestPostgresTaskByLegacyScheduleID(t *testing.T) {
	repo, pool := integrationRepo(t)
	ctx := context.Background()

	stamp := time.Now().UnixNano()
	projectA, err := repo.CreateProject(ctx, fmt.Sprintf("SchedLookup A %d", stamp), Scope{Targets: []string{"a.example"}})
	if err != nil {
		t.Fatal(err)
	}
	cleanupProject(t, pool, projectA.ID)
	projectB, err := repo.CreateProject(ctx, fmt.Sprintf("SchedLookup B %d", stamp), Scope{Targets: []string{"b.example"}})
	if err != nil {
		t.Fatal(err)
	}
	cleanupProject(t, pool, projectB.ID)

	converted, _, err := repo.ImportScheduleTask(ctx, projectA.ID,
		TaskInput{Name: fmt.Sprintf("PG lookup %d", stamp), Schedule: &TaskSchedule{Interval: "hourly"}},
		fmt.Sprintf("pg-sched-lookup-%d", stamp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.DeleteTask(context.Background(), converted.ID) })

	hit, err := repo.TaskByLegacyScheduleID(ctx, projectA.ID, fmt.Sprintf("pg-sched-lookup-%d", stamp))
	if err != nil {
		t.Fatalf("hit: %v", err)
	}
	if hit.ID != converted.ID {
		t.Errorf("hit task = %s, want %s", hit.ID, converted.ID)
	}

	if _, err := repo.TaskByLegacyScheduleID(ctx, projectA.ID, "pg-sched-lookup-miss"); !errors.Is(err, ErrNotFound) {
		t.Errorf("miss = %v, want ErrNotFound", err)
	}
	if _, err := repo.TaskByLegacyScheduleID(ctx, projectB.ID, fmt.Sprintf("pg-sched-lookup-%d", stamp)); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-project lookup = %v, want ErrNotFound", err)
	}
}
