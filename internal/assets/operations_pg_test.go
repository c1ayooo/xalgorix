package assets

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestPGAssetOperations exercises archive/filter/detail/merge against a real
// PostgreSQL (XALGORIX_TEST_DATABASE_URL). Skipped without a DSN.
func TestPGAssetOperations(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	projectID := fmt.Sprintf("assets-ops-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM task_asset_links WHERE scan_id LIKE $1`, projectID+"%")
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM assets WHERE project_id = $1`, projectID)
	})

	src, err := store.UpsertAsset(ctx, AssetInput{ProjectID: projectID, Kind: KindSubdomain, Key: "www.example.com"})
	if err != nil {
		t.Fatalf("upsert src: %v", err)
	}
	dst, err := store.UpsertAsset(ctx, AssetInput{ProjectID: projectID, Kind: KindRootDomain, Key: "example.com"})
	if err != nil {
		t.Fatalf("upsert dst: %v", err)
	}
	ep, err := store.UpsertAsset(ctx, AssetInput{ProjectID: projectID, Kind: KindEndpoint, Key: "/login"})
	if err != nil {
		t.Fatalf("upsert ep: %v", err)
	}
	if err := store.LinkTaskAsset(ctx, Link{ScanID: projectID + "-run", TaskID: "task-1", AssetID: src.ID, Source: "traffic"}); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := store.AddEdge(ctx, projectID, src.ID, ep.ID, "serves"); err != nil {
		t.Fatalf("edge: %v", err)
	}
	if err := store.AddEdge(ctx, projectID, dst.ID, ep.ID, "serves"); err != nil {
		t.Fatalf("edge dup: %v", err)
	}

	if err := store.MergeAssets(ctx, projectID, src.ID, dst.ID, "alice"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	detail, err := store.GetAssetDetail(ctx, projectID, dst.ID)
	if err != nil {
		t.Fatalf("target detail: %v", err)
	}
	if len(detail.Links) != 1 || len(detail.Edges) != 1 {
		t.Fatalf("target detail = %+v", detail)
	}
	if detail.Edges[0].SrcID != dst.ID {
		t.Fatalf("edge not rewritten: %+v", detail.Edges)
	}
	srcDetail, err := store.GetAssetDetail(ctx, projectID, src.ID)
	if err != nil {
		t.Fatalf("source detail: %v", err)
	}
	if srcDetail.Asset.MergedIntoID == nil || *srcDetail.Asset.MergedIntoID != dst.ID || srcDetail.Asset.ArchivedAt == nil {
		t.Fatalf("source tombstone = %+v", srcDetail.Asset)
	}

	active, err := store.ListProjectAssets(ctx, AssetFilter{ProjectID: projectID})
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	for _, a := range active {
		if a.ID == src.ID {
			t.Fatalf("merged source listed active: %+v", active)
		}
	}
	if n, err := store.SetAssetsArchived(ctx, projectID, []int64{ep.ID}, true, "alice"); err != nil || n != 1 {
		t.Fatalf("archive n=%d err=%v", n, err)
	}
	filtered, err := store.ListProjectAssets(ctx, AssetFilter{ProjectID: projectID, TaskID: "task-1"})
	if err != nil {
		t.Fatalf("task filter: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != dst.ID {
		t.Fatalf("task filter = %+v", filtered)
	}
}
