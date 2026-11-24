package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/assets"
)

func TestScanAssetsProjection_ReadsLinkedAssets(t *testing.T) {
	s := newTestServer(t, nil)
	store := assets.NewMemoryStore()
	assets.SetBackend(store)
	t.Cleanup(func() { assets.SetBackend(nil) })
	ctx := context.Background()
	a, err := store.UpsertAsset(ctx, assets.AssetInput{
		ProjectID: "p1", Kind: assets.KindEndpoint, Key: "/api/users",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.LinkTaskAsset(ctx, assets.Link{
		ScanID: "run-assets-ui", TaskID: "task-1", AssetID: a.ID, Source: "engine_inventory",
	}); err != nil {
		t.Fatalf("link: %v", err)
	}

	rr := projectionGet(t, s.handleScanAssets, "/api/scans/run-assets-ui/assets")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Source string         `json:"source"`
		Items  []assets.Asset `json:"items"`
		Total  int            `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != "assets" || body.Total != 1 || len(body.Items) != 1 {
		t.Fatalf("body = %#v", body)
	}
	if body.Items[0].Key != "/api/users" || body.Items[0].Kind != assets.KindEndpoint {
		t.Fatalf("item = %+v", body.Items[0])
	}
}

func TestScanAssetsProjection_NoBackendIsHonestEmpty(t *testing.T) {
	s := newTestServer(t, nil)
	rr := projectionGet(t, s.handleScanAssets, "/api/scans/no-assets/assets")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct {
		Source string `json:"source"`
		Total  int    `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != "none" || body.Total != 0 {
		t.Fatalf("body = %#v", body)
	}
}
