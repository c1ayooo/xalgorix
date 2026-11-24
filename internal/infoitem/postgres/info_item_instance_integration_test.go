package postgres

// info_item_instance_integration_test.go — the write path stamps instance_id
// from run_journal so the unified view's run_id is indexable without a lateral
// join at query time.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

func TestInfoItemWriteStampsInstanceIDAndViewRunID(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	runID := fmt.Sprintf("instance-stamp-%d", time.Now().UnixNano())
	ctxID := runID + "-ctx"

	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM info_item_edges WHERE scan_id = ANY($1)`,
			`DELETE FROM info_items WHERE scan_id = ANY($1)`,
			`DELETE FROM run_journal WHERE scan_id = ANY($1)`,
			`DELETE FROM info_audit_records WHERE scan_id = ANY($1)`,
			`DELETE FROM info_sync_watermarks WHERE scan_id = ANY($1)`,
		} {
			if _, err := pool.Exec(ctx, stmt, []string{runID, ctxID}); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO run_journal (scan_id, instance_id, seq, record)
		VALUES ($1, $2, 1, '{}')`, ctxID, runID); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	item := testInfoItem(ctxID, "stamped", 1, infoitem.NatureObserved, nil)
	store := New(pool)
	if err := store.Apply(ctx, domainjournal.Record{
		ScanID:    ctxID,
		Seq:       1,
		At:        time.Now().UTC(),
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "instance-stamp-test"},
		Operation: domainjournal.OpInfoItemProjected,
		InfoItem:  &item,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var instanceID string
	if err := pool.QueryRow(ctx, `
		SELECT instance_id FROM info_items WHERE scan_id = $1 AND id = 'stamped'`, ctxID).
		Scan(&instanceID); err != nil {
		t.Fatalf("read instance_id: %v", err)
	}
	if instanceID != runID {
		t.Fatalf("instance_id = %q, want %q", instanceID, runID)
	}

	var viewRunID string
	if err := pool.QueryRow(ctx, `
		SELECT run_id FROM info_space_items WHERE info_id = 'stamped'`).Scan(&viewRunID); err != nil {
		t.Fatalf("read view run_id: %v", err)
	}
	if viewRunID != runID {
		t.Fatalf("view run_id = %q, want %q", viewRunID, runID)
	}
}
