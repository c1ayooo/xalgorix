package assets

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryArchiveAndFilter(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	host, _ := m.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindSubdomain, Key: "api.example.com"})
	path, _ := m.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindEndpoint, Key: "/login"})

	if n, err := m.SetAssetsArchived(ctx, "p1", []int64{host.ID}, true, "alice"); err != nil || n != 1 {
		t.Fatalf("archive n=%d err=%v", n, err)
	}
	active, _ := m.ListProjectAssets(ctx, AssetFilter{ProjectID: "p1"})
	if len(active) != 1 || active[0].ID != path.ID {
		t.Fatalf("active = %+v", active)
	}
	all, _ := m.ListProjectAssets(ctx, AssetFilter{ProjectID: "p1", IncludeArchived: true})
	if len(all) != 2 {
		t.Fatalf("include archived = %d", len(all))
	}
	if allHost, err := m.GetAssetDetail(ctx, "p1", host.ID); err != nil || allHost.Asset.ArchivedAt == nil || allHost.Asset.ArchivedBy != "alice" {
		t.Fatalf("archived detail = %+v err=%v", allHost, err)
	}
	if n, err := m.SetAssetsArchived(ctx, "p1", []int64{host.ID}, false, "alice"); err != nil || n != 1 {
		t.Fatalf("restore n=%d err=%v", n, err)
	}
}

func TestMemoryListFilterByTaskAndQuery(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	a, _ := m.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindEndpoint, Key: "/admin"})
	b, _ := m.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindEndpoint, Key: "/login"})
	_, _ = m.UpsertAsset(ctx, AssetInput{ProjectID: "p2", Kind: KindEndpoint, Key: "/admin"})
	_ = m.LinkTaskAsset(ctx, Link{ScanID: "run-1", TaskID: "task-1", AssetID: a.ID, Source: "agent"})
	_ = m.LinkTaskAsset(ctx, Link{ScanID: "run-2", TaskID: "task-2", AssetID: b.ID, Source: "traffic"})

	byTask, _ := m.ListProjectAssets(ctx, AssetFilter{ProjectID: "p1", TaskID: "task-1"})
	if len(byTask) != 1 || byTask[0].ID != a.ID {
		t.Fatalf("by task = %+v", byTask)
	}
	byRun, _ := m.ListProjectAssets(ctx, AssetFilter{ProjectID: "p1", RunID: "run-2"})
	if len(byRun) != 1 || byRun[0].ID != b.ID {
		t.Fatalf("by run = %+v", byRun)
	}
	byQuery, _ := m.ListProjectAssets(ctx, AssetFilter{ProjectID: "p1", Query: "adm"})
	if len(byQuery) != 1 || byQuery[0].ID != a.ID {
		t.Fatalf("by query = %+v", byQuery)
	}
}

func TestMemoryMergeMovesLinksAndEdges(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	source, _ := m.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindSubdomain, Key: "www.example.com"})
	target, _ := m.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindRootDomain, Key: "example.com"})
	other, _ := m.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindEndpoint, Key: "/login"})
	_ = m.LinkTaskAsset(ctx, Link{ScanID: "run-1", TaskID: "task-1", AssetID: source.ID, Source: "traffic"})
	_ = m.AddEdge(ctx, "p1", source.ID, other.ID, "serves")
	_ = m.AddEdge(ctx, "p1", target.ID, other.ID, "serves") // duplicate after rewrite

	if err := m.MergeAssets(ctx, "p1", source.ID, target.ID, "alice"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	detail, err := m.GetAssetDetail(ctx, "p1", target.ID)
	if err != nil {
		t.Fatalf("target detail: %v", err)
	}
	if len(detail.Links) != 1 || detail.Links[0].ScanID != "run-1" {
		t.Fatalf("target links = %+v", detail.Links)
	}
	edges, _ := m.ListEdges(ctx, "p1")
	if len(edges) != 1 || edges[0].SrcID != target.ID {
		t.Fatalf("edges after merge = %+v", edges)
	}
	srcDetail, _ := m.GetAssetDetail(ctx, "p1", source.ID)
	if srcDetail.Asset.MergedIntoID == nil || *srcDetail.Asset.MergedIntoID != target.ID {
		t.Fatalf("source tombstone = %+v", srcDetail.Asset)
	}
	if srcDetail.Asset.ArchivedAt == nil {
		t.Fatal("merged source must be archived")
	}
	active, _ := m.ListProjectAssets(ctx, AssetFilter{ProjectID: "p1"})
	for _, a := range active {
		if a.ID == source.ID {
			t.Fatalf("merged source still listed active: %+v", active)
		}
	}
	// A merged tombstone cannot be restored independently.
	if n, _ := m.SetAssetsArchived(ctx, "p1", []int64{source.ID}, false, "alice"); n != 0 {
		t.Fatalf("restored merged source, n=%d", n)
	}
}

func TestMemoryMergeRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	a, _ := m.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindEndpoint, Key: "/a"})
	if err := m.MergeAssets(ctx, "p1", a.ID, a.ID, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("self merge err = %v", err)
	}
	if err := m.MergeAssets(ctx, "p1", a.ID, 999, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target err = %v", err)
	}
}
