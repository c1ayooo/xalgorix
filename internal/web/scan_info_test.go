package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

type fakeInfoReader struct {
	regionQ    infoitem.RegionQuery
	contextQ   infoitem.ContextQuery
	assocQ     infoitem.AssociationQuery
	assocCalls []infoitem.AssociationQuery
	regionRes  infoitem.RegionResult
	contextRes infoitem.ContextResult
	assocRes   infoitem.AssociationResult
	err        error
}

func (f *fakeInfoReader) Region(_ context.Context, q infoitem.RegionQuery) (infoitem.RegionResult, error) {
	f.regionQ = q
	return f.regionRes, f.err
}

func (f *fakeInfoReader) Associate(_ context.Context, q infoitem.AssociationQuery) (infoitem.AssociationResult, error) {
	f.assocCalls = append(f.assocCalls, q)
	f.assocQ = q
	return f.assocRes, f.err
}

func (f *fakeInfoReader) ContextSlice(_ context.Context, q infoitem.ContextQuery) (infoitem.ContextResult, error) {
	f.contextQ = q
	return f.contextRes, f.err
}

func (f *fakeInfoReader) GetByID(_ context.Context, _ string, _ string) (infoitem.Item, error) {
	if f.err != nil {
		return infoitem.Item{}, f.err
	}
	return infoitem.Item{}, infoitem.ErrNotFound
}

