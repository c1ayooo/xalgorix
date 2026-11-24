package agent

// asset_projection.go — M1-6b: derive project-scoped asset identities from the
// engine's observed attack surface.
//
// This first slice is deliberately conservative: it projects endpoints and
// hosts the engine already extracted from real tool output / notes into the
// asset graph as source=engine_inventory (derived, not a raw observation).
// Raw observed assets will be added by the traffic/tool-output ingestion in
// the next slice; InfoItem remains the provenance authority.

import (
	"context"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/assets"
)

func (a *Agent) syncAssets() {
	if a == nil || a.scanCtx == nil || a.state == nil || a.delegatedAgentID != "" {
		return
	}
	repo := assets.Backend()
	runID := a.planGraphScanID()
	if repo == nil || runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	projectID := strings.TrimSpace(a.scanCtx.ProjectID)
	taskID := strings.TrimSpace(a.scanCtx.TaskID)
	for _, raw := range a.state.DiscoveredEndpoints {
		for _, in := range assetInputsFromEndpoint(projectID, raw) {
			asset, err := repo.UpsertAsset(ctx, in)
			if err != nil || asset == nil {
				continue
			}
			_ = repo.LinkTaskAsset(ctx, assets.Link{
				ScanID: runID, TaskID: taskID, AssetID: asset.ID, Source: "engine_inventory",
			})
		}
	}
}

// assetInputsFromEndpoint maps one discovered path/URL to asset identities.
// It never invents a host: a bare path becomes an endpoint only.
func assetInputsFromEndpoint(projectID, raw string) []assets.AssetInput {
	return assets.InputsFromURL(projectID, raw)
}
