package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/infoitem"
	"github.com/xalgord/xalgorix/v4/internal/platform"
)

type fakeInfoReader struct {
	regionQ    infoitem.RegionQuery
	contextQ   infoitem.ContextQuery
	assocQ     infoitem.AssociationQuery
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
	f.assocQ = q
	return f.assocRes, f.err
}

func (f *fakeInfoReader) ContextSlice(_ context.Context, q infoitem.ContextQuery) (infoitem.ContextResult, error) {
	f.contextQ = q
	return f.contextRes, f.err
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
}
