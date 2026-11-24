package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/assets"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func newProjectAssetServer(t *testing.T) (*Server, platform.Repository, string) {
	t.Helper()
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	project, err := store.CreateProject(context.Background(), "Asset Project", platform.Scope{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	repo := assets.NewMemoryStore()
	assets.SetBackend(repo)
	t.Cleanup(func() { assets.SetBackend(nil) })
	return s, store, project.ID
}

func assetRequest(t *testing.T, s *Server, store platform.Repository, projectID, method, suffix string, body any) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/projects/" + projectID + "/assets" + suffix
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	pathOnly := suffix
	if i := strings.IndexByte(pathOnly, '?'); i >= 0 {
		pathOnly = pathOnly[:i]
	}
	s.handleProjectAssets(rr, req, store, projectID, splitAssetSuffix(pathOnly))
	return rr
}

func splitAssetSuffix(s string) []string {
	if len(s) > 0 && s[0] == '/' {
		s = s[1:]
	}
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '/' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func TestProjectAssetsRecordListDetailAndArchive(t *testing.T) {
	s, store, projectID := newProjectAssetServer(t)
	rr := assetRequest(t, s, store, projectID, http.MethodPost, "", map[string]any{
		"kind": "endpoint", "key": "Users", "display_name": "Users",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("record status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created assets.Asset
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode asset: %v", err)
	}
	if created.Key != "/users" || created.Kind != assets.KindEndpoint {
		t.Fatalf("created = %+v", created)
	}

	rr = assetRequest(t, s, store, projectID, http.MethodGet, "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var list struct {
		Items []assets.Asset `json:"items"`
		Total int            `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}

	rr = assetRequest(t, s, store, projectID, http.MethodGet, fmt.Sprintf("/%d", created.ID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("detail status = %d body=%s", rr.Code, rr.Body.String())
	}
	var detail assets.AssetDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Asset.ID != created.ID {
		t.Fatalf("detail = %+v", detail)
	}

	rr = assetRequest(t, s, store, projectID, http.MethodPost, "/archive", map[string]any{
		"ids": []int64{created.ID}, "archived": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("archive status = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = assetRequest(t, s, store, projectID, http.MethodGet, "", nil)
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if list.Total != 0 {
		t.Fatalf("archived asset still listed: %+v", list)
	}
	rr = assetRequest(t, s, store, projectID, http.MethodGet, "?include_archived=true", nil)
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if list.Total != 1 {
		t.Fatalf("include_archived list = %+v", list)
	}
}

func TestProjectAssetsMergeAndBatch(t *testing.T) {
	s, store, projectID := newProjectAssetServer(t)
	ctx := context.Background()
	repo := assets.Backend()
	source, _ := repo.UpsertAsset(ctx, assets.AssetInput{ProjectID: projectID, Kind: assets.KindSubdomain, Key: "www.example.com"})
	target, _ := repo.UpsertAsset(ctx, assets.AssetInput{ProjectID: projectID, Kind: assets.KindRootDomain, Key: "example.com"})
	other, _ := repo.UpsertAsset(ctx, assets.AssetInput{ProjectID: projectID, Kind: assets.KindEndpoint, Key: "/login"})
	_ = repo.LinkTaskAsset(ctx, assets.Link{ScanID: "run-1", TaskID: "task-1", AssetID: source.ID, Source: "traffic"})

	rr := assetRequest(t, s, store, projectID, http.MethodPost, "/merge", map[string]any{
		"source_id": source.ID, "target_id": target.ID,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("merge status = %d body=%s", rr.Code, rr.Body.String())
	}
	detail, err := repo.GetAssetDetail(ctx, projectID, target.ID)
	if err != nil || len(detail.Links) != 1 {
		t.Fatalf("target detail = %+v err=%v", detail, err)
	}

	rr = assetRequest(t, s, store, projectID, http.MethodPost, "/archive", map[string]any{
		"ids": []int64{target.ID, other.ID}, "archived": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("batch archive status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Updated int `json:"updated"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.Updated != 2 {
		t.Fatalf("updated = %d", body.Updated)
	}
}

func TestProjectAssetEdgesListIsDeterministic(t *testing.T) {
	s, store, projectID := newProjectAssetServer(t)
	ctx := context.Background()
	repo := assets.Backend()
	host, err := repo.UpsertAsset(ctx, assets.AssetInput{ProjectID: projectID, Kind: assets.KindSubdomain, Key: "api.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	path, err := repo.UpsertAsset(ctx, assets.AssetInput{ProjectID: projectID, Kind: assets.KindEndpoint, Key: "/users"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddEdge(ctx, projectID, host.ID, path.ID, assets.RelExposes); err != nil {
		t.Fatal(err)
	}

	rr := assetRequest(t, s, store, projectID, http.MethodGet, "/edges", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("edges status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		ProjectID string        `json:"project_id"`
		Edges     []assets.Edge `json:"edges"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ProjectID != projectID || len(body.Edges) != 1 {
		t.Fatalf("edges body = %+v", body)
	}
	if body.Edges[0].SrcID != host.ID || body.Edges[0].DstID != path.ID || body.Edges[0].Rel != assets.RelExposes {
		t.Fatalf("edge = %+v", body.Edges[0])
	}

	rr = assetRequest(t, s, store, projectID, http.MethodPost, "/edges", nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST edges status = %d", rr.Code)
	}
}

func TestProjectAssetsRouteDispatchesThroughProjectDetail(t *testing.T) {
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	project, err := store.CreateProject(context.Background(), "Routed", platform.Scope{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	repo := assets.NewMemoryStore()
	assets.SetBackend(repo)
	t.Cleanup(func() { assets.SetBackend(nil) })

	rr := doJSON(t, s, http.MethodPost, "/api/projects/"+project.ID+"/assets",
		`{"kind":"endpoint","key":"/routed"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("route record status = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = doJSON(t, s, http.MethodGet, "/api/projects/"+project.ID+"/assets", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("route list status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestProjectAssetsUnknownProjectAndAsset(t *testing.T) {
	s, store, _ := newProjectAssetServer(t)
	rr := assetRequest(t, s, store, "missing-project", http.MethodGet, "", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown project status = %d body=%s", rr.Code, rr.Body.String())
	}
	project, _ := store.CreateProject(context.Background(), "P2", platform.Scope{})
	rr = assetRequest(t, s, store, project.ID, http.MethodGet, "/999", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown asset status = %d body=%s", rr.Code, rr.Body.String())
	}
}
