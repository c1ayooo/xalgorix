package assetobs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/assets"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

type fakeRepo struct {
	locators []assets.LocatorInput
	mu       sync.Mutex
	upserts  []assets.AssetInput
	links    []assets.Link
	edges    []assets.Edge
	wrote    chan struct{}
}

func newFakeRepo() *fakeRepo { return &fakeRepo{wrote: make(chan struct{}, 16)} }

func (f *fakeRepo) UpsertAsset(_ context.Context, in assets.AssetInput) (*assets.Asset, error) {
	f.mu.Lock()
	f.upserts = append(f.upserts, in)
	id := int64(len(f.upserts))
	f.mu.Unlock()
	f.wrote <- struct{}{}
	return &assets.Asset{ID: id, Kind: in.Kind, Key: in.Key}, nil
}
func (f *fakeRepo) LinkTaskAsset(_ context.Context, in assets.Link) error {
	f.mu.Lock()
	f.links = append(f.links, in)
	f.mu.Unlock()
	return nil
}
func (f *fakeRepo) AddEdge(_ context.Context, _ string, srcID, dstID int64, rel string) error {
	f.mu.Lock()
	f.edges = append(f.edges, assets.Edge{SrcID: srcID, DstID: dstID, Rel: rel})
	f.mu.Unlock()
	return nil
}
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locators = append(f.locators, in)
	return &assets.Locator{ID: int64(len(f.locators)), ProjectID: in.ProjectID, AssetID: in.AssetID, Locator: in.Locator}, nil
}
func (f *fakeRepo) ListAssetLocators(context.Context, string, int64) ([]assets.Locator, error) {
	return nil, nil
}
func (f *fakeRepo) MergeAssets(context.Context, string, int64, int64, string) error { return nil }
func (f *fakeRepo) SetAssetsArchived(context.Context, string, []int64, bool, string) (int, error) {
	return 0, nil
}
func (f *fakeRepo) Close() error { return nil }

func TestIngestURLDeduplicatesPerSession(t *testing.T) {
	repo := newFakeRepo()
	assets.SetBackend(repo)
	t.Cleanup(func() { assets.SetBackend(nil) })

	sc := scanctx.New("scan-assetobs", t.TempDir())
	sc.ProjectID = "p1"
	sc.TaskID = "task-1"
	sc.SetInstanceID("inst-1")

	IngestURL(sc, "ii-1", "https://api.example.com/a", "traffic_observed")
	IngestURL(sc, "ii-2", "https://api.example.com/a", "traffic_observed")

	deadline := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-repo.wrote:
		case <-deadline:
			t.Fatalf("timed out after %d writes", i)
		}
	}
	time.Sleep(20 * time.Millisecond) // let any stray goroutine land

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.upserts) != 2 {
		t.Fatalf("upserts = %d, want 2 (host + endpoint, deduped)", len(repo.upserts))
	}
	for _, in := range repo.upserts {
		if in.ProjectID != "p1" {
			t.Fatalf("project id = %q", in.ProjectID)
		}
	}
	if len(repo.links) != 2 || repo.links[0].ScanID != "inst-1" || repo.links[0].TaskID != "task-1" {
		t.Fatalf("links = %+v", repo.links)
	}
}

func TestIngestURLRequiresProjectScope(t *testing.T) {
	repo := newFakeRepo()
	assets.SetBackend(repo)
	t.Cleanup(func() { assets.SetBackend(nil) })
	sc := scanctx.New("scan-noscope", t.TempDir())
	IngestURL(sc, "ii-1", "https://api.example.com/a", "traffic_observed")
	select {
	case <-repo.wrote:
		t.Fatal("no-project runs must not write assets")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestIngestURLRecordsHostExposesPathEdge(t *testing.T) {
	repo := newFakeRepo()
	assets.SetBackend(repo)
	t.Cleanup(func() { assets.SetBackend(nil) })

	sc := scanctx.New("scan-asset-edge", t.TempDir())
	sc.ProjectID = "p1"
	sc.SetInstanceID("inst-1")

	IngestURL(sc, "ii-1", "https://api.example.com/users", "traffic_observed")

	deadline := time.Now().Add(2 * time.Second)
	for {
		repo.mu.Lock()
		edges := append([]assets.Edge(nil), repo.edges...)
		upserts := len(repo.upserts)
		repo.mu.Unlock()
		if len(edges) == 1 {
			if upserts != 2 {
				t.Fatalf("upserts = %d, want host + endpoint", upserts)
			}
			// InputsFromURL yields host first, path second; the fake assigns
			// ids in that order.
			if edges[0].SrcID != 1 || edges[0].DstID != 2 || edges[0].Rel != assets.RelExposes {
				t.Fatalf("edge = %+v", edges[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for exposes edge; edges=%+v", edges)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
