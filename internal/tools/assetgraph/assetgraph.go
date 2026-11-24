// Package assetgraph exposes the model-facing record_asset tool.
//
// The asset graph is project-scoped and authoritative for identity; the
// InfoItem is authoritative for provenance. This tool records one normalized
// identity plus an optional InfoItem reference. It never writes findings,
// hypotheses or plan state, and it never invents a project scope.
package assetgraph

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/assets"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// Register adds record_asset to the registry. Worker and legacy surfaces get
// it; main/planner are read-only and deny it through the surface allowlist.
func Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name: "record_asset",
		Description: "Record one project-scoped asset identity in the asset graph: a host (root_domain/subdomain), " +
			"an endpoint path, an IP, a service, or an app. Optionally cite the InfoItem ID that proves it. " +
			"Use when recon establishes a target asset that the graph does not yet know; this does not report a finding.",
		Parameters: []tools.Parameter{
			{Name: "kind", Description: "One of: root_domain, subdomain, ip, service, endpoint, app", Required: true},
			{Name: "key", Description: "Normalized identity: hostname, URL path, IP, service key or app name", Required: true},
			{Name: "display_name", Description: "Optional human-readable label (defaults to key)", Required: false},
			{Name: "info_item_id", Description: "Optional InfoItem ID that proves this asset", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return record(r, args)
		},
	})
}

func record(r *tools.Registry, args map[string]string) (tools.Result, error) {
	repo := assets.Backend()
	if repo == nil {
		return tools.Result{Output: "Asset graph unavailable (no PostgreSQL asset authority); nothing recorded."}, nil
	}
	sc := scanctx.Get(r.GetScanContextID())
	if sc == nil {
		return tools.Result{Error: "record_asset: scan context unavailable"}, nil
	}
	projectID := strings.TrimSpace(sc.ProjectID)
	if projectID == "" {
		return tools.Result{Error: "record_asset: this run has no project scope; assets are project-scoped"}, nil
	}
	in := assets.AssetInput{
		ProjectID:   projectID,
		Kind:        assets.Kind(strings.ToLower(strings.TrimSpace(args["kind"]))),
		Key:         args["key"],
		DisplayName: args["display_name"],
		InfoItemID:  strings.TrimSpace(args["info_item_id"]),
	}
	if err := in.Normalize(); err != nil {
		return tools.Result{Error: "record_asset: " + err.Error()}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	asset, err := repo.UpsertAsset(ctx, in)
	if err != nil {
		return tools.Result{Error: fmt.Sprintf("record_asset: asset graph write failed: %v", err)}, nil
	}
	if asset == nil {
		return tools.Result{Error: "record_asset: asset graph returned no asset"}, nil
	}
	scanID := strings.TrimSpace(sc.InstanceID)
	if scanID == "" {
		scanID = strings.TrimSpace(sc.ID)
	}
	if err := repo.LinkTaskAsset(ctx, assets.Link{
		ScanID: scanID, TaskID: strings.TrimSpace(sc.TaskID), AssetID: asset.ID, Source: "agent_recorded",
	}); err != nil {
		return tools.Result{Error: fmt.Sprintf("record_asset: link asset to run failed: %v", err)}, nil
	}
	return tools.Result{Output: fmt.Sprintf("Asset recorded: %s %s (id=%d)", asset.Kind, asset.Key, asset.ID)}, nil
}
