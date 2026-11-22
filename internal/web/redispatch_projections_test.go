package web

// Regression loop for the 1:1 task:run re-dispatch (续跑, REDESIGN D11): one
// run mount keyed by instance "resume-inst" owns TWO top-level scan records —
// the finished first session (journals/ledger/intel/findings on disk) and the
// resumed second session (fresh dir, already writing). Every task-detail
// dashboard surface must project the run's WHOLE sibling history, not just
// whichever single scan dir its resolver happens to land on.
//
// Fixture naming mirrors redispatchSiblingFixtures: the alphabetically-first
// sibling is the newest, so first-wins directory pickers deterministically
// land on the empty resumed session — the exact shape that blanks the boards.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

func writeSiblingFile(t *testing.T, dir, name, content string) {
	t.Helper()
	// Journals are append-only and always newline-terminated (walkers read
	// line-by-line), so the fixture mirrors that shape.
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resumedRunFixtures mounts one task/run (instance "resume-inst") with two
// sibling scan records and returns (taskID, projectID).
func resumedRunFixtures(t *testing.T, s *Server) (string, string) {
	t.Helper()
	ctx := context.Background()
	store := s.currentPlatformStore()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{
		Name: "Resumed task",
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
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "resume-inst", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	// The first session linked its scan id (scan_session.go SetRunLegacyScanID,
	// fill-once); the resumed second session reuses the instance and gets no
	// linkage — the identity shape every projection below must survive.
	if err := store.SetRunLegacyScanID(ctx, "resume-inst", "zzz-old-session"); err != nil {
		t.Fatal(err)
	}

	// Finished first session: full history on disk. Coverage counters show a
	// partially completed plan; the resumed session re-plans from zero.
	oldDir := writeScanFixture(t, s.dataDir, "resume.example", "2026-05-01", ScanRecord{
		ID: "zzz-old-session", InstanceID: "resume-inst", Target: "resume.example",
		Status: "finished", StartedAt: "2026-05-01T18:00:00Z", EventsJournal: true,
		PlanTasksTotal: 10, PlanTasksCompleted: 4,
		Vulns: []VulnSummary{{ID: "v1", Title: "Old SQLi", Severity: "high", Endpoint: "/old"}},
	})
	writeSiblingFile(t, oldDir, "events.jsonl", strings.Join([]string{
		`{"type":"message","content":"OLD session: enumerated the admin panel","timestamp":"2026-05-01T18:05:00Z"}`,
		`{"type":"tool_call","tool_name":"http_request","content":"GET /admin","timestamp":"2026-05-01T18:06:00Z"}`,
		`{"type":"tool_result","output":"⛔ OUT-OF-SCOPE TARGET BLOCKED: http://internal.example is not in scope","timestamp":"2026-05-01T18:07:00Z"}`,
	}, "\n"))
	writeSiblingFile(t, oldDir, "domain_journal.jsonl", strings.Join([]string{
		`{"scan_id":"zzz-old-session","seq":1,"at":"2026-05-01T18:05:30Z","actor":{"kind":"engine"},"operation":"claim_hypothesis","hypothesis_id":"H-1","summary":"admin panel exists"}`,
		`{"scan_id":"zzz-old-session","seq":2,"at":"2026-05-01T18:06:30Z","actor":{"kind":"engine"},"operation":"add_evidence","hypothesis_id":"H-1","evidence_id":"E-1","summary":"GET /admin returned 200"}`,
	}, "\n"))
	writeSiblingFile(t, oldDir, "intelligence.json", `{"version":1,"seq":1,"items":[{"id":"i1","fingerprint":"oldfp000000001","kind":"lead","title":"Old admin panel lead","content":"admin panel at /admin","created_at":"2026-05-01T18:07:00Z","updated_at":"2026-05-01T18:07:00Z"}]}`)

	// Resumed second session: fresh dir, already writing conversation events,
	// no domain journal / intelligence snapshot of its own yet.
	newDir := writeScanFixture(t, s.dataDir, "resume.example", "2026-05-01", ScanRecord{
		ID: "aaa-new-session", InstanceID: "resume-inst", Target: "resume.example",
		Status: "running", StartedAt: "2026-05-01T19:00:00Z", EventsJournal: true,
		Vulns: []VulnSummary{},
	})
	writeSiblingFile(t, newDir, "events.jsonl",
		`{"type":"message","content":"NEW session: continuing from the previous transcript","timestamp":"2026-05-01T19:05:00Z"}`)
	return task.ID, project.ID
}

// TestResumedRunDashboardsCarryHistory pins the re-dispatched run's dashboard
// projections to the sibling-merge discipline introduced for findings
// (09d06a3e): 会话 events, 对话 transcript, 探索链路 exploration, and the
// 情报 surfaces must all see both sessions' data while the new one runs.
func TestResumedRunDashboardsCarryHistory(t *testing.T) {
	s := newPlatformTestServer(t)
	taskID, projectID := resumedRunFixtures(t, s)
	store := s.currentPlatformStore()
	ctx := context.Background()

	t.Run("sessions events keep previous session history", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.handleScanEvents(rr, httptest.NewRequest(http.MethodGet, "/api/scans/resume-inst/events", nil))
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
			t.Fatalf("run events total = %d, want 4 (both sessions' journals merged); got %+v", body.Total, body.Events)
		}
		for _, ev := range body.Events {
			if strings.Contains(ev.Content, "OLD session") {
				return // previous session's history survived
			}
		}
		t.Fatalf("previous session's events missing from run events: %+v", body.Events)
	})

	t.Run("transcript shows old and resumed session turns", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.handleTranscript(rr, httptest.NewRequest(http.MethodGet, "/api/conversations/resume-inst/transcript", nil), store, "resume-inst")
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
		s.handleScanExploration(rr, httptest.NewRequest(http.MethodGet, "/api/scans/resume-inst/exploration", nil))
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
		if len(body.Items) != 1 || body.Items[0].Fingerprint != "oldfp000000001" {
			t.Fatalf("task intelligence lost the finished session's items: %+v", body.Items)
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
		if !containsString(reader.regionQ.ScanIDs, "aaa-new-session") {
			t.Fatalf("info region query whitelists %v; the resumed session's scan id (the key the domain journal and InfoItem replica write under) is missing", reader.regionQ.ScanIDs)
		}
		if !containsString(reader.regionQ.ScanIDs, "zzz-old-session") {
			t.Fatalf("info region query whitelists %v; the first session's scan id is missing", reader.regionQ.ScanIDs)
		}
	})

	t.Run("info context accepts resumed session scan id", func(t *testing.T) {
		s.SetInfoReader(&fakeInfoReader{})
		rr := httptest.NewRecorder()
		path := "/api/info/context?project_id=" + projectID + "&scan_id=aaa-new-session"
		s.handleInfoRoutes(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("info context for the resumed session's scan id: code = %d, body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("security chain keeps previous session refusals", func(t *testing.T) {
		rr := httptest.NewRecorder()
		s.handleScanSecurity(rr, httptest.NewRequest(http.MethodGet, "/api/scans/resume-inst/security", nil))
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
