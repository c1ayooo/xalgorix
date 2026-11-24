package assetgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/assets"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

type fakeRepo struct {
	upserts []assets.AssetInput
	links   []assets.Link
}

func (f *fakeRepo) UpsertAsset(_ context.Context, in assets.AssetInput) (*assets.Asset, error) {
	f.upserts = append(f.upserts, in)
	return &assets.Asset{ID: 7, Kind: in.Kind, Key: in.Key}, nil
}
func (f *fakeRepo) LinkTaskAsset(_ context.Context, in assets.Link) error {
	f.links = append(f.links, in)
	return nil
}
func (f *fakeRepo) AddEdge(context.Context, string, int64, int64, string) error { return nil }
func (f *fakeRepo) ListAssets(context.Context, string, ...assets.Kind) ([]assets.Asset, error) {
	return nil, nil
}
func (f *fakeRepo) ListTaskAssets(context.Context, string) ([]assets.Asset, error) { return nil, nil }
func (f *fakeRepo) ListEdges(context.Context, string) ([]assets.Edge, error)       { return nil, nil }
func (f *fakeRepo) ListProjectAssets(context.Context, assets.AssetFilter) ([]assets.Asset, error) {
	return nil, nil
}
func (f *fakeRepo) GetAssetDetail(context.Context, string, int64) (assets.AssetDetail, error) {
	return assets.AssetDetail{}, nil
}
func (f *fakeRepo) RecordLocator(_ context.Context, in assets.LocatorInput) (*assets.Locator, error) {
	return &assets.Locator{ID: 1, ProjectID: in.ProjectID, AssetID: in.AssetID, Locator: in.Locator}, nil
}
func (f *fakeRepo) ListAssetLocators(context.Context, string, int64) ([]assets.Locator, error) {
	return nil, nil
}
func (f *fakeRepo) MergeAssets(context.Context, string, int64, int64, string) error { return nil }
func (f *fakeRepo) SetAssetsArchived(context.Context, string, []int64, bool, string) (int, error) {
	return 0, nil
}
func (f *fakeRepo) Close() error { return nil }

func TestRecordAssetWritesProjectScopedIdentity(t *testing.T) {
	repo := &fakeRepo{}
	assets.SetBackend(repo)
	t.Cleanup(func() { assets.SetBackend(nil) })

	sc := scanctx.New("scan-record-asset", t.TempDir())
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(sc.ID) })
	sc.ProjectID = "p1"
	sc.TaskID = "task-1"
	sc.SetInstanceID("inst-1")

	r := tools.NewRegistry()
	r.SetScanContextID(sc.ID)
	res, err := record(r, map[string]string{
		"kind": "endpoint", "key": "Users", "info_item_id": "ii-1",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "endpoint /users") {
		t.Fatalf("output = %q", res.Output)
	}
	if len(repo.upserts) != 1 {
		t.Fatalf("upserts = %d", len(repo.upserts))
	}
	if repo.upserts[0].ProjectID != "p1" || repo.upserts[0].Key != "/users" || repo.upserts[0].InfoItemID != "ii-1" {
		t.Fatalf("upsert = %+v", repo.upserts[0])
	}
	if len(repo.links) != 1 || repo.links[0].ScanID != "inst-1" || repo.links[0].Source != "agent_recorded" {
		t.Fatalf("link = %+v", repo.links)
	}
}

func TestRecordAssetRequiresProjectScope(t *testing.T) {
	repo := &fakeRepo{}
	assets.SetBackend(repo)
	t.Cleanup(func() { assets.SetBackend(nil) })
	sc := scanctx.New("scan-no-project", t.TempDir())
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(sc.ID) })
	r := tools.NewRegistry()
	r.SetScanContextID(sc.ID)

	res, err := record(r, map[string]string{"kind": "endpoint", "key": "/x"})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "no project scope") {
		t.Fatalf("error = %q", res.Error)
	}
	if len(repo.upserts) != 0 {
		t.Fatalf("no-project run wrote %d assets", len(repo.upserts))
	}
}
