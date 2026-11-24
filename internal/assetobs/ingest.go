// Package assetobs ingests observed asset identities into the project asset
// graph from traffic and tool observations.
//
// The InfoItem is the provenance authority; this package only records the
// normalized identity and the InfoItem reference. Ingestion is best-effort and
// never blocks the observation path: dedupe is checked synchronously per scan,
// and the PostgreSQL write happens on a short-lived background goroutine.
package assetobs

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/assets"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

const ingestTimeout = 3 * time.Second

// IngestURL derives and records the asset identities an observed URL proves.
// source is a short provenance label ("traffic_observed", "tool_observation").
//
// The host and the path of one URL come from the same observation, so the batch
// also records the deterministic `exposes` edge. That edge is what lets the
// dashboard drill from a target into its paths instead of showing every asset
// kind in one flat list.
func IngestURL(sc *scanctx.ScanContext, infoItemID, rawURL, source string) {
	if sc == nil || strings.TrimSpace(sc.ProjectID) == "" {
		return
	}
	ingestURLInputs(sc, infoItemID, assets.InputsFromURL(sc.ProjectID, rawURL), source)
}

// IngestURLsInText extracts at most maxURLsPerText http(s) URLs from free-form
// tool output and records their asset identities. Local file paths are NOT
// ingested: only an explicit scheme proves a network asset.
func IngestURLsInText(sc *scanctx.ScanContext, infoItemID, text, source string) {
	const maxURLsPerText = 10
	if sc == nil || strings.TrimSpace(sc.ProjectID) == "" {
		return
	}
	seen := 0
	for _, field := range strings.Fields(text) {
		raw := strings.Trim(field, "\"'`()[]{}<>,;")
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			continue
		}
		IngestURL(sc, infoItemID, raw, source)
		seen++
		if seen >= maxURLsPerText {
			return
		}
	}
}

// ingestURLInputs dedupes the identities of one URL per session. When at least
// one identity is new it upserts the whole batch: a previously seen host is
// re-read (idempotent) so the edge can reference its id.
func ingestURLInputs(sc *scanctx.ScanContext, infoItemID string, inputs []assets.AssetInput, source string) {
	if sc == nil || strings.TrimSpace(sc.ProjectID) == "" || len(inputs) == 0 {
		return
	}
	repo := assets.Backend()
	if repo == nil {
		return
	}
	anyNew := false
	for i := range inputs {
		inputs[i].ProjectID = sc.ProjectID
		inputs[i].InfoItemID = strings.TrimSpace(infoItemID)
		if sc.MarkAssetSeen(string(inputs[i].Kind), inputs[i].Key) {
			anyNew = true
		}
	}
	if !anyNew {
		return
	}
	go ingestURLBatch(sc, repo, inputs, source)
}

func ingestURLBatch(sc *scanctx.ScanContext, repo assets.Repository, inputs []assets.AssetInput, source string) {
	parent := sc.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, ingestTimeout)
	defer cancel()

	scanID := strings.TrimSpace(sc.InstanceID)
	if scanID == "" {
		scanID = strings.TrimSpace(sc.ID)
	}
	var hosts, endpoints []*assets.Asset
	for i := range inputs {
		in := inputs[i]
		asset, err := repo.UpsertAsset(ctx, in)
		if err != nil {
			log.Printf("[assetobs] upsert %s/%s failed: %v", in.Kind, in.Key, err)
			continue
		}
		if asset == nil {
			continue
		}
		if err := repo.LinkTaskAsset(ctx, assets.Link{
			ScanID: scanID, TaskID: strings.TrimSpace(sc.TaskID), AssetID: asset.ID, Source: source,
		}); err != nil {
			log.Printf("[assetobs] link %s to %s failed: %v", in.Key, scanID, err)
		}
		switch in.Kind {
		case assets.KindRootDomain, assets.KindSubdomain, assets.KindIP, assets.KindService, assets.KindApp:
			hosts = append(hosts, asset)
		case assets.KindEndpoint:
			endpoints = append(endpoints, asset)
		}
	}
	for _, host := range hosts {
		for _, endpoint := range endpoints {
			if err := repo.AddEdge(ctx, sc.ProjectID, host.ID, endpoint.ID, assets.RelExposes); err != nil {
				log.Printf("[assetobs] exposes edge %d -> %d failed: %v", host.ID, endpoint.ID, err)
			}
		}
	}
}

// IngestInputs records the given identities, skipping ones already seen in
// this session. It performs the dedupe check synchronously and the database
// writes asynchronously. Generic callers pass unrelated identities, so no
// structural edge is inferred here; URL ingestion uses ingestURLInputs.
func IngestInputs(sc *scanctx.ScanContext, infoItemID string, inputs []assets.AssetInput, source string) {
	if sc == nil || strings.TrimSpace(sc.ProjectID) == "" {
		return
	}
	repo := assets.Backend()
	if repo == nil {
		return
	}
	for _, in := range inputs {
		if !sc.MarkAssetSeen(string(in.Kind), in.Key) {
			continue
		}
		in.ProjectID = sc.ProjectID
		in.InfoItemID = strings.TrimSpace(infoItemID)
		go ingestOne(sc, repo, in, source)
	}
}

func ingestOne(sc *scanctx.ScanContext, repo assets.Repository, in assets.AssetInput, source string) {
	parent := sc.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, ingestTimeout)
	defer cancel()

	asset, err := repo.UpsertAsset(ctx, in)
	if err != nil {
		log.Printf("[assetobs] upsert %s/%s failed: %v", in.Kind, in.Key, err)
		return
	}
	if asset == nil {
		return
	}
	scanID := strings.TrimSpace(sc.InstanceID)
	if scanID == "" {
		scanID = strings.TrimSpace(sc.ID)
	}
	if err := repo.LinkTaskAsset(ctx, assets.Link{
		ScanID: scanID, TaskID: strings.TrimSpace(sc.TaskID), AssetID: asset.ID, Source: source,
	}); err != nil {
		log.Printf("[assetobs] link %s to %s failed: %v", in.Key, scanID, err)
	}
}
