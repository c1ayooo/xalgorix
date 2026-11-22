package agent

// A real-postgres bridge for the M4 cross-scan consumers. The stub-based
// contract tests above pin the query shapes, but a permissive stub cannot
// catch a consumer query that the real store would reject (the M4 review
// finding: a region query without scan_ids failed validation, so the
// annotation never rendered in production). These tests run the SAME consumer
// code against the SAME postgres.Store implementation the cold path registers,
// so the stub↔real gap stays closed.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem/postgres"
)

func crossScanPGPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set; the real-reader bridge needs PostgreSQL")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func crossScanApplyItem(t *testing.T, store *postgres.Store, scanID, evidenceID, subject string, seq uint64) infoitem.Item {
	t.Helper()
	item := infoitem.NewEvidenceItem(scanID, evidenceID, infoitem.NatureObserved,
		infoitem.Content{Text: "observation on " + subject + " from " + scanID, MediaType: "text/plain"},
		infoitem.Source{Kind: "tool", Name: "http_request"},
		infoitem.Subject{Kind: "endpoint", Identifier: subject},
		time.Now().UTC(), seq)
	if err := store.Apply(context.Background(), domainjournal.Record{
		ScanID: scanID, Seq: seq, At: time.Now().UTC(),
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent-1"},
		Operation: domainjournal.OpInfoItemProjected,
		InfoItem:  &item,
	}); err != nil {
		t.Fatalf("apply %s: %v", evidenceID, err)
	}
	return item
}

func crossScanCleanup(t *testing.T, pool *pgxpool.Pool, scanIDs ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, s := range scanIDs {
			for _, table := range []string{"info_item_edges", "info_items", "info_audit_records", "info_sync_watermarks"} {
				_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DELETE FROM %s WHERE scan_id=$1`, table), s)
			}
		}
	})
}

// fetchCrossScanHistory must produce a real result through the real store:
// the AllScans region query reaches another scan's items, and the current
// scan's same-subject item IDs come back as association roots.
func TestCrossScanHistoryAgainstRealStore(t *testing.T) {
	pool := crossScanPGPool(t)
	ctx := context.Background()
	store := postgres.New(pool)
	oldScan := fmt.Sprintf("cs-old-%d", time.Now().UnixNano())
	curScan := fmt.Sprintf("cs-cur-%d", time.Now().UnixNano())
	crossScanCleanup(t, pool, oldScan, curScan)

	crossScanApplyItem(t, store, oldScan, "EV-old-1", "/orders", 1)
	current := crossScanApplyItem(t, store, curScan, "EV-cur-1", "/orders", 1)

	cancel := infoitem.RegisterReader(store)
	defer cancel()

	hist, err := fetchCrossScanHistory(ctx, store, curScan, "/orders", crossScanRegionLimit)
	if err != nil {
		t.Fatalf("fetchCrossScanHistory: %v", err)
	}
	if hist.total != 1 || hist.observed != 1 || hist.inferred != 0 {
		t.Fatalf("prior-scan history = %+v, want exactly 1 observed item", hist)
	}
	if len(hist.scans) != 1 || hist.scans[0] != oldScan {
		t.Fatalf("scan labels = %v, want [%s]", hist.scans, oldScan)
	}
	if len(hist.currentIDs) != 1 || hist.currentIDs[0] != current.ID {
		t.Fatalf("current-scan roots = %v, want [%s]", hist.currentIDs, current.ID)
	}
}

// The full claim path against the real store: region history from the prior
// scan plus a per-scan edge traversal rooted at the current scan's item.
func TestClaimCrossScanNoteAgainstRealStore(t *testing.T) {
	pool := crossScanPGPool(t)
	ctx := context.Background()
	store := postgres.New(pool)
	oldScan := fmt.Sprintf("cs-old-%d", time.Now().UnixNano())
	curScan := fmt.Sprintf("cs-cur-%d", time.Now().UnixNano())
	crossScanCleanup(t, pool, oldScan, curScan)

	crossScanApplyItem(t, store, oldScan, "EV-old-1", "/orders", 1)
	root := crossScanApplyItem(t, store, curScan, "EV-cur-1", "/orders", 1)
	related := infoitem.NewEvidenceItem(curScan, "EV-cur-2", infoitem.NatureObserved,
		infoitem.Content{Text: "related observation", MediaType: "text/plain"},
		infoitem.Source{Kind: "tool", Name: "http_request"},
		infoitem.Subject{Kind: "endpoint", Identifier: "/orders"},
		time.Now().UTC(), 2)
	if err := store.Apply(ctx, domainjournal.Record{
		ScanID: curScan, Seq: 2, At: time.Now().UTC(),
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent-1"},
		Operation: domainjournal.OpInfoItemProjected,
		InfoItem:  &related,
		Relations: []infoitem.Relation{{
			ScanID: curScan, SourceSeq: 2,
			FromID: root.ID, ToID: related.ID,
			Type: infoitem.RelationReferences, CreatedAt: time.Now().UTC(),
		}},
	}); err != nil {
		t.Fatalf("apply related: %v", err)
	}

	cancel := infoitem.RegisterReader(store)
	defer cancel()

	a := newCrossScanAgent(t, curScan)
	seedSchedulableHypothesis(a.scanCtx, "idor", "/orders", 0.9)
	res, err := a.claimNextHypothesisTool(map[string]string{})
	if err != nil || res.Error != "" {
		t.Fatalf("claim failed: result=%+v err=%v", res, err)
	}

	if !strings.Contains(res.Output, "同主体跨扫描历史: 1 条既往观察 (observed=1 inferred=0), 扫描: "+oldScan) {
		t.Fatalf("cross-scan region line missing against the real store:\n%s", res.Output)
	}
	// Both same-subject items are roots and each is reachable from the other
	// via the one references edge, so the real traversal returns both.
	if !strings.Contains(res.Output, "当前扫描内关联可达项: 2 个 (沿边 ≤2 跳), 扫描: "+curScan) {
		t.Fatalf("edge traversal from the real per-scan edge table found nothing:\n%s", res.Output)
	}
}

// The same-shaped read_ledger call against the real store: the region note
// renders on the schedulable entry (the failure mode the M4 review caught was
// that this note never rendered in production).
func TestReadLedgerCrossScanNoteAgainstRealStore(t *testing.T) {
	pool := crossScanPGPool(t)
	store := postgres.New(pool)
	oldScan := fmt.Sprintf("cs-old-%d", time.Now().UnixNano())
	curScan := fmt.Sprintf("cs-cur-%d", time.Now().UnixNano())
	crossScanCleanup(t, pool, oldScan, curScan)

	crossScanApplyItem(t, store, oldScan, "EV-old-1", "/orders", 1)

	cancel := infoitem.RegisterReader(store)
	defer cancel()

	a := newCrossScanAgent(t, curScan)
	seedSchedulableHypothesis(a.scanCtx, "idor", "/orders", 0.9)
	res, err := a.readLedgerTool(map[string]string{"filter": "schedulable"})
	if err != nil || res.Error != "" {
		t.Fatalf("read_ledger failed: result=%+v err=%v", res, err)
	}
	if !strings.Contains(res.Output, "同端点跨扫描历史: 1 条既往观察 (observed=1 inferred=0), 扫描: "+oldScan) {
		t.Fatalf("region note missing against the real store:\n%s", res.Output)
	}
}
