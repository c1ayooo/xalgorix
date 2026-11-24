package agent

import (
	"context"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/assets"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestAssetInputsFromEndpoint(t *testing.T) {
	cases := []struct {
		raw   string
		kinds []assets.Kind
		keys  []string
	}{
		{"/api/users", []assets.Kind{assets.KindEndpoint}, []string{"/api/users"}},
		{"https://api.example.com/v1/users?id=1", []assets.Kind{assets.KindSubdomain, assets.KindEndpoint}, []string{"api.example.com", "/v1/users"}},
		{"example.com", []assets.Kind{assets.KindRootDomain}, []string{"example.com"}},
		{"", nil, nil},
	}
	for _, tc := range cases {
		got := assetInputsFromEndpoint("p1", tc.raw)
		if len(got) != len(tc.kinds) {
			t.Fatalf("%q: got %d inputs, want %d (%+v)", tc.raw, len(got), len(tc.kinds), got)
		}
		for i := range got {
			if got[i].Kind != tc.kinds[i] || got[i].Key != tc.keys[i] {
				t.Fatalf("%q[%d] = %+v, want %s/%s", tc.raw, i, got[i], tc.kinds[i], tc.keys[i])
			}
		}
	}
}

func TestSyncAssetsProjectsAndLinks(t *testing.T) {
	store := assets.NewMemoryStore()
	assets.SetBackend(store)
	t.Cleanup(func() { assets.SetBackend(nil) })

	sctx := scanctx.New("scan-assets", "")
	sctx.SetInstanceID("run-assets")
	sctx.ProjectID = "proj-assets"
	sctx.TaskID = "task-assets"
	state := NewScanState()
	state.ScanContextID = sctx.ID
	state.DiscoveredEndpoints = []string{
		"/api/users",
		"https://api.example.com/v1/login",
	}
	ag := &Agent{state: state, scanCtx: sctx}
	ag.syncAssets()

	ctx := context.Background()
	items, err := store.ListTaskAssets(ctx, "run-assets")
	if err != nil {
		t.Fatalf("list task assets: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("task assets = %+v, want endpoint+/api/users + host + /v1/login", items)
	}
	byKind := map[assets.Kind]int{}
	for _, item := range items {
		byKind[item.Kind]++
	}
	if byKind[assets.KindEndpoint] != 2 || byKind[assets.KindSubdomain] != 1 {
		t.Fatalf("kind counts = %+v", byKind)
	}
	all, _ := store.ListAssets(ctx, "proj-assets")
	if len(all) != 3 {
		t.Fatalf("project assets = %+v", all)
	}
}
