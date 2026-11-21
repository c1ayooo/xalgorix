package coldpath

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xalgord/xalgorix/v4/internal/domainjournal"
	"github.com/xalgord/xalgorix/v4/internal/infoitem"
	"github.com/xalgord/xalgorix/v4/internal/infoitem/postgres"
	"github.com/xalgord/xalgorix/v4/internal/jobs"
)

// crashRecoveryPool connects to the integration database. The crash-recovery
// acceptance (charter §10 / M3) requires real PostgreSQL: fakes cannot model
// the "JSON written, enqueue lost" window.
func crashRecoveryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set; crash-recovery acceptance needs PostgreSQL")
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

func waitForWatermark(t *testing.T, store *postgres.Store, scanID string, want uint64, timeout time.Duration) postgres.Watermark {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last postgres.Watermark
	for {
		wm, err := store.Watermark(context.Background(), scanID)
		if err != nil {
			t.Fatalf("watermark: %v", err)
		}
		last = wm
		if wm.AppliedSeq >= want && wm.Status == "applied" {
			return wm
		}
		if time.Now().After(deadline) {
			t.Fatalf("watermark did not reach %d within %s: %+v", want, timeout, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func countScanRows(t *testing.T, pool *pgxpool.Pool, table, scanID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE scan_id = $1`, table), scanID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestCrashRecoveryReconcilesUnenqueuedJournal is the M3 crash-recovery
// acceptance: the process dies after the journal (JSON) is durable but BEFORE
// anything reaches River/PostgreSQL. On restart the reconciler must detect
// journal(last) > watermark(applied) and enqueue the missing range; River
// applies it; the watermark advances to exactly the journal position; replay
// must be idempotent (no duplicate InfoItems, no watermark regression).
func TestCrashRecoveryReconcilesUnenqueuedJournal(t *testing.T) {
	pool := crashRecoveryPool(t)
	ctx := context.Background()
	scanID := fmt.Sprintf("crashrec-%d", time.Now().UnixNano())

	dataDir := t.TempDir()
	scanDir := filepath.Join(dataDir, scanID)
	if err := os.MkdirAll(scanDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"info_item_edges", "info_items", "info_audit_records", "info_sync_watermarks"} {
			_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DELETE FROM %s WHERE scan_id=$1`, table), scanID)
		}
	})

	j, err := domainjournal.Open(filepath.Join(scanDir, domainjournal.FileName), scanID)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(hypID string) domainjournal.Record {
		rec, err := j.Append(domainjournal.Record{
			Actor:        domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "hook.ledger_seed"},
			Operation:    domainjournal.OpSeedHypothesis,
			HypothesisID: hypID,
			After:        &domainjournal.State{Status: "queued", Confidence: 0.5},
			Summary:      "seed " + hypID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	seed("H-1")
	// The observed-evidence record carries a real InfoItem payload so the cold
	// store gains a queryable row; Append stamps ScanID/SourceSeq.
	item := infoitem.NewEvidenceItem(scanID, "EV-1", infoitem.NatureObserved,
		infoitem.Content{Text: "admin panel reachable without authentication at /admin", MediaType: "text/plain"},
		infoitem.Source{Kind: "tool", Name: "http_request"},
		infoitem.Subject{Kind: "endpoint", Identifier: "/admin"},
		time.Time{}, 0)
	if _, err := j.Append(domainjournal.Record{
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent_1", Source: "ledger_tool"},
		Operation:    domainjournal.OpAddEvidence,
		EvidenceID:   "EV-1",
		HypothesisID: "H-1",
		InfoItem:     &item,
		Summary:      "evidence EV-1 observed",
	}); err != nil {
		t.Fatal(err)
	}
	seed("H-2")

	store := postgres.New(pool)
	// The crash window precondition: journal durable, PostgreSQL untouched.
	wm, err := store.Watermark(ctx, scanID)
	if err != nil {
		t.Fatal(err)
	}
	if wm.AppliedSeq != 0 || wm.Status != "pending" {
		t.Fatalf("precondition: watermark = %+v, want applied_seq 0 / pending", wm)
	}

	// Build the same stack Service.Start would run: River client + reconciler.
	processor, err := NewProcessor(dataDir, store)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(ctx, pool, processor)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
	t.Cleanup(func() { _ = manager.Stop(stopCtx); stopCancel() })
	reconciler, err := NewReconciler(dataDir, store, manager)
	if err != nil {
		t.Fatal(err)
	}

	// One reconciler pass must enqueue the missing 1..3 range; River applies it.
	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	waitForWatermark(t, store, scanID, 3, 15*time.Second)

	// The projected InfoItem is queryable through the Region activation query
	// with full provenance (source, nature, subject).
	region, err := store.Region(ctx, infoitem.RegionQuery{
		ScanIDs: []string{scanID},
		Subject: "/admin",
		Nature:  infoitem.NatureObserved,
	})
	if err != nil {
		t.Fatalf("region query: %v", err)
	}
	if len(region.Items) != 1 {
		t.Fatalf("region items = %d, want exactly 1 (item=%+v)", len(region.Items), region.Items)
	}
	got := region.Items[0]
	if got.Coordinates.Source.EvidenceID != "EV-1" || got.Nature != infoitem.NatureObserved ||
		got.Coordinates.Subject.Identifier != "/admin" {
		t.Fatalf("provenance lost: %+v", got)
	}

	// Idempotent replay: reconciling again and re-delivering the same range
	// must not duplicate rows or move the watermark.
	itemsBefore := countScanRows(t, pool, "info_items", scanID)
	edgesBefore := countScanRows(t, pool, "info_item_edges", scanID)
	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if err := manager.EnqueueRange(ctx, scanID, scanID, 1, 3); err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}
	wm2 := waitForWatermark(t, store, scanID, 3, 15*time.Second)
	if wm2.AppliedSeq != 3 {
		t.Fatalf("watermark regressed or advanced past the journal: %+v", wm2)
	}
	if n := countScanRows(t, pool, "info_items", scanID); n != itemsBefore {
		t.Fatalf("info_items count changed after replay: %d → %d (duplicate delivery is not idempotent)", itemsBefore, n)
	}
	if n := countScanRows(t, pool, "info_item_edges", scanID); n != edgesBefore {
		t.Fatalf("info_item_edges count changed after replay: %d → %d", edgesBefore, n)
	}
}
