package assets

import (
	"context"
	"os"
	"testing"
)

func TestMemoryStore_UpsertLinkAndEdges(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	a, err := store.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindEndpoint, Key: "API/Users", DisplayName: "/API/Users"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if a.Key != "/api/users" {
		t.Fatalf("normalized key = %q", a.Key)
	}
	again, err := store.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindEndpoint, Key: "/api/users", InfoItemID: "ii-1"})
	if err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	if again.ID != a.ID {
		t.Fatalf("dedup failed: %d != %d", again.ID, a.ID)
	}
	if again.LastInfoItemID != "ii-1" {
		t.Fatalf("last info item = %q", again.LastInfoItemID)
	}
	host, _ := store.UpsertAsset(ctx, AssetInput{ProjectID: "p1", Kind: KindSubdomain, Key: "API.Example.com"})
	_ = store.LinkTaskAsset(ctx, Link{ScanID: "run-1", TaskID: "task-1", AssetID: a.ID, Source: "engine_inventory"})
	_ = store.LinkTaskAsset(ctx, Link{ScanID: "run-1", TaskID: "task-1", AssetID: a.ID, Source: "engine_inventory"})
	_ = store.AddEdge(ctx, "p1", host.ID, a.ID, "serves")

	items, err := store.ListTaskAssets(ctx, "run-1")
	if err != nil || len(items) != 1 || items[0].ID != a.ID {
		t.Fatalf("task assets = %+v err=%v", items, err)
	}
	all, _ := store.ListAssets(ctx, "p1", KindEndpoint)
	if len(all) != 1 || all[0].ID != a.ID {
		t.Fatalf("project endpoints = %+v", all)
	}
	edges, _ := store.ListEdges(ctx, "p1")
	if len(edges) != 1 || edges[0].Rel != "serves" {
		t.Fatalf("edges = %+v", edges)
	}
}

func TestPGStore_UpsertAndLink(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	store, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	project := "asset-test-project"
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM assets WHERE project_id = $1`, project)
	})
	a, err := store.UpsertAsset(ctx, AssetInput{ProjectID: project, Kind: KindEndpoint, Key: "/API/Users", DisplayName: "/api/users"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	again, err := store.UpsertAsset(ctx, AssetInput{ProjectID: project, Kind: KindEndpoint, Key: "/api/users", InfoItemID: "ii-1"})
	if err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	if again.ID != a.ID || again.LastInfoItemID != "ii-1" {
		t.Fatalf("dedup/update = %+v", again)
	}
	if err := store.LinkTaskAsset(ctx, Link{ScanID: "asset-run-1", TaskID: "task-1", AssetID: a.ID, Source: "engine_inventory"}); err != nil {
		t.Fatalf("link: %v", err)
	}
	items, err := store.ListTaskAssets(ctx, "asset-run-1")
	if err != nil || len(items) != 1 || items[0].ID != a.ID {
		t.Fatalf("task assets = %+v err=%v", items, err)
	}
}
