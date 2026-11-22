package runstate

// PG round-trip integration test for the R6.1 runs-table takeover of
// scan.json: the per-record row (run_scan_records, migration 0016), the runs
// mount row's takeover columns (migration 0012, top-level records only) and
// the failed-watermark path. Skipped unless XALGORIX_TEST_DATABASE_URL is
// set (the same gate as the R6.0 importer integration test; CI gains a PG
// service with R6.1).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem/postgres"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// ensureScanRecordSchema applies the shared embedded migrations (0016
// included) so the takeover tests run on a fresh test database too. Test-only:
// production migrates in platform.Open / coldpath.New before any writes.
func ensureScanRecordSchema(t *testing.T, store *Store) {
	t.Helper()
	if err := postgres.Migrate(context.Background(), store.pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func TestPutScanRecordRoundTrip(t *testing.T) {
	store := integrationStore(t)
	ensureScanRecordSchema(t, store)
	ctx := context.Background()

	detail := []byte(`{
	  "id":"scan-r61","instance_id":"inst-r61","status":"running",
	  "target":"r61.example","parent_target":"",
	  "started_at":"2026-05-01T18:00:00Z","finished_at":"",
	  "current_phase":3,"plan_tasks_total":4,"plan_tasks_completed":2,
	  "events":[{"type":"message","content":"tail"}],"events_journal":true
	}`)
	if err := store.PutScanRecord(ScanRecordState{
		ScanID:     "scan-r61",
		InstanceID: "inst-r61",
		ScanDir:    "r61.example/2026-05-01/scan-r61",
		Detail:     detail,
	}); err != nil {
		t.Fatalf("put scan record: %v", err)
	}

	// Per-record row round-trips verbatim with its anchor.
	row, ok, err := store.LoadScanRecord(ctx, "scan-r61")
	if err != nil || !ok {
		t.Fatalf("load scan record: ok=%v err=%v", ok, err)
	}
	if row.InstanceID != "inst-r61" || row.ScanDir != "r61.example/2026-05-01/scan-r61" {
		t.Fatalf("row identity = %+v", row)
	}
	if string(row.Detail) != string(detail) {
		t.Fatalf("detail lost byte fidelity:\n got %s\nwant %s", row.Detail, detail)
	}

	// Idempotent re-save (a retried safe point reuses the row).
	if err := store.PutScanRecord(ScanRecordState{
		ScanID: "scan-r61", InstanceID: "inst-r61",
		ScanDir: "r61.example/2026-05-01/scan-r61", Detail: detail,
	}); err != nil {
		t.Fatalf("re-put: %v", err)
	}
	_, ok, _ = store.LoadScanRecord(ctx, "scan-r61")
	if !ok {
		t.Fatalf("row vanished after idempotent re-save")
	}

	if err := store.DeleteScanRecord(ctx, "scan-r61"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := store.LoadScanRecord(ctx, "scan-r61"); ok {
		t.Fatalf("row survived delete")
	}
}

func TestPutScanRecordTopLevelGating(t *testing.T) {
	store := integrationStore(t)
	ensureScanRecordSchema(t, store)
	ctx := context.Background()

	// A wildcard child must not overwrite the instance's newest top-level
	// snapshot on the runs mount row — only its own per-record row.
	child := []byte(`{"id":"scan-r61-child","instance_id":"inst-r61-gate","status":"finished","target":"child.gate.example","parent_target":"gate.example","started_at":"2026-05-01T19:00:00Z"}`)
	if err := store.PutScanRecord(ScanRecordState{
		ScanID: "scan-r61-child", InstanceID: "inst-r61-gate",
		ParentTarget: "gate.example", ScanDir: "child.gate.example/2026-05-01/scan-r61-child",
		Detail: child,
	}); err != nil {
		t.Fatalf("put child: %v", err)
	}
	parent := []byte(`{"id":"scan-r61-parent","instance_id":"inst-r61-gate","status":"finished","target":"gate.example","started_at":"2026-05-01T18:00:00Z","finished_at":"2026-05-01T20:00:00Z","plan_tasks_total":9,"plan_tasks_completed":8}`)
	if err := store.PutScanRecord(ScanRecordState{
		ScanID: "scan-r61-parent", InstanceID: "inst-r61-gate",
		ScanDir: "gate.example/2026-05-01/scan-r61-parent", Detail: parent,
	}); err != nil {
		t.Fatalf("put parent: %v", err)
	}

	// Both per-record rows exist.
	for _, id := range []string{"scan-r61-child", "scan-r61-parent"} {
		if _, ok, err := store.LoadScanRecord(ctx, id); err != nil || !ok {
			t.Fatalf("row %s missing: ok=%v err=%v", id, ok, err)
		}
	}

	// The runs row (unmounted instance: no row expected) — the takeover
	// update is a no-op on a missing mount, never an error. Assert via a
	// direct count so the test also documents the posture.
	var runsRows int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM runs WHERE instance_id = $1`, "inst-r61-gate",
	).Scan(&runsRows); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runsRows != 0 {
		t.Fatalf("unmounted instance created %d runs rows, want 0", runsRows)
	}

	// A MOUNTED instance: the top-level record's save writes all nine takeover
	// columns onto the mount row (the R6.1 "runs 表接管 scan.json" write).
	// Close ordering: t.Cleanup runs LIFO, so the pool's Close is registered
	// BEFORE the task cleanup that still needs the pool.
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	project, err := platform.Open(ctx, platform.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("platform store: %v", err)
	}
	if concrete, ok := project.(*platform.Store); ok {
		t.Cleanup(concrete.Close)
	}
	defProject, err := project.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatalf("default project: %v", err)
	}
	task, err := project.CreateTask(ctx, defProject.ID, platform.TaskInput{Name: "r61 takeover gate"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	t.Cleanup(func() { _ = project.DeleteTask(context.Background(), task.ID) })
	if _, err := project.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-r61-gate"}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := store.PutScanRecord(ScanRecordState{
		ScanID: "scan-r61-parent", InstanceID: "inst-r61-gate",
		ScanDir: "gate.example/2026-05-01/scan-r61-parent", Detail: parent,
	}); err != nil {
		t.Fatalf("put mounted parent: %v", err)
	}
	var (
		scanStatus   string
		scanTarget   string
		scanParent   string
		planTotal    int
		planDone     int
		detailStored []byte
	)
	if err := store.pool.QueryRow(ctx,
		`SELECT scan_status, scan_target, scan_parent_target, scan_plan_total, scan_plan_completed, scan_detail
		 FROM runs WHERE instance_id = $1`, "inst-r61-gate",
	).Scan(&scanStatus, &scanTarget, &scanParent, &planTotal, &planDone, &detailStored); err != nil {
		t.Fatalf("read runs takeover columns: %v", err)
	}
	if scanStatus != "finished" || scanTarget != "gate.example" || planTotal != 9 || planDone != 8 {
		t.Fatalf("runs takeover columns = %s/%s/%d/%d", scanStatus, scanTarget, planTotal, planDone)
	}
	var stored map[string]any
	if err := json.Unmarshal(detailStored, &stored); err != nil {
		t.Fatalf("scan_detail decode: %v", err)
	}
	if stored["id"] != "scan-r61-parent" {
		t.Fatalf("scan_detail = %v", stored)
	}

	// The child's save must NOT flip the mount row onto the child snapshot.
	if err := store.PutScanRecord(ScanRecordState{
		ScanID: "scan-r61-child", InstanceID: "inst-r61-gate",
		ParentTarget: "gate.example",
		ScanDir:      "child.gate.example/2026-05-01/scan-r61-child", Detail: child,
	}); err != nil {
		t.Fatalf("put mounted child: %v", err)
	}
	if err := store.pool.QueryRow(ctx,
		`SELECT scan_target FROM runs WHERE instance_id = $1`, "inst-r61-gate",
	).Scan(&scanTarget); err != nil {
		t.Fatalf("re-read runs: %v", err)
	}
	if scanTarget != "gate.example" {
		t.Fatalf("child record overwrote the mount row (scan_target=%s)", scanTarget)
	}

	store.DeleteScanRecord(ctx, "scan-r61-child")  //nolint:errcheck // cleanup
	store.DeleteScanRecord(ctx, "scan-r61-parent") //nolint:errcheck // cleanup
}

// TestImportDataDirLandsScanRecordRow pins the R6.0 importer ↔ R6.1
// per-record home bridge: a directory import now lands the run_scan_records
// row too (scan_id = directory basename, dataDir-relative anchor), still
// watermark-guarded and idempotent.
func TestImportDataDirLandsScanRecordRow(t *testing.T) {
	store := integrationStore(t)
	ensureScanRecordSchema(t, store)
	ctx := context.Background()

	if _, err := store.pool.Exec(ctx, `DELETE FROM run_state_watermarks WHERE scan_id = $1`, "scan-old"); err != nil {
		t.Fatalf("re-arm import: %v", err)
	}
	scanDir := writeFixtures(t)
	dataDir := filepath.Dir(filepath.Dir(filepath.Dir(scanDir)))

	if _, err := store.ImportDataDir(ctx, dataDir); err != nil {
		t.Fatalf("import: %v", err)
	}
	// The imported row must not leak into other tests sweeping the shared
	// test database (the web end-to-end test asserts its own set).
	t.Cleanup(func() {
		_ = store.DeleteScanRecord(context.Background(), "scan-old")
		_, _ = store.pool.Exec(context.Background(),
			`DELETE FROM run_state_watermarks WHERE scan_id = $1`, "scan-old")
	})
	row, ok, err := store.LoadScanRecord(ctx, "scan-old")
	if err != nil || !ok {
		t.Fatalf("imported scan record row missing: ok=%v err=%v", ok, err)
	}
	if row.InstanceID != "inst-old" {
		t.Fatalf("imported identity = %+v", row)
	}
	rel, rerr := filepath.Rel(dataDir, scanDir)
	if rerr != nil {
		t.Fatalf("rel: %v", rerr)
	}
	if want := filepath.ToSlash(rel); row.ScanDir != want {
		t.Fatalf("imported anchor = %q, want %q", row.ScanDir, want)
	}
	var rec map[string]any
	if err := json.Unmarshal(row.Detail, &rec); err != nil {
		t.Fatalf("imported detail decode: %v", err)
	}
	if rec["status"] != "finished" || rec["plan_tasks_total"] != float64(10) {
		t.Fatalf("imported detail = %v", rec)
	}
	// Re-import skips (watermark) and the row survives.
	if _, err := store.ImportDataDir(ctx, dataDir); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if _, ok, _ := store.LoadScanRecord(ctx, "scan-old"); !ok {
		t.Fatalf("row vanished after skipped re-import")
	}
}

func TestScanRecordSweepSeenByPlatformStore(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	store := integrationStore(t)
	ctx := context.Background()

	// The production pairing: one database, runstate writes the per-record
	// rows, the platform store sweeps them (the web layer's read API). The
	// platform store construction also applies the shared migrations, so
	// run_scan_records (0016) exists even on a fresh test database.
	platformStore, err := platform.Open(ctx, platform.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("platform store: %v", err)
	}
	if concrete, ok := platformStore.(*platform.Store); ok {
		t.Cleanup(concrete.Close)
	}

	detail := []byte(`{"id":"scan-r61-sweep","instance_id":"inst-r61-sweep","status":"finished","target":"sweep.example","started_at":"2026-05-01T18:00:00Z","finished_at":"2026-05-01T19:00:00Z"}`)
	if err := store.PutScanRecord(ScanRecordState{
		ScanID: "scan-r61-sweep", InstanceID: "inst-r61-sweep",
		ScanDir: "sweep.example/2026-05-01/scan-r61-sweep",
		Detail:  detail, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteScanRecord(context.Background(), "scan-r61-sweep") })

	rows, next, err := platformStore.ScanRunRecords(ctx, "", 100)
	if err != nil {
		t.Fatalf("platform sweep: %v", err)
	}
	if next != "" && len(rows) >= 100 {
		t.Fatalf("unexpected cursor %q with %d rows", next, len(rows))
	}
	found := false
	for _, row := range rows {
		if row.ScanID == "scan-r61-sweep" {
			found = true
			if row.InstanceID != "inst-r61-sweep" || row.ScanDir != "sweep.example/2026-05-01/scan-r61-sweep" {
				t.Fatalf("swept identity = %+v", row)
			}
			var rec map[string]any
			if err := json.Unmarshal(row.Detail, &rec); err != nil {
				t.Fatalf("swept detail decode: %v", err)
			}
			if rec["target"] != "sweep.example" || rec["status"] != "finished" {
				t.Fatalf("swept detail = %v", rec)
			}
		}
	}
	if !found {
		t.Fatalf("sweep of %d rows missed scan-r61-sweep", len(rows))
	}
}
