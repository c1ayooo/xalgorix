package graph

// infospace_integration_test.go — IG1 acceptance for the unified information
// space reader. The reader must see graph aggregates and InfoItem atoms under
// one run_id, bound depth/limits, preserve paths, and report cold-path gaps.

import (
	"context"
	"fmt"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
)

func TestInfoSpaceRegionContextAndAssociation(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	runID := scanID(t)
	ctxID := runID + "-ctx"
	cleanupInfoSpaceScan(t, store, runID, ctxID)

	// Aggregates: one intent and one fact share a subject/purpose; one belief
	// uses the legacy payload shape (no info block).
	intent, err := store.AddNode(ctx, NodeInput{
		ScanID: runID, Kind: KindIntent, State: StateOpen, Priority: 10,
		Payload: `{"task_id":"i1","title":"probe container","target":"10.0.1.5:8080",` +
			`"target_kind":"container","vuln_class":"ssrf",` +
			`"info":{"subject":{"kind":"container","identifier":"10.0.1.5:8080"},` +
			`"purpose":"ssrf","nature":"inferred","observed_at":"2026-11-24T01:00:00Z"}}`,
	})
	if err != nil {
		t.Fatalf("add intent: %v", err)
	}
	fact, err := store.AddNode(ctx, NodeInput{
		ScanID: runID, Kind: KindFact, State: StateConfirmed, Priority: 5,
		Payload: `{"task_id":"f1","title":"ssrf response","target":"10.0.1.5:8080",` +
			`"target_kind":"container","vuln_class":"ssrf",` +
			`"info":{"subject":{"kind":"container","identifier":"10.0.1.5:8080"},` +
			`"purpose":"ssrf","nature":"observed","observed_at":"2026-11-24T01:05:00Z"}}`,
	})
	if err != nil {
		t.Fatalf("add fact: %v", err)
	}
	legacy, err := store.AddNode(ctx, NodeInput{
		ScanID: runID, Kind: KindBelief, State: StateQueued, Priority: 1,
		Payload: `{"task_id":"b1","title":"legacy belief","target":"10.0.1.6/api","vuln_class":"xss"}`,
	})
	if err != nil {
		t.Fatalf("add belief: %v", err)
	}
	// Aggregate fact that carries an InfoItem reference: IG3 links it to the
	// atom so association can cross from the aggregate layer to the atom layer.
	linkedFact, err := store.AddNode(ctx, NodeInput{
		ScanID: runID, Kind: KindFact, State: StateConfirmed, Priority: 5,
		Payload: `{"task_id":"f2","title":"sqli response","target":"10.0.1.5:8080/api",` +
			`"vuln_class":"sqli","info_item_id":"atom-link",` +
			`"info":{"subject":{"kind":"endpoint","identifier":"/api/v2"},` +
			`"purpose":"sqli","nature":"observed","observed_at":"2026-11-24T01:10:00Z"}}`,
	})
	if err != nil {
		t.Fatalf("add linked fact: %v", err)
	}
	if err := store.UpsertInfoLink(ctx, runID, linkedFact.ID, "atom-link", InfoLinkRoleProvenance); err != nil {
		t.Fatalf("link fact atom: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO exploration_edges (scan_id, src_id, rel, dst_id)
		VALUES ($1, $2, 'supports', $3)`, runID, fact.ID, intent.ID); err != nil {
		t.Fatalf("add graph edge: %v", err)
	}

	// Atoms live under a sibling ScanContext; run_journal maps them to runID.
	for seq := 1; seq <= 4; seq++ {
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO run_journal (scan_id, instance_id, seq, record)
			VALUES ($1, $2, $3, '{}')`, ctxID, runID, seq); err != nil {
			t.Fatalf("journal seq %d: %v", seq, err)
		}
	}
	for _, atom := range []struct {
		id      string
		seq     int
		nature  string
		support string
	}{
		{id: "atom-a", seq: 1, nature: "observed", support: "[]"},
		{id: "atom-b", seq: 2, nature: "observed", support: "[]"},
		{id: "atom-c", seq: 3, nature: "inferred", support: "[]"}, // unsubstantiated
		{id: "atom-link", seq: 4, nature: "observed", support: "[]"},
	} {
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO info_items (
				id, scan_id, source_seq, nature, content, coordinates, source, subject,
				purpose, context, relation_ids, supports, confidence, coordinate_status,
				observed_at, created_at, instance_id)
			VALUES ($1,$2,$3,$4,
				'{"text":"observed response","media_type":"text/plain"}',
				'{"purpose":"ssrf"}',
				'{"kind":"tool","name":"http_request"}',
				'{"kind":"endpoint","identifier":"/api"}',
				'ssrf','{"role":"user"}','[]',$5,0.9,'complete',
				'2026-11-24T01:00:00Z','2026-11-24T01:00:00Z',$6)`,
			atom.id, ctxID, atom.seq, atom.nature, atom.support, runID); err != nil {
			t.Fatalf("insert atom %s: %v", atom.id, err)
		}
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO info_item_edges (scan_id, source_seq, from_id, to_id, type, weight, created_at)
		VALUES ($1, 3, 'atom-a', 'atom-b', 'supports', 0.8, now()),
		       ($1, 4, 'atom-a', 'atom-c', 'supports', 0.8, now())`, ctxID); err != nil {
		t.Fatalf("insert atom edges: %v", err)
	}

	reader := infospace.Reader(store)
	legacyItem, err := readerRegionItem(ctx, reader, runID, fmt.Sprintf("%d", legacy.ID))
	if err != nil {
		t.Fatalf("legacy fallback: %v", err)
	}
	if legacyItem.Subject != "10.0.1.6/api" || legacyItem.Purpose != "xss" {
		t.Fatalf("legacy fallback coordinates = %+v", legacyItem)
	}

	// Region: aggregate-only, mixed (run_id bridges the sibling context), and a
	// property filter.
	agg, err := reader.Region(ctx, infospace.RegionQuery{
		RunIDs: []string{runID}, Layer: infospace.LayerAggregate, Limit: 10,
	})
	if err != nil {
		t.Fatalf("region aggregate: %v", err)
	}
	if len(agg.Items) != 4 {
		t.Fatalf("aggregate items = %d, want 4: %+v", len(agg.Items), agg.Items)
	}
	mixed, err := reader.Region(ctx, infospace.RegionQuery{RunIDs: []string{runID}, Limit: 10})
	if err != nil {
		t.Fatalf("region mixed: %v", err)
	}
	if len(mixed.Items) != 7 {
		t.Fatalf("mixed items = %d, want 4 aggregates + 3 substantiated atoms", len(mixed.Items))
	}
	filtered, err := reader.Region(ctx, infospace.RegionQuery{
		RunIDs: []string{runID}, Subject: "10.0.1.5:8080", Limit: 10,
	})
	if err != nil {
		t.Fatalf("region subject: %v", err)
	}
	if len(filtered.Items) != 2 {
		t.Fatalf("subject-filtered items = %d, want 2: %+v", len(filtered.Items), filtered.Items)
	}
	for _, item := range filtered.Items {
		if item.Layer != infospace.LayerAggregate {
			t.Fatalf("subject filter leaked layer %s", item.Layer)
		}
	}

	// Keyset pagination: no overlap and the cursor advances.
	page1, err := reader.Region(ctx, infospace.RegionQuery{RunIDs: []string{runID}, Limit: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if !page1.HasMore || page1.NextCursor == "" || len(page1.Items) != 2 {
		t.Fatalf("page1 = %+v", page1)
	}
	cursor, err := infoitem.ParseRegionCursor(page1.NextCursor)
	if err != nil {
		t.Fatalf("parse cursor: %v", err)
	}
	page2, err := reader.Region(ctx, infospace.RegionQuery{
		RunIDs: []string{runID}, Limit: 2, Cursor: cursor,
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Items) == 0 {
		t.Fatal("page2 empty")
	}
	seen := map[string]bool{}
	for _, item := range page1.Items {
		seen[string(item.Layer)+":"+item.InfoID] = true
	}
	for _, item := range page2.Items {
		if seen[string(item.Layer)+":"+item.InfoID] {
			t.Fatalf("pagination overlap at %s/%s", item.Layer, item.InfoID)
		}
	}

	// Context slice: fixed subject.
	slice, err := reader.ContextSlice(ctx, infospace.ContextQuery{
		RunID: runID, Subject: "10.0.1.5:8080", Layer: infospace.LayerAggregate, Limit: 10,
	})
	if err != nil {
		t.Fatalf("context slice: %v", err)
	}
	if len(slice.Items) != 2 {
		t.Fatalf("context slice = %d, want 2", len(slice.Items))
	}

	// Association: aggregate supports edge; inferred unsubstantiated atom is
	// not activated; observed atom path is returned with relation types.
	hits, err := reader.Associate(ctx, infospace.AssociationQuery{
		RunID: runID, StartIDs: []string{fmt.Sprintf("%d", intent.ID)}, MaxDepth: 2, Limit: 10,
	})
	if err != nil {
		t.Fatalf("associate aggregate: %v", err)
	}
	if len(hits.Hits) != 1 || hits.Hits[0].Item.InfoID != fmt.Sprintf("%d", fact.ID) {
		t.Fatalf("aggregate association = %+v", hits.Hits)
	}
	if len(hits.Hits[0].Path) != 2 || len(hits.Hits[0].Rels) != 1 || hits.Hits[0].Rels[0] != "supports" {
		t.Fatalf("aggregate path = %+v", hits.Hits[0])
	}
	atomHits, err := reader.Associate(ctx, infospace.AssociationQuery{
		RunID: runID, StartIDs: []string{"atom-a"}, MaxDepth: 2, Limit: 10,
	})
	if err != nil {
		t.Fatalf("associate atoms: %v", err)
	}
	if len(atomHits.Hits) != 1 || atomHits.Hits[0].Item.InfoID != "atom-b" {
		t.Fatalf("atom association = %+v", atomHits.Hits)
	}
	// IG3: an aggregate fact carrying info_item_id reaches its atom through the
	// materializes edge projected from graph_info_links; the atom stays an atom
	// (it is not turned into a graph node).
	linkHits, err := reader.Associate(ctx, infospace.AssociationQuery{
		RunID: runID, StartIDs: []string{fmt.Sprintf("%d", linkedFact.ID)},
		EdgeTypes: []string{infospace.RelationMaterializes}, MaxDepth: 2, Limit: 10,
	})
	if err != nil {
		t.Fatalf("associate linked fact: %v", err)
	}
	if len(linkHits.Hits) != 1 || linkHits.Hits[0].Item.InfoID != "atom-link" {
		t.Fatalf("materializes association = %+v", linkHits.Hits)
	}
	if hit := linkHits.Hits[0]; hit.Depth != 1 || len(hit.Rels) != 1 || hit.Rels[0] != infospace.RelationMaterializes {
		t.Fatalf("materializes path = %+v", hit)
	}
	if hit := linkHits.Hits[0]; hit.Item.Layer != infospace.LayerAtom || hit.Item.Nature != "observed" {
		t.Fatalf("materializes hit = %+v", hit.Item)
	}
	if _, err := reader.Associate(ctx, infospace.AssociationQuery{
		RunID: runID, StartIDs: []string{"atom-a"}, EdgeTypes: []string{"bogus"}, MaxDepth: 2, Limit: 10,
	}); err == nil {
		t.Fatal("unknown edge type must be rejected")
	}

	// A cold-path gap is surfaced on atom-inclusive queries, not on
	// aggregate-only ones.
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO info_sync_watermarks (scan_id, status, last_error, updated_at)
		VALUES ($1, 'gap', 'reconciler behind', now())`, ctxID); err != nil {
		t.Fatalf("insert watermark: %v", err)
	}
	gapped, err := reader.Region(ctx, infospace.RegionQuery{RunIDs: []string{runID}, Limit: 10})
	if err != nil {
		t.Fatalf("region with gap: %v", err)
	}
	if !gapped.Incomplete || gapped.GapReason == "" {
		t.Fatalf("mixed query must report the gap: %+v", gapped)
	}
	aggGap, err := reader.Region(ctx, infospace.RegionQuery{
		RunIDs: []string{runID}, Layer: infospace.LayerAggregate, Limit: 10,
	})
	if err != nil {
		t.Fatalf("aggregate with gap: %v", err)
	}
	if aggGap.Incomplete {
		t.Fatalf("aggregate-only query must be complete: %+v", aggGap)
	}
}

// TestGraphInfoLinkUpsertIdempotentAndBackfillCompatible — IG3 link writes are
// keyed on (run_id, node_id, info_item_id, role), the same key the 9002
// backfill uses, so re-projection and the backfill converge to one row instead
// of duplicating links.
func TestGraphInfoLinkUpsertIdempotentAndBackfillCompatible(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	runID := scanID(t)
	cleanupInfoSpaceScan(t, store, runID, runID+"-ctx")

	scalar, err := store.AddNode(ctx, NodeInput{
		ScanID: runID, Kind: KindFact, State: StateConfirmed, UnitKey: "ev-scalar",
		Payload: `{"evidence_id":"ev-scalar","info_item_id":"atom-scalar"}`,
	})
	if err != nil {
		t.Fatalf("add scalar fact: %v", err)
	}
	array, err := store.AddNode(ctx, NodeInput{
		ScanID: runID, Kind: KindFinding, State: StateConfirmed, UnitKey: "F-1",
		Payload: `{"finding_id":"F-1","info_item_ids":["atom-a1","atom-b1","atom-a1"]}`,
	})
	if err != nil {
		t.Fatalf("add array finding: %v", err)
	}
	countLinks := func(nodeID int64) int {
		t.Helper()
		var n int
		if err := store.pool.QueryRow(ctx,
			`SELECT count(*) FROM graph_info_links WHERE run_id = $1 AND node_id = $2`,
			runID, nodeID).Scan(&n); err != nil {
			t.Fatalf("count links: %v", err)
		}
		return n
	}

	// Same key twice: one row. An empty role defaults to provenance.
	for i := 0; i < 2; i++ {
		if err := store.UpsertInfoLink(ctx, runID, scalar.ID, "atom-scalar", ""); err != nil {
			t.Fatalf("upsert link %d: %v", i, err)
		}
	}
	if got := countLinks(scalar.ID); got != 1 {
		t.Fatalf("scalar links = %d, want 1", got)
	}
	var role string
	if err := store.pool.QueryRow(ctx,
		`SELECT role FROM graph_info_links WHERE run_id = $1 AND node_id = $2`,
		runID, scalar.ID).Scan(&role); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role != InfoLinkRoleProvenance {
		t.Fatalf("role = %q, want %q", role, InfoLinkRoleProvenance)
	}

	// Role is part of the key: a different role is a distinct link.
	if err := store.UpsertInfoLink(ctx, runID, scalar.ID, "atom-scalar", InfoLinkRoleSupports); err != nil {
		t.Fatalf("upsert other role: %v", err)
	}
	if got := countLinks(scalar.ID); got != 2 {
		t.Fatalf("scalar links after role change = %d, want 2", got)
	}

	// Invalid inputs are rejected instead of silently writing junk.
	if err := store.UpsertInfoLink(ctx, runID, scalar.ID, "", InfoLinkRoleProvenance); err == nil {
		t.Fatal("empty atom id must be rejected")
	}
	if err := store.UpsertInfoLink(ctx, runID, scalar.ID, "atom-scalar", "bogus"); err == nil {
		t.Fatal("unknown role must be rejected")
	}

	// The 9002 backfill statements, scoped to this run. Running them twice must
	// not duplicate the projection-written row (first pass) or each other.
	backfillScalar := `
		INSERT INTO graph_info_links (run_id, node_id, info_item_id, role)
		SELECT n.scan_id, n.id, xalgorix_safe_jsonb(n.payload) ->> 'info_item_id', 'provenance'
		FROM exploration_nodes n
		WHERE n.scan_id = $1 AND n.kind IN ('fact', 'finding')
		  AND COALESCE(xalgorix_safe_jsonb(n.payload) ->> 'info_item_id', '') <> ''
		ON CONFLICT DO NOTHING`
	backfillArray := `
		INSERT INTO graph_info_links (run_id, node_id, info_item_id, role)
		SELECT n.scan_id, n.id, atom_id, 'provenance'
		FROM exploration_nodes n
		CROSS JOIN LATERAL jsonb_array_elements_text(
			CASE
				WHEN jsonb_typeof(xalgorix_safe_jsonb(n.payload) -> 'info_item_ids') = 'array'
				THEN xalgorix_safe_jsonb(n.payload) -> 'info_item_ids'
				ELSE '[]'::jsonb
			END
		) AS atom_id
		WHERE n.scan_id = $1 AND n.kind IN ('fact', 'finding') AND atom_id <> ''
		ON CONFLICT DO NOTHING`
	for i := 0; i < 2; i++ {
		if _, err := store.pool.Exec(ctx, backfillScalar, runID); err != nil {
			t.Fatalf("scalar backfill %d: %v", i, err)
		}
		if _, err := store.pool.Exec(ctx, backfillArray, runID); err != nil {
			t.Fatalf("array backfill %d: %v", i, err)
		}
	}
	if got := countLinks(scalar.ID); got != 2 { // provenance + supports
		t.Fatalf("scalar links after backfill = %d, want 2", got)
	}
	if got := countLinks(array.ID); got != 2 { // atom-a1 + atom-b1 (array dup dropped)
		t.Fatalf("array links after backfill = %d, want 2", got)
	}
}

func cleanupInfoSpaceScan(t *testing.T, store *PGStore, runID, ctxID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM info_item_edges WHERE scan_id = ANY($1)`,
			`DELETE FROM info_items WHERE scan_id = ANY($1)`,
			`DELETE FROM run_journal WHERE scan_id = ANY($1)`,
			`DELETE FROM info_sync_watermarks WHERE scan_id = ANY($1)`,
			`DELETE FROM graph_info_links WHERE run_id = ANY($1)`,
		} {
			if _, err := store.pool.Exec(ctx, stmt, []string{runID, ctxID}); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
		if _, err := store.pool.Exec(ctx, `DELETE FROM exploration_nodes WHERE scan_id = $1`, runID); err != nil {
			t.Logf("cleanup nodes: %v", err)
		}
	})
}

func readerRegionItem(ctx context.Context, reader infospace.Reader, runID, infoID string) (infospace.Item, error) {
	res, err := reader.Region(ctx, infospace.RegionQuery{
		RunIDs: []string{runID}, Layer: infospace.LayerAggregate, Limit: 50,
	})
	if err != nil {
		return infospace.Item{}, err
	}
	for _, item := range res.Items {
		if item.InfoID == infoID {
			return item, nil
		}
	}
	return infospace.Item{}, fmt.Errorf("info id %s not found in %+v", infoID, res.Items)
}
