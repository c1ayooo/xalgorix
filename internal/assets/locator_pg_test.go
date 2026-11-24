package assets

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestPGAssetLocators exercises migration 0037 against a real PostgreSQL:
// the logical asset identity is stable while locator observations append and
// read back newest-first, isolated by project.
func TestPGAssetLocators(t *testing.T) {
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
	projectID := fmt.Sprintf("assets-locator-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM assets WHERE project_id = $1`, projectID)
	})

	asset, err := store.UpsertAsset(ctx, AssetInput{
		ProjectID: projectID, Kind: KindApp, Key: "customer-portal",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	first := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	second := first.Add(time.Hour)
	if _, err := store.RecordLocator(ctx, LocatorInput{
		ProjectID: projectID, AssetID: asset.ID, Locator: "10.0.0.5:8080",
		InstanceID: "container-old", ObservedAt: first, SourceInfoItemID: "ii-1",
	}); err != nil {
		t.Fatalf("record first: %v", err)
	}
	// Same tuple is idempotent.
	if _, err := store.RecordLocator(ctx, LocatorInput{
		ProjectID: projectID, AssetID: asset.ID, Locator: "10.0.0.5:8080",
		InstanceID: "container-old", ObservedAt: first, SourceInfoItemID: "ii-1",
	}); err != nil {
		t.Fatalf("record duplicate: %v", err)
	}
	if _, err := store.RecordLocator(ctx, LocatorInput{
		ProjectID: projectID, AssetID: asset.ID, Locator: "10.0.0.9:8080",
		InstanceID: "container-new", ObservedAt: second, SourceInfoItemID: "ii-2",
	}); err != nil {
		t.Fatalf("record second: %v", err)
	}

	locs, err := store.ListAssetLocators(ctx, projectID, asset.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(locs) != 2 {
		t.Fatalf("locators = %d, want 2: %+v", len(locs), locs)
	}
	if locs[0].Locator != "10.0.0.9:8080" || locs[1].Locator != "10.0.0.5:8080" {
		t.Fatalf("order = %+v", locs)
	}
	for _, loc := range locs {
		if loc.AssetID != asset.ID {
			t.Fatalf("locator lost logical identity: %+v", loc)
		}
	}
	if _, err := store.ListAssetLocators(ctx, projectID+"-other", asset.ID); err == nil {
		t.Fatal("locator history crossed the project boundary")
	}
}
