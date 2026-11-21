package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

func TestProjectFilterFailsClosedWithoutPlatformStore(t *testing.T) {
	s := newTestServer(t, nil)
	for _, path := range []string{
		"/api/scans?project_id=p1",
		"/api/instances?project_id=p1",
		"/api/findings?project_id=p1",
		"/api/findings/summary?project_id=p1",
	} {
		rr := doJSON(t, s, http.MethodGet, path, "")
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want 503 fail-closed; body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestScopeDefaultPortsAndWildcard(t *testing.T) {
	scope := platform.Scope{Targets: []string{"*.example.com"}, AllowPorts: []int{443}}
	cases := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{name: "wildcard subdomain https", target: "https://app.example.com"},
		{name: "wildcard does not match apex", target: "https://example.com", wantErr: true},
		{name: "http default is denied", target: "http://app.example.com", wantErr: true},
		{name: "bare host needs both defaults", target: "app.example.com", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTargetsInScope([]string{tc.target}, scope)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateTargetsInScope(%q) = %v, wantErr=%v", tc.target, err, tc.wantErr)
			}
		})
	}
	both := platform.Scope{Targets: []string{"*.example.com"}, AllowPorts: []int{80, 443}}
	if err := validateTargetsInScope([]string{"app.example.com"}, both); err != nil {
		t.Fatalf("bare host with both defaults denied: %v", err)
	}
}

func TestTaskRunContextPathsIncludeAllTaskAttachments(t *testing.T) {
	task := &platform.Task{ContextPaths: []string{"/ctx/a", "/ctx/b"}}
	override := ScanRequest{ScanContext: "/ctx/c", ScanContextPaths: []string{"/ctx/b", " /ctx/d "}}
	got := taskRunContextPaths(task, override)
	want := []string{"/ctx/a", "/ctx/b", "/ctx/c", "/ctx/d"}
	if len(got) != len(want) {
		t.Fatalf("context paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("context paths = %v, want %v", got, want)
		}
	}
}

func TestRunProjectionDistinguishesMissingFromPending(t *testing.T) {
	idx := &scanSummaryIndex{}
	run := platform.Run{ID: "run-1", InstanceID: "inst-1"}
	idx.projectRun(&run, func(string) bool { return false })
	if run.Status != RunMissingStatus {
		t.Fatalf("missing run status = %q, want %q", run.Status, RunMissingStatus)
	}
	if n := idx.activeRunCount([]platform.Run{run}, func(string) bool { return false }); n != 0 {
		t.Fatalf("missing run counted active: %d", n)
	}
	pending := platform.Run{ID: "run-2", InstanceID: "inst-2"}
	idx.projectRun(&pending, func(id string) bool { return id == "inst-2" })
	if pending.Status != "pending" {
		t.Fatalf("durable pending dispatch status = %q, want pending", pending.Status)
	}
}

// runPageStub exposes only ListRuns; the findings degradation path is the only
// consumer under test.
type runPageStub struct {
	platform.Repository
	runs []platform.Run
}

func (s *runPageStub) ListRuns(_ context.Context, _ string, page, size int) ([]platform.Run, int, error) {
	start := (page - 1) * size
	if start > len(s.runs) {
		start = len(s.runs)
	}
	end := start + size
	if end > len(s.runs) {
		end = len(s.runs)
	}
	return append([]platform.Run(nil), s.runs[start:end]...), len(s.runs), nil
}

