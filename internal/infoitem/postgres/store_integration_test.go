package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xalgord/xalgorix/v4/internal/domainjournal"
	"github.com/xalgord/xalgorix/v4/internal/infoitem"
)

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func testInfoItem(scanID, id string, seq uint64, nature infoitem.Nature, supports []string) infoitem.Item {
	now := time.Now().UTC()
	return infoitem.Item{
		ID:         id,
		ScanID:     scanID,
		SourceSeq:  seq,
		Nature:     nature,
		Supports:   supports,
		Confidence: 0.8,
		Coordinates: infoitem.Coordinates{
			Content:    infoitem.Content{Text: "observed response " + id, MediaType: "text/plain"},
			Source:     infoitem.Source{Kind: "tool", Name: "http_request", EvidenceID: id},
			ObservedAt: now,
			Subject:    infoitem.Subject{Kind: "endpoint", Identifier: "https://example.test/api"},
			Purpose:    "authorization review",
			Context:    map[string]string{"role": "user"},
		},
		CoordinateStatus: infoitem.CoordinatesComplete,
		CreatedAt:        now,
	}
}

func TestStoreApplyQueriesAndWatermark(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	scanID := fmt.Sprintf("coldpath-test-%d", time.Now().UnixNano())
	store := New(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_item_edges WHERE scan_id=$1`, scanID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_items WHERE scan_id=$1`, scanID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_audit_records WHERE scan_id=$1`, scanID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_sync_watermarks WHERE scan_id=$1`, scanID)
	})

	now := time.Now().UTC()
	item1 := testInfoItem(scanID, "ii-1", 1, infoitem.NatureObserved, nil)
	rec1 := domainjournal.Record{
		ScanID: scanID, Seq: 1, At: now,
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent-1"},
		Operation: domainjournal.OpInfoItemProjected, InfoItem: &item1,
	}
	if err := store.Apply(ctx, rec1); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(ctx, rec1); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	wm, err := store.Watermark(ctx, scanID)
	if err != nil {
		t.Fatal(err)
	}
	if wm.AppliedSeq != 1 || wm.Status != "applied" {
		t.Fatalf("watermark = %+v", wm)
	}

	// R2b trafficproxy: an OpObserveTraffic record carries a standalone
	// observed InfoItem (no hypothesis) and must project identically.
	trafficItem := infoitem.Item{
		ID:        "traffic-ii",
		ScanID:    scanID,
		SourceSeq: 2,
		Nature:    infoitem.NatureObserved,
		Coordinates: infoitem.Coordinates{
			Content:    infoitem.Content{Text: "GET https://target.example/ -> 200 via direct"},
			Source:     infoitem.Source{Kind: "trafficproxy", Name: "direct", AgentID: "inst"},
			Subject:    infoitem.Subject{Kind: "endpoint", Identifier: "GET https://target.example/"},
			Purpose:    "traffic_observation",
			Context:    map[string]string{"phase": "traffic"},
			ObservedAt: now,
		},
		CreatedAt: now,
	}
	trafficRec := domainjournal.Record{
		ScanID: scanID, Seq: 2, At: now,
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "trafficproxy"},
		Operation: domainjournal.OpObserveTraffic, InfoItem: &trafficItem,
	}
	if err := store.Apply(ctx, trafficRec); err != nil {
		t.Fatalf("apply observe_traffic record: %v", err)
	}

	item2 := testInfoItem(scanID, "ii-2", 2, infoitem.NatureInferred, []string{"ii-1"})
	rec2 := domainjournal.Record{
		ScanID: scanID, Seq: 2, At: now,
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "test"},
		Operation: domainjournal.OpInfoItemProjected, InfoItem: &item2,
		Relations: []infoitem.Relation{{
			ScanID: scanID, SourceSeq: 2, FromID: "ii-2", ToID: "ii-1",
			Type: infoitem.RelationSupports, Weight: 0.8, CreatedAt: now,
		}},
	}
	if err := store.Apply(ctx, rec2); err != nil {
		t.Fatal(err)
	}
	item3 := testInfoItem(scanID, "ii-3", 3, infoitem.NatureInferred, nil)
	rec3 := domainjournal.Record{
		ScanID: scanID, Seq: 3, At: now,
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "test"},
		Operation: domainjournal.OpInfoItemProjected, InfoItem: &item3,
	}
	if err := store.Apply(ctx, rec3); err != nil {
		t.Fatal(err)
	}
	wm, _ = store.Watermark(ctx, scanID)
	if wm.AppliedSeq != 3 || wm.Status != "applied" {
		t.Fatalf("watermark after seq3 = %+v", wm)
	}

	region, err := store.Region(ctx, infoitem.RegionQuery{
		ScanIDs: []string{scanID},
		Subject: "https://example.test/api",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(region.Items) != 2 || region.Incomplete || region.Truncated {
		t.Fatalf("region = %+v", region)
	}

	assoc, err := store.Associate(ctx, infoitem.AssociationQuery{
		ScanID: scanID, StartIDs: []string{"ii-2"}, MaxDepth: 2, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(assoc.Hits) != 1 || assoc.Hits[0].Item.ID != "ii-1" || assoc.Hits[0].Depth != 1 {
		t.Fatalf("association = %+v", assoc)
	}

	contextSlice, err := store.ContextSlice(ctx, infoitem.ContextQuery{
		ScanID: scanID, Subject: "https://example.test/api",
		ContextEquals: map[string]string{"role": "user"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(contextSlice.Items) != 3 {
		t.Fatalf("context slice = %+v", contextSlice)
	}
}

func TestStoreWatermarkReportsGapUntilContiguous(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	scanID := fmt.Sprintf("coldpath-gap-%d", time.Now().UnixNano())
	store := New(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_items WHERE scan_id=$1`, scanID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_audit_records WHERE scan_id=$1`, scanID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_sync_watermarks WHERE scan_id=$1`, scanID)
	})

	now := time.Now().UTC()
	record := func(seq uint64) domainjournal.Record {
		return domainjournal.Record{
			ScanID: scanID, Seq: seq, At: now,
			Actor: domainjournal.Actor{Kind: domainjournal.ActorEngine}, Operation: domainjournal.OpSetStatus,
		}
	}
	if err := store.Apply(ctx, record(3)); err != nil {
		t.Fatal(err)
	}
	wm, _ := store.Watermark(ctx, scanID)
	if wm.AppliedSeq != 0 || wm.Status != "gap" {
		t.Fatalf("gap watermark = %+v", wm)
	}
	if err := store.Apply(ctx, record(1)); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(ctx, record(2)); err != nil {
		t.Fatal(err)
	}
	wm, _ = store.Watermark(ctx, scanID)
	if wm.AppliedSeq != 3 || wm.Status != "applied" {
		t.Fatalf("repaired watermark = %+v", wm)
	}
}

// The AllScans region form is the cross-scan shape the M4 region consumer
// relies on: it must reach other scans' items, while an explicit ScanIDs list
// keeps the per-scan scoping unchanged.
func TestStoreRegionAllScansSpansScans(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	scanA := fmt.Sprintf("allscans-a-%d", time.Now().UnixNano())
	scanB := fmt.Sprintf("allscans-b-%d", time.Now().UnixNano())
	store := New(pool)
	t.Cleanup(func() {
		for _, s := range []string{scanA, scanB} {
			_, _ = pool.Exec(context.Background(), `DELETE FROM info_item_edges WHERE scan_id=$1`, s)
			_, _ = pool.Exec(context.Background(), `DELETE FROM info_items WHERE scan_id=$1`, s)
			_, _ = pool.Exec(context.Background(), `DELETE FROM info_audit_records WHERE scan_id=$1`, s)
			_, _ = pool.Exec(context.Background(), `DELETE FROM info_sync_watermarks WHERE scan_id=$1`, s)
		}
	})

	itemA := testInfoItem(scanA, "ii-a-1", 1, infoitem.NatureObserved, nil)
	itemA.Coordinates.Subject.Identifier = "/orders"
	if err := store.Apply(ctx, domainjournal.Record{
		ScanID: scanA, Seq: 1, At: time.Now().UTC(),
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent-1"},
		Operation: domainjournal.OpInfoItemProjected, InfoItem: &itemA,
	}); err != nil {
		t.Fatal(err)
	}

	// AllScans crosses the scan boundary.
	all, err := store.Region(ctx, infoitem.RegionQuery{AllScans: true, Subject: "/orders", Limit: 10})
	if err != nil {
		t.Fatalf("all-scans region: %v", err)
	}
	found := false
	for _, it := range all.Items {
		if it.ScanID == scanA && it.ID == itemA.ID {
			found = true
		}
		if it.ScanID != scanA {
			t.Fatalf("all-scans query returned a foreign row without cleanup: %+v", it)
		}
	}
	if !found {
		t.Fatalf("all-scans region missed the item from %s: %d items", scanA, len(all.Items))
	}

	// The per-scan form stays scoped: scanB's view must not see scanA's item.
	scoped, err := store.Region(ctx, infoitem.RegionQuery{ScanIDs: []string{scanB}, Subject: "/orders", Limit: 10})
	if err != nil {
		t.Fatalf("scoped region: %v", err)
	}
	if len(scoped.Items) != 0 {
		t.Fatalf("scoped region leaked items: %+v", scoped.Items)
	}
}

// Keyset paging must page through rows that share observed_at without skipping
// or repeating them (the ordering is observed_at DESC, id ASC), and association
// hits must carry the real per-hop relation type.
func TestStoreRegionKeysetPagingAndAssociationRels(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	scanID := fmt.Sprintf("keyset-%d", time.Now().UnixNano())
	store := New(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_item_edges WHERE scan_id=$1`, scanID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_items WHERE scan_id=$1`, scanID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_audit_records WHERE scan_id=$1`, scanID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM info_sync_watermarks WHERE scan_id=$1`, scanID)
	})

	// All three items share one observed_at on purpose: the tie-break is id ASC.
	observedAt := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	ids := []string{"k-1", "k-2", "k-3"}
	for i, id := range ids {
		item := testInfoItem(scanID, id, uint64(i+1), infoitem.NatureObserved, nil)
		item.Coordinates.ObservedAt = observedAt
		rec := domainjournal.Record{
			ScanID: scanID, Seq: uint64(i + 1), At: observedAt,
			Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "keyset-test"},
			Operation: domainjournal.OpInfoItemProjected, InfoItem: &item,
		}
		if i == 1 {
			// k-2 --supports--> k-1 supplies the hop type assertion below.
			rec.Relations = []infoitem.Relation{{
				ScanID: scanID, SourceSeq: uint64(i + 1), FromID: "k-2", ToID: "k-1",
				Type: infoitem.RelationSupports, Weight: 0.9, CreatedAt: observedAt,
			}}
		}
		if err := store.Apply(ctx, rec); err != nil {
			t.Fatalf("apply %s: %v", id, err)
		}
	}

	page1, err := store.Region(ctx, infoitem.RegionQuery{ScanIDs: []string{scanID}, Limit: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Items) != 2 || page1.Items[0].ID != "k-1" || page1.Items[1].ID != "k-2" {
		t.Fatalf("page1 items = %+v", page1.Items)
	}
	if !page1.HasMore || !page1.Truncated || page1.NextCursor == "" {
		t.Fatalf("page1 paging = %+v", page1)
	}

	cursor, err := infoitem.ParseRegionCursor(page1.NextCursor)
	if err != nil {
		t.Fatalf("parse next_cursor: %v", err)
	}
	page2, err := store.Region(ctx, infoitem.RegionQuery{
		ScanIDs: []string{scanID}, Limit: 2, Cursor: cursor,
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Items) != 1 || page2.Items[0].ID != "k-3" {
		t.Fatalf("page2 items = %+v", page2.Items)
	}
	if page2.HasMore || page2.NextCursor != "" {
		t.Fatalf("page2 must be the last page: %+v", page2)
	}

	assoc, err := store.Associate(ctx, infoitem.AssociationQuery{
		ScanID: scanID, StartIDs: []string{"k-1"}, MaxDepth: 2, Limit: 10,
	})
	if err != nil {
		t.Fatalf("associate: %v", err)
	}
	if len(assoc.Hits) != 1 || assoc.Hits[0].Item.ID != "k-2" {
		t.Fatalf("association hits = %+v", assoc.Hits)
	}
	hit := assoc.Hits[0]
	if len(hit.Rels) != len(hit.Path)-1 {
		t.Fatalf("rels/path mismatch: path=%v rels=%v", hit.Path, hit.Rels)
	}
	if len(hit.Rels) != 1 || hit.Rels[0] != infoitem.RelationSupports {
		t.Fatalf("hop type = %v, want [supports]", hit.Rels)
	}
}
