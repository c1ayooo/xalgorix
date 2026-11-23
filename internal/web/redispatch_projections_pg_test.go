package web

// R6.2 regression loop for the 1:1 task:run re-dispatch (续跑, REDESIGN D11)
// with the PG primary stores registered (REDESIGN D12, charter v3.0): every
// task-detail dashboard surface must project the run's WHOLE sibling history
// from the durable rows — 写 PG → 读 PG. This is the PG twin of
// redispatch_projections_test.go (nil-backend file mode, no DSN required);
// the nine subtests assert the same semantics against rows seeded through the
// run-state and platform repositories instead of files:
//
//	sessions events / conversation transcript / exploration chain /
//	broadcast board (= exploration stream) / security chain / ledger /
//	intelligence / overview objective (+ the findings control).
//
// Skipped unless XALGORIX_TEST_DATABASE_URL is set (the same gate as the
// runstate and platform integration tests; CI adds the PG service with the
// R6 batches). Fixture identity is unique per run and cleaned up by project
// cascade + direct state-row deletes, so the shared test database stays tidy.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestResumedRunDashboardsCarryHistoryFromPG(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}

	// ── unique fixture identity: the test database is shared ───────────────
	suffix := strings.ToLower(strings.ReplaceAll(t.Name(), "Test", "")) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	instID := "resume-inst-" + suffix
	oldID := "zzz-old-session-" + suffix
	newID := "aaa-new-session-" + suffix

	s := newPlatformTestServer(t)

	// Cleanup pool (raw deletes; the repository's project delete cascades the
	// tasks/runs/findings, the state rows are swept by scan/instance key).
	cpool, err := pgxpool.NewWithConfig(context.Background(), mustPoolConfig(dsn))
	if err != nil {
		t.Fatalf("cleanup pool: %v", err)
	}
	t.Cleanup(cpool.Close)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range []string{oldID, newID} {
			for _, stmt := range []string{
				`DELETE FROM run_events WHERE scan_id = $1`,
				`DELETE FROM run_journal WHERE scan_id = $1`,
				`DELETE FROM run_ledger_snapshots WHERE scan_id = $1`,
				`DELETE FROM run_state_files WHERE scan_id = $1`,
				`DELETE FROM run_state_watermarks WHERE scan_id = $1`,
				`DELETE FROM run_scan_records WHERE scan_id = $1`,
			} {
				_, _ = cpool.Exec(ctx, stmt, id)
			}
		}
		_, _ = cpool.Exec(ctx, `DELETE FROM dispatch_snapshots WHERE instance_id = $1`, instID)
	})

	// ── platform store (PostgreSQL) replaces the test file store ───────────
	store, err := platform.Open(context.Background(), platform.Config{DSN: dsn, DataDir: s.dataDir})
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	if ps, ok := store.(interface{ Close() }); ok {
		t.Cleanup(ps.Close)
	}
	s.SetPlatformStore(store)

	// ── run-state store + the four durable seams (the production wiring) ───
	rs, err := runstate.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open runstate store: %v", err)
	}
	t.Cleanup(rs.Close)
	scanctx.SetLedgerBackend(rs)
	domainjournal.SetBackend(rs)
	intelligence.SetBackend(rs)
	SetEventStateBackend(rs)
	SetScanStateBackend(rs)
	t.Cleanup(func() {
		scanctx.SetLedgerBackend(nil)
		domainjournal.SetBackend(nil)
		intelligence.SetBackend(nil)
		SetEventStateBackend(nil)
		SetScanStateBackend(nil)
	})

	// ── platform rows: project → task → run mount ──────────────────────────
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Resumed PG "+suffix, platform.Scope{Targets: []string{"resume.example"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteProject(ctx, project.ID) })
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{
		Name: "Resumed PG task",
		Objective: &platform.Objective{
			Kind:      platform.ObjectiveKindCoverage,
			Statement: "cover the resumed target",
			Criteria: []platform.Criterion{
				{Kind: "coverage", VerifyBy: "deterministic", Required: true},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: instID, Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	// The first session linked its scan id (fill-once); the re-dispatched
	// session reuses the instance and gets no linkage — the identity shape
	// every projection must survive.
	if err := store.SetRunLegacyScanID(ctx, instID, oldID); err != nil {
		t.Fatal(err)
	}

	// ── scan records via the state repository (the runs-table takeover) ────
	putRecord := func(id, started, status string, planTotal, planDone int, vulns []VulnSummary) {
		t.Helper()
		rec := ScanRecord{
			ID: id, InstanceID: instID, Target: "resume.example",
			Status: status, StartedAt: started, EventsJournal: true,
			PlanTasksTotal: planTotal, PlanTasksCompleted: planDone,
			Vulns: vulns,
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := rs.PutScanRecord(runstate.ScanRecordState{
			ScanID:     id,
			InstanceID: instID,
			ScanDir:    filepath.Join("resume.example", "2026-05-01", id),
			Detail:     raw,
		}); err != nil {
			t.Fatalf("put scan record %s: %v", id, err)
		}
	}
	// The finished first session: partial coverage; the resumed session
	// re-plans from zero.
	putRecord(oldID, "2026-05-01T18:00:00Z", "finished", 10, 4, []VulnSummary{
		{ID: "v1", Title: "Old SQLi", Severity: "high", Endpoint: "/old"},
	})
	putRecord(newID, "2026-05-01T19:00:00Z", "running", 0, 0, []VulnSummary{})

	// ── events rows (the conversation/telemetry journal) ───────────────────
	putEvents := func(scanID string, lines ...string) {
		t.Helper()
		rows := make([]runstate.EventRow, 0, len(lines))
		base := time.Date(2026, 5, 1, 18, 5, 0, 0, time.UTC)
		if scanID != oldID {
			base = time.Date(2026, 5, 1, 19, 5, 0, 0, time.UTC)
		}
		for i, line := range lines {
			rows = append(rows, runstate.EventRow{
				Seq:        uint64(i + 1), //nolint:gosec // bounded line order
				Type:       "",
				OccurredAt: base.Add(time.Duration(i) * time.Minute),
				Payload:    []byte(line),
			})
		}
		if err := rs.PutEvents(ctx, scanID, instID, rows); err != nil {
			t.Fatalf("put events %s: %v", scanID, err)
		}
	}
	putEvents(oldID,
		`{"type":"message","content":"OLD session: enumerated the admin panel","timestamp":"2026-05-01T18:05:00Z"}`,
		`{"type":"tool_call","tool_name":"http_request","content":"GET /admin","timestamp":"2026-05-01T18:06:00Z"}`,
		`{"type":"tool_result","output":"⛔ OUT-OF-SCOPE TARGET BLOCKED: http://internal.example is not in scope","timestamp":"2026-05-01T18:07:00Z"}`,
	)
	putEvents(newID,
		`{"type":"message","content":"NEW session: continuing from the previous transcript","timestamp":"2026-05-01T19:05:00Z"}`,
	)

	// ── domain journal rows (the broadcast board + exploration chain) ──────
	putJournal := func(lines ...string) {
		t.Helper()
		rows := make([]runstate.JournalRow, 0, len(lines))
		for i, line := range lines {
			rows = append(rows, runstate.JournalRow{
				Seq:        uint64(i + 1), //nolint:gosec // bounded line order
				OccurredAt: time.Date(2026, 5, 1, 18, 5, 30, 0, time.UTC).Add(time.Duration(i) * time.Minute),
				Payload:    []byte(line),
			})
		}
		if err := rs.PutJournalRecords(ctx, oldID, instID, rows); err != nil {
			t.Fatalf("put journal: %v", err)
		}
	}
	putJournal(
		`{"scan_id":"`+oldID+`","seq":1,"at":"2026-05-01T18:05:30Z","actor":{"kind":"engine"},"operation":"claim_hypothesis","hypothesis_id":"H-1","summary":"admin panel exists"}`,
		`{"scan_id":"`+oldID+`","seq":2,"at":"2026-05-01T18:06:30Z","actor":{"kind":"engine"},"operation":"add_evidence","hypothesis_id":"H-1","evidence_id":"E-1","summary":"GET /admin returned 200"}`,
	)

	// ── intelligence row (the 情报 snapshot of the finished session) ────────
	intel := `{"version":1,"seq":1,"items":[{"id":"i1","fingerprint":"oldfp` + suffix + `","kind":"lead","title":"Old admin panel lead","content":"admin panel at /admin","created_at":"2026-05-01T18:07:00Z","updated_at":"2026-05-01T18:07:00Z"}]}`
	if err := rs.SaveIntelSnapshot(oldID, instID, 1, []byte(intel)); err != nil {
		t.Fatalf("put intel snapshot: %v", err)
	}

	taskID, projectID := task.ID, project.ID

	t.Run("sessions events keep previous session history", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.handleScanEvents(rr, httptest.NewRequest(http.MethodGet, "/api/scans/"+instID+"/events", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("events code = %d, body=%s", rr.Code, rr.Body.String())
		}
		var body struct {
			Events []WSEvent `json:"events"`
			Total  int       `json:"total"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Total != 4 {
			t.Fatalf("run events total = %d, want 4 (both sessions' durable rows); got %+v", body.Total, body.Events)
		}
		for _, ev := range body.Events {
			if strings.Contains(ev.Content, "OLD session") {
				return // previous session's history survived the re-dispatch
			}
		}
		t.Fatalf("previous session's events missing from run events: %+v", body.Events)
	})

	t.Run("transcript shows old and resumed session turns", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.handleTranscript(rr, httptest.NewRequest(http.MethodGet, "/api/conversations/"+instID+"/transcript", nil), store, instID)
		if rr.Code != http.StatusOK {
			t.Fatalf("transcript code = %d, body=%s", rr.Code, rr.Body.String())
		}
		var body struct {
			Items []transcriptItem `json:"items"`
			Total int              `json:"total"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var sawOld, sawNew bool
		for _, it := range body.Items {
			if strings.Contains(it.Text, "OLD session") {
				sawOld = true
			}
			if strings.Contains(it.Text, "NEW session") {
				sawNew = true
			}
		}
		if !sawOld || !sawNew {
			t.Fatalf("transcript lost a session's turns (old=%v new=%v): %+v", sawOld, sawNew, body.Items)
		}
	})

	t.Run("exploration keeps previous session chain", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.handleScanExploration(rr, httptest.NewRequest(http.MethodGet, "/api/scans/"+instID+"/exploration", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("exploration code = %d, body=%s", rr.Code, rr.Body.String())
		}
		var view explorationView
		if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if !view.JournalPresent {
			t.Fatalf("exploration lost the run's domain journal (note=%q, nodes=%d)", view.Note, len(view.Nodes))
		}
		var sawHyp, sawEv bool
		for _, n := range view.Nodes {
			switch n.ID {
			case "hyp:H-1":
				sawHyp = true
			case "ev:E-1":
				sawEv = true
			}
		}
		if !sawHyp || !sawEv {
			t.Fatalf("exploration lost the previous session's chain nodes: %+v", view.Nodes)
		}
	})

	t.Run("task intelligence aggregates sibling snapshots", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.handleTaskIntelligence(rr, httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskID+"/intelligence", nil), store, taskID)
		if rr.Code != http.StatusOK {
			t.Fatalf("task intelligence code = %d, body=%s", rr.Code, rr.Body.String())
		}
		var body struct {
			Items []struct {
				Fingerprint string `json:"fingerprint"`
				Title       string `json:"title"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		want := "oldfp" + suffix
		if len(body.Items) != 1 || body.Items[0].Fingerprint != want {
			t.Fatalf("task intelligence lost the finished session's items: %+v (want fingerprint %s)", body.Items, want)
		}
	})

	t.Run("info region whitelist includes resumed session scan id", func(t *testing.T) {
		reader := &fakeInfoReader{}
		s.SetInfoReader(reader)
		rr := httptest.NewRecorder()
		path := "/api/info/region?project_id=" + projectID + "&task_id=" + taskID
		s.handleInfoRoutes(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("info region code = %d, body=%s", rr.Code, rr.Body.String())
		}
		if !containsString(reader.regionQ.ScanIDs, newID) {
			t.Fatalf("info region query whitelists %v; the resumed session's scan id (the key the domain journal and InfoItem replica write under) is missing", reader.regionQ.ScanIDs)
		}
		if !containsString(reader.regionQ.ScanIDs, oldID) {
			t.Fatalf("info region query whitelists %v; the first session's scan id is missing", reader.regionQ.ScanIDs)
		}
	})

	t.Run("info context accepts resumed session scan id", func(t *testing.T) {
		s.SetInfoReader(&fakeInfoReader{})
		rr := httptest.NewRecorder()
		path := "/api/info/context?project_id=" + projectID + "&scan_id=" + newID
		s.handleInfoRoutes(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("info context for the resumed session's scan id: code = %d, body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("security chain keeps previous session refusals", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.handleScanSecurity(rr, httptest.NewRequest(http.MethodGet, "/api/scans/"+instID+"/security", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("security code = %d, body=%s", rr.Code, rr.Body.String())
		}
		var view scanSecurityView
		if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.Counters["scopeguard"] != 1 || len(view.Blocks) != 1 {
			t.Fatalf("security chain lost the previous session's refusal: counters=%v blocks=%+v", view.Counters, view.Blocks)
		}
	})

	t.Run("objective coverage merges across sessions", func(t *testing.T) {
		task, err := store.GetTask(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		report, err := s.evaluateTaskObjective(ctx, store, task)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Report.Criteria) == 0 {
			t.Fatalf("objective report lost its criteria: %+v", report.Report)
		}
		// The resumed session re-plans from zero (0/10); the merged view must
		// keep the finished session's 4 completed tasks, not reset to 0.
		if !strings.Contains(report.Report.Criteria[0].Reason, "coverage completed 4/10") {
			t.Fatalf("objective coverage reset by the resumed session: %+v", report.Report.Criteria[0])
		}
	})

	t.Run("control: task findings merge siblings", func(t *testing.T) {
		findings, err := s.taskFindingsFromScans(ctx, store, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if len(findings) != 1 || findings[0].Title != "Old SQLi" {
			t.Fatalf("control failed: task findings = %+v, want the finished session's Old SQLi", findings)
		}
	})
}

// mustPoolConfig parses the integration DSN for the cleanup pool.
func mustPoolConfig(dsn string) *pgxpool.Config {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		panic(err) // the caller fails the test on construction errors
	}
	return cfg
}

// TestScanLedgerPanelReadsPGSnapshot pins the ledger panel's R6.2 read
// (REDESIGN D12): with the ledger backend registered, GET /api/scans/{id}/ledger
// serves the durable snapshot row — ledger_present=true, the six status
// buckets zero-filled, hypotheses decoded — even though no ledger.json file
// exists anywhere (the pre-R6.2 file-stat blind spot). A damaged row reports
// damaged honestly instead of folding into a clean empty.
func TestScanLedgerPanelReadsPGSnapshot(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	s := newPlatformTestServer(t)
	// The durable sweep reads the platform store's scan-record set; the test
	// server's file store holds no rows for these seeds, so the PG platform
	// store replaces it (same wiring as the projections twin).
	store, err := platform.Open(context.Background(), platform.Config{DSN: dsn, DataDir: s.dataDir})
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	if ps, ok := store.(interface{ Close() }); ok {
		t.Cleanup(ps.Close)
	}
	s.SetPlatformStore(store)
	rs, err := runstate.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open runstate store: %v", err)
	}
	t.Cleanup(rs.Close)
	scanctx.SetLedgerBackend(rs)
	t.Cleanup(func() { scanctx.SetLedgerBackend(nil) })

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	cpool, err := pgxpool.NewWithConfig(context.Background(), mustPoolConfig(dsn))
	if err != nil {
		t.Fatalf("cleanup pool: %v", err)
	}
	t.Cleanup(cpool.Close)

	ctx := context.Background()
	seed := func(scanID, instanceID, ledgerRow string) {
		t.Helper()
		rec := ScanRecord{
			ID: scanID, InstanceID: instanceID, Target: "ledgerpin.example",
			Status: "finished", StartedAt: "2026-05-02T10:00:00Z", EventsJournal: true,
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := rs.PutScanRecord(runstate.ScanRecordState{
			ScanID:     scanID,
			InstanceID: instanceID,
			ScanDir:    filepath.Join("ledgerpin.example", "2026-05-02", scanID),
			Detail:     raw,
		}); err != nil {
			t.Fatalf("put scan record %s: %v", scanID, err)
		}
		if err := rs.PutLedgerSnapshot(ctx, scanID, instanceID, 3, []byte(ledgerRow)); err != nil {
			t.Fatalf("put ledger snapshot %s: %v", scanID, err)
		}
		t.Cleanup(func() {
			for _, stmt := range []string{
				`DELETE FROM run_ledger_snapshots WHERE scan_id = $1`,
				`DELETE FROM run_scan_records WHERE scan_id = $1`,
			} {
				_, _ = cpool.Exec(ctx, stmt, scanID)
			}
		})
	}
	healthy := `{"seq":3,"hypotheses":[{"id":"H-1","dedup_key":"sqli|/admin","title":"Admin panel takeover","vuln_class":"sqli","status":"queued","confidence":0.6}]}`
	seed("ledger-ok-"+suffix, "ledger-inst-"+suffix, healthy)
	damaged := `{"seq":4,"hypotheses":[`
	seed("ledger-bad-"+suffix, "ledger-inst-"+suffix, damaged)

	t.Run("durable row serves the panel", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.handleScanLedger(rr, httptest.NewRequest(http.MethodGet, "/api/scans/ledger-ok-"+suffix+"/ledger", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("ledger code = %d, body=%s", rr.Code, rr.Body.String())
		}
		var view scanLedgerView
		if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if !view.LedgerPresent {
			t.Fatalf("ledger panel ignored the durable snapshot row: %+v", view)
		}
		if view.LedgerDamaged || len(view.Hypotheses) != 1 || view.Hypotheses[0].ID != "H-1" {
			t.Fatalf("durable snapshot decoded wrong: damaged=%v hyps=%+v", view.LedgerDamaged, view.Hypotheses)
		}
		if view.Counts["queued"] != 1 {
			t.Fatalf("status buckets wrong: %+v", view.Counts)
		}
	})

	t.Run("damaged row stays honest", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.handleScanLedger(rr, httptest.NewRequest(http.MethodGet, "/api/scans/ledger-bad-"+suffix+"/ledger", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("ledger code = %d, body=%s", rr.Code, rr.Body.String())
		}
		var view scanLedgerView
		if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if !view.LedgerPresent || !view.LedgerDamaged || !strings.Contains(view.Note, "does not parse") {
			t.Fatalf("damaged snapshot not reported honestly: present=%v damaged=%v note=%q", view.LedgerPresent, view.LedgerDamaged, view.Note)
		}
	})
}