func TestTaskFindingsFileModePagesPastFiveHundredRuns(t *testing.T) {
	s := newTestServer(t, nil)
	writeScanFixture(t, s.dataDir, "old.example", "2026-05-01", ScanRecord{
		ID: "old-scan", InstanceID: "old-inst", Target: "old.example",
		Status: "finished", StartedAt: "2026-05-01T10:00:00Z",
		Vulns: []VulnSummary{{ID: "old-v", Title: "Old finding", Severity: "high", Endpoint: "/api"}},
	})
	runs := make([]platform.Run, 0, 501)
	for i := 0; i < 500; i++ {
		runs = append(runs, platform.Run{ID: "new-" + strconv.Itoa(i), InstanceID: "new-inst-" + strconv.Itoa(i)})
	}
	// The only run with a scan record is on page 2 (the 501st newest run).
	runs = append(runs, platform.Run{ID: "old-run", InstanceID: "old-inst"})

	findings, err := s.taskFindingsFromScans(context.Background(), &runPageStub{runs: runs}, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Title != "Old finding" {
		t.Fatalf("findings beyond the first 500 runs were dropped: %+v", findings)
	}
}

func TestRetentionRemovesPlatformRunMount(t *testing.T) {
	s := newPlatformTestServer(t)
	ctx := context.Background()
	store := s.currentPlatformStore()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Retention task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "purge-inst", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	writeScanFixture(t, s.dataDir, "old.example", "2020-01-01", ScanRecord{
		ID: "purge-scan", InstanceID: "purge-inst", Target: "old.example",
		Status: "finished", StartedAt: "2020-01-01T10:00:00Z", FinishedAt: "2020-01-01T11:00:00Z",
	})
	if removed := s.pruneExpiredScans(30); removed != 1 {
		t.Fatalf("pruneExpiredScans = %d, want 1", removed)
	}
	runs, err := store.ListRunsByProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("purged scan left %d platform run mount(s)", len(runs))
	}
}

func TestManualScanDeleteRemovesPlatformMount(t *testing.T) {
	s := newPlatformTestServer(t)
	ctx := context.Background()
	store := s.currentPlatformStore()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Delete task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "manual-inst", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	writeScanFixture(t, s.dataDir, "manual.example", "2026-05-01", ScanRecord{
		ID: "manual-scan", InstanceID: "manual-inst", Target: "manual.example",
		Status: "finished", StartedAt: "2026-05-01T10:00:00Z",
	})
	rr := httptest.NewRecorder()
	s.handleGetScan(rr, httptest.NewRequest(http.MethodDelete, "/api/scans/manual-scan", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE scan = %d; body=%s", rr.Code, rr.Body.String())
	}
	runs, err := store.ListRunsByProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("manual scan delete left %d run mount(s)", len(runs))
	}
}

func TestLegacyImportDoesNotRemountModernInstance(t *testing.T) {
	s := newPlatformTestServer(t)
	ctx := context.Background()
	store := s.currentPlatformStore()
	project, err := store.CreateProject(ctx, "Modern Project", platform.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Modern task"})
	if err != nil {
		t.Fatal(err)
	}
	modern, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "modern-inst", Mode: "single"})
	if err != nil {
		t.Fatal(err)
	}
	writeScanFixture(t, s.dataDir, "modern.example", "2026-05-01", ScanRecord{
		ID: "modern-scan", InstanceID: "modern-inst", Target: "modern.example",
		Status: "finished", StartedAt: "2026-05-01T10:00:00Z",
	})
	s.importLegacyPlatformRecords()

	runs, err := store.RunsByInstanceID(ctx, "modern-inst")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != modern.ID || runs[0].TaskID != task.ID {
		t.Fatalf("modern run was remounted by legacy import: %+v", runs)
	}
}

func TestProjectedRunFindingsKeepAcceptedPayload(t *testing.T) {
	rec := &ScanRecord{
		ID: "scan-1", Target: "example.com", StartedAt: time.Now().UTC().Format(time.RFC3339),
		Vulns: []VulnSummary{{
			ID: "v1", Title: "Auth bypass", Severity: "high", Endpoint: "/admin",
			Verified: true,
		}},
	}
	items := projectedRunFindings(rec)
	if len(items) != 1 || items[0].DedupKey != dedupFindingKey(rec.Target, rec.Vulns[0]) {
		t.Fatalf("projection = %+v", items)
	}
	if string(items[0].Finding) == "" || items[0].ScanStartedAt.IsZero() {
		t.Fatalf("projection lost payload/started_at: %+v", items[0])
	}
}
