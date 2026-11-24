package graph

import (
	"context"
	"testing"
	"time"
)

// TestPGSettleIntentOwnerFenceAndAtomicReason pins the two properties the
// orchestrator relies on after the 2026-11-24 control review: an old worker
// whose expired lease was reclaimed by a new claimant cannot rewrite the new
// running row, and a successful settlement writes the reason in the same
// statement as the terminal state.
func TestPGSettleIntentOwnerFenceAndAtomicReason(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	sid := scanID(t)
	cleanupScan(t, store, sid)

	intent, err := store.AddNode(ctx, NodeInput{
		ScanID: sid, Kind: KindIntent, UnitKey: "T-settle",
		Payload: `{"task_id":"T-settle"}`,
	})
	if err != nil {
		t.Fatalf("add intent: %v", err)
	}
	first, err := store.ClaimNextIntent(ctx, sid, "work#1", 50*time.Millisecond)
	if err != nil || first == nil {
		t.Fatalf("first claim: %v %+v", err, first)
	}
	time.Sleep(120 * time.Millisecond)
	second, err := store.ClaimNextIntent(ctx, sid, "work#2", time.Minute)
	if err != nil || second == nil {
		t.Fatalf("second claim after expiry: %v %+v", err, second)
	}

	moved, err := store.SettleIntent(ctx, sid, intent.ID, "work#1", StateBlocked, "stale worker")
	if err != nil {
		t.Fatalf("stale settle: %v", err)
	}
	if moved {
		t.Fatal("stale worker settlement must be fenced out")
	}
	live, err := store.GetNode(ctx, sid, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if live.State != StateRunning || live.Owner != "work#2" || live.BlockedReason != "" {
		t.Fatalf("new owner row was rewritten: %+v", live)
	}

	moved, err = store.SettleIntent(ctx, sid, intent.ID, "work#2", StateExhausted, "no information-layer progress")
	if err != nil || !moved {
		t.Fatalf("owner settle = %v, %v", moved, err)
	}
	settled, err := store.GetNode(ctx, sid, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != StateExhausted || settled.Owner != "" || settled.LeaseUntil != nil || settled.CompletedAt == nil {
		t.Fatalf("settled row = %+v", settled)
	}
	if settled.BlockedReason != "no information-layer progress" {
		t.Fatalf("reason not written atomically: %q", settled.BlockedReason)
	}

	if _, err := store.SettleIntent(ctx, sid, intent.ID, "work#2", StateDone, "x"); err == nil {
		t.Fatal("settle to done must be refused")
	}
}
