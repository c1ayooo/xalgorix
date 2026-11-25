package assets

import (
	"context"
	"testing"
	"time"
)

// TestLocatorHistoryKeepsLogicalIdentity pins P0 test 5 at the asset-graph
// boundary: an address/port change appends a locator, while the logical asset
// id stays stable and both observations remain traceable.
func TestLocatorHistoryKeepsLogicalIdentity(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	asset, err := store.UpsertAsset(ctx, AssetInput{
		ProjectID: "proj-locator", Kind: KindApp, Key: "customer-portal",
	})
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	if _, err := store.RecordLocator(ctx, LocatorInput{
		ProjectID: "proj-locator", AssetID: asset.ID, Locator: "10.0.0.5:8080",
		InstanceID: "container-old", ObservedAt: first, SourceInfoItemID: "ii-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordLocator(ctx, LocatorInput{
		ProjectID: "proj-locator", AssetID: asset.ID, Locator: "10.0.0.9:8080",
		InstanceID: "container-new", ObservedAt: second, SourceInfoItemID: "ii-2",
	}); err != nil {
		t.Fatal(err)
	}
	locs, err := store.ListAssetLocators(ctx, "proj-locator", asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 2 {
		t.Fatalf("locators = %d, want 2", len(locs))
	}
	if locs[0].Locator != "10.0.0.9:8080" || locs[1].Locator != "10.0.0.5:8080" {
		t.Fatalf("locator order = %+v", locs)
	}
	if locs[0].AssetID != asset.ID || locs[1].AssetID != asset.ID {
		t.Fatalf("locator lost logical identity: %+v", locs)
	}
	if locs[0].InstanceID != "container-new" || locs[1].InstanceID != "container-old" {
		t.Fatalf("instance history = %+v", locs)
	}
	if _, err := store.ListAssetLocators(ctx, "other-project", asset.ID); err == nil {
		t.Fatal("locator history crossed the project boundary")
	}
}