func infoTestServer(t *testing.T) (*Server, *platform.Project, *platform.Project) {
	t.Helper()
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()
	projectA, err := store.CreateProject(ctx, "Info A", platform.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := store.CreateProject(ctx, "Info B", platform.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	for project, inst := range map[string]string{projectA.ID: "inst-a", projectB.ID: "inst-b"} {
		task, err := store.CreateTask(ctx, project, platform.TaskInput{Name: "Info task"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: inst, Mode: "single"}); err != nil {
			t.Fatal(err)
		}
	}
	return s, projectA, projectB
}

func infoRequest(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleInfoRoutes(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func TestInfoRegionUsesProjectScanScopeAndPassesGap(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeInfoReader{regionRes: infoitem.RegionResult{
		Items:     []infoitem.Item{{ID: "i1", ScanID: "inst-a"}},
		Truncated: true, Incomplete: true, GapReason: "watermark pending",
	}}
	s.SetInfoReader(reader)
	rr := infoRequest(t, s, "/api/info/region?project_id="+projectA.ID+"&subject=api.example&limit=10")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.regionQ.ScanIDs) != 1 || reader.regionQ.ScanIDs[0] != "inst-a" {
		t.Fatalf("project scope passed to reader = %v", reader.regionQ.ScanIDs)
	}
	var body struct {
		Available  bool   `json:"available"`
		Truncated  bool   `json:"truncated"`
		Incomplete bool   `json:"incomplete"`
		GapReason  string `json:"gap_reason"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || !body.Truncated || !body.Incomplete || body.GapReason != "watermark pending" {
		t.Fatalf("response = %+v", body)
	}
}

// The region cursor is a keyset position, not a time filter: it must reach the
// reader parsed, and has_more/next_cursor must pass through to the dashboard.
func TestInfoRegionCursorPassthroughAndPagingFields(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeInfoReader{regionRes: infoitem.RegionResult{
		Items:      []infoitem.Item{{ID: "i1", ScanID: "inst-a"}},
		Truncated:  true,
		HasMore:    true,
		NextCursor: "2026-11-22T10:00:00Z|i1",
	}}
	s.SetInfoReader(reader)
	rr := infoRequest(t, s, "/api/info/region?project_id="+projectA.ID+
		"&cursor=2026-11-22T10:00:00Z%7Cii-9&limit=10")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if reader.regionQ.Cursor == nil || reader.regionQ.Cursor.ID != "ii-9" {
		t.Fatalf("cursor passed to reader = %+v", reader.regionQ.Cursor)
	}
	if got := reader.regionQ.Cursor.ObservedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-11-22T10:00:00Z" {
		t.Fatalf("cursor observed_at = %s", got)
	}
	var body struct {
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.HasMore || body.NextCursor != "2026-11-22T10:00:00Z|i1" {
		t.Fatalf("paging fields = %+v", body)
	}
}

// A malformed cursor is a client error (400) and must never reach the reader,
// otherwise a typo would look like a cold-path outage.
func TestInfoRegionInvalidCursorIsBadRequest(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeInfoReader{}
	s.SetInfoReader(reader)
	rr := infoRequest(t, s, "/api/info/region?project_id="+projectA.ID+"&cursor=not-a-cursor")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor = %d body=%s", rr.Code, rr.Body.String())
	}
	if reader.regionQ.Limit != 0 || reader.regionQ.Cursor != nil {
		t.Fatalf("reader called for invalid cursor: %+v", reader.regionQ)
	}
}

func TestInfoContextRejectsCrossProjectScan(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeInfoReader{}
	s.SetInfoReader(reader)
	rr := infoRequest(t, s, "/api/info/context?project_id="+projectA.ID+"&scan_id=inst-b")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-project scan = %d body=%s", rr.Code, rr.Body.String())
	}
	if reader.contextQ.ScanID != "" {
		t.Fatalf("reader called for cross-project scan: %+v", reader.contextQ)
	}
}

// Empty result sets marshal as [] not null: the dashboard iterates items/hits
// unconditionally, and a nil slice from any Reader implementation (PG cold
// path returns nil on zero rows) would crash the page.
func TestInfoEmptyResultSetsMarshalAsEmptyArrays(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	s.SetInfoReader(&fakeInfoReader{}) // zero values: nil Items / nil Hits

	rr := infoRequest(t, s, "/api/info/region?project_id="+projectA.ID)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `"items":null`) {
		t.Fatalf("region empty body = %d %s", rr.Code, rr.Body.String())
	}
	rr = infoRequest(t, s, "/api/info/context?project_id="+projectA.ID+"&scan_id=inst-a")
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `"items":null`) {
		t.Fatalf("context empty body = %d %s", rr.Code, rr.Body.String())
	}
	rr = infoRequest(t, s, "/api/info/association?project_id="+projectA.ID+"&ids=i1")
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `"hits":null`) {
		t.Fatalf("association empty body = %d %s", rr.Code, rr.Body.String())
	}
}

func TestInfoDisabledIsExplicit(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	s.SetInfoReader(&fakeInfoReader{err: errors.New("infoitem/postgres: store unavailable")})
	rr := infoRequest(t, s, "/api/info/region?project_id="+projectA.ID)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled code = %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["available"] != false || body["reason"] != "cold_path_unavailable" {
		t.Fatalf("disabled body = %+v", body)
	}
}

func TestInfoAssociationValidatesProjectAndPassesFilters(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeInfoReader{assocRes: infoitem.AssociationResult{
		Hits: []infoitem.AssociationHit{{Item: infoitem.Item{ID: "i2", ScanID: "inst-a"}, Path: []string{"i1", "i2"}, Depth: 1}},
	}}
	s.SetInfoReader(reader)
	rr := infoRequest(t, s, "/api/info/association?project_id="+projectA.ID+
		"&scan_id=inst-a&ids=i1,i2&edge_types=supports&max_depth=2&limit=5")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if reader.assocQ.ScanID != "inst-a" || len(reader.assocQ.StartIDs) != 2 ||
		reader.assocQ.MaxDepth != 2 || reader.assocQ.Limit != 5 ||
		len(reader.assocQ.EdgeTypes) != 1 || reader.assocQ.EdgeTypes[0] != infoitem.RelationSupports {
		t.Fatalf("association query = %+v", reader.assocQ)
	}
	// Single-scan responses keep their contract shape: no aggregation fields.
	if strings.Contains(rr.Body.String(), "scans_consulted") {
		t.Fatalf("single-scan response leaks scans_consulted: %s", rr.Body.String())
	}
}

func TestInfoAssociationRejectsCrossProjectScan(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeInfoReader{}
	s.SetInfoReader(reader)
	rr := infoRequest(t, s, "/api/info/association?project_id="+projectA.ID+"&scan_id=inst-b&ids=i1")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-project scan = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.assocCalls) != 0 {
		t.Fatalf("reader called for cross-project scan: %+v", reader.assocCalls)
	}
}

// A project with two runs: the project-wide form queries both whitelisted
// scans and merges their hits on the item id dimension.
func TestInfoAssociationProjectFormAggregatesWhitelistedScans(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()
	task, err := store.CreateTask(ctx, projectA.ID, platform.TaskInput{Name: "Second run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-a2", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	reader := &fakeInfoReader{assocRes: infoitem.AssociationResult{
		// Same hit id returned by both scans — the merge must dedupe it.
		Hits: []infoitem.AssociationHit{{Item: infoitem.Item{ID: "i2"}, Path: []string{"i1", "i2"}, Depth: 1}},
	}}
	s.SetInfoReader(reader)
	rr := infoRequest(t, s, "/api/info/association?project_id="+projectA.ID+"&ids=i1&max_depth=3&limit=10")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.assocCalls) != 2 {
		t.Fatalf("associate calls = %d (%+v)", len(reader.assocCalls), reader.assocCalls)
	}
	for _, q := range reader.assocCalls {
		if q.ScanID != "inst-a" && q.ScanID != "inst-a2" {
			t.Fatalf("query outside project whitelist: %+v", q)
		}
		if q.MaxDepth != 3 || q.Limit != 10 || len(q.StartIDs) != 1 || q.StartIDs[0] != "i1" {
			t.Fatalf("bounded traversal params not preserved: %+v", q)
		}
	}
	var body struct {
		Available      bool `json:"available"`
		ScansConsulted int  `json:"scans_consulted"`
		Hits           []struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || body.ScansConsulted != 2 {
		t.Fatalf("response = %+v", body)
	}
	if len(body.Hits) != 1 || body.Hits[0].Item.ID != "i2" {
		t.Fatalf("merged hits not deduped on item id: %+v", body.Hits)
	}
}

func TestInfoAssociationProjectFormRequiresStartIDs(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeInfoReader{}
	s.SetInfoReader(reader)
	rr := infoRequest(t, s, "/api/info/association?project_id="+projectA.ID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing ids = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.assocCalls) != 0 {
		t.Fatalf("reader called without start ids: %+v", reader.assocCalls)
	}
}

// The aggregation fan-out is capped so one request cannot turn into one
// cold-path query per run of a large project; the cap is disclosed via
// truncated=true and scans_consulted.
func TestInfoAssociationProjectFormCapsScanFanOut(t *testing.T) {
	s, projectB, _ := infoTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()
	task, err := store.CreateTask(ctx, projectB.ID, platform.TaskInput{Name: "Many runs"})
	if err != nil {
		t.Fatal(err)
	}
	const totalRuns = 25
	for i := 0; i < totalRuns; i++ {
		id := fmt.Sprintf("inst-b-%02d", i)
		if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: id, Mode: "single"}); err != nil {
			t.Fatal(err)
		}
	}
	reader := &fakeInfoReader{}
	s.SetInfoReader(reader)
	rr := infoRequest(t, s, "/api/info/association?project_id="+projectB.ID+"&ids=i1")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.assocCalls) != maxProjectAssociationScans {
		t.Fatalf("associate calls = %d, want cap %d", len(reader.assocCalls), maxProjectAssociationScans)
	}
	var body struct {
		Truncated      bool `json:"truncated"`
		ScansConsulted int  `json:"scans_consulted"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Truncated || body.ScansConsulted != maxProjectAssociationScans {
		t.Fatalf("cap disclosure = %+v", body)
	}
}

// taskNarrowFixture builds one project with two tasks (one run each) plus a
// foreign project with its own task, for task_id narrowing tests.
func taskNarrowFixture(t *testing.T) (*Server, *platform.Project, *platform.Task, *platform.Task, *platform.Task) {
	t.Helper()
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Task Narrow", platform.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateProject(ctx, "Task Narrow Other", platform.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	taskOne, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "One"})
	if err != nil {
		t.Fatal(err)
	}
	taskTwo, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Two"})
	if err != nil {
		t.Fatal(err)
	}
	foreignTask, err := store.CreateTask(ctx, other.ID, platform.TaskInput{Name: "Foreign"})
	if err != nil {
		t.Fatal(err)
	}
	for task, inst := range map[string]string{taskOne.ID: "inst-n1", taskTwo.ID: "inst-n2"} {
		if _, err := store.CreateRun(ctx, task, platform.RunMount{InstanceID: inst, Mode: "single"}); err != nil {
			t.Fatal(err)
		}
	}
	return s, project, taskOne, taskTwo, foreignTask
}

func TestInfoRegionTaskIDNarrowsWhitelist(t *testing.T) {
	s, project, taskOne, _, foreignTask := taskNarrowFixture(t)
	reader := &fakeInfoReader{}
	s.SetInfoReader(reader)

	// task_id narrows the whitelist to that task's run scan ids.
	rr := infoRequest(t, s, "/api/info/region?project_id="+project.ID+"&task_id="+taskOne.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.regionQ.ScanIDs) != 1 || reader.regionQ.ScanIDs[0] != "inst-n1" {
		t.Fatalf("task scope passed to reader = %v", reader.regionQ.ScanIDs)
	}

	// Without task_id the project whitelist is unchanged.
	if rr := infoRequest(t, s, "/api/info/region?project_id="+project.ID); rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.regionQ.ScanIDs) != 2 {
		t.Fatalf("project scope = %v, want both tasks", reader.regionQ.ScanIDs)
	}

	// A task of another project is 404, never an empty cross-project view.
	if rr := infoRequest(t, s, "/api/info/region?project_id="+project.ID+"&task_id="+foreignTask.ID); rr.Code != http.StatusNotFound {
		t.Fatalf("cross-project task code=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := infoRequest(t, s, "/api/info/region?project_id="+project.ID+"&task_id=missing"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown task code=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestInfoAssociationTaskIDAggregatesOverTaskScans(t *testing.T) {
	s, project, taskOne, _, foreignTask := taskNarrowFixture(t)
	reader := &fakeInfoReader{}
	s.SetInfoReader(reader)

	rr := infoRequest(t, s, "/api/info/association?project_id="+project.ID+"&task_id="+taskOne.ID+"&ids=i1")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.assocCalls) != 1 || reader.assocCalls[0].ScanID != "inst-n1" {
		t.Fatalf("association queries outside task scope: %+v", reader.assocCalls)
	}

	// A foreign task must not fan out any query.
	if rr := infoRequest(t, s, "/api/info/association?project_id="+project.ID+"&task_id="+foreignTask.ID+"&ids=i1"); rr.Code != http.StatusNotFound {
		t.Fatalf("cross-project task code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.assocCalls) != 1 {
		t.Fatalf("rejected task fanned out queries: %+v", reader.assocCalls)
	}
}
