package graph

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSettleIntentFencesOnOwner pins the lease-handover invariant: a worker
// whose expired lease was reclaimed by a new claimant must not be able to
// rewrite the new worker's running row.
func TestSettleIntentFencesOnOwner(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	scanID := "settle-fence"
	addIntentNode(t, store, scanID, "unit-a", `{"task_id":"unit-a"}`)

	first, err := store.ClaimNextIntent(ctx, scanID, "work#1", 10*time.Millisecond)
	if err != nil || first == nil {
		t.Fatalf("first claim: %v %v", first, err)
	}
	time.Sleep(40 * time.Millisecond)
	second, err := store.ClaimNextIntent(ctx, scanID, "work#2", time.Minute)
	if err != nil || second == nil {
		t.Fatalf("second claim: %v %v", second, err)
	}

	moved, err := store.SettleIntent(ctx, scanID, first.ID, "work#1", StateBlocked, "stale worker")
	if err != nil {
		t.Fatalf("stale settle: %v", err)
	}
	if moved {
		t.Fatal("stale worker settlement must be fenced out")
	}
	got, err := store.GetNode(ctx, scanID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateRunning || got.Owner != "work#2" || got.BlockedReason != "" {
		t.Fatalf("new owner row was rewritten: state=%s owner=%q reason=%q", got.State, got.Owner, got.BlockedReason)
	}

	moved, err = store.SettleIntent(ctx, scanID, second.ID, "work#2", StateExhausted, "no progress")
	if err != nil || !moved {
		t.Fatalf("owner settle = %v, %v", moved, err)
	}
	got, _ = store.GetNode(ctx, scanID, second.ID)
	if got.State != StateExhausted || got.Owner != "" || got.LeaseUntil != nil {
		t.Fatalf("settled row = state %s owner %q lease %v", got.State, got.Owner, got.LeaseUntil)
	}
	if !strings.Contains(got.BlockedReason, "no progress") {
		t.Fatalf("reason not written atomically: %q", got.BlockedReason)
	}
}

// TestSettleIntentOpenReleasesClaim pins the worker-missing path: a missing
// worker releases the claim for retry without writing a terminal reason.
func TestSettleIntentOpenReleasesClaim(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	scanID := "settle-open"
	addIntentNode(t, store, scanID, "unit-b", `{"task_id":"unit-b"}`)
	node, err := store.ClaimNextIntent(ctx, scanID, "work#1", time.Minute)
	if err != nil || node == nil {
		t.Fatalf("claim: %v %v", node, err)
	}
	moved, err := store.SettleIntent(ctx, scanID, node.ID, "work#1", StateOpen, "worker missing")
	if err != nil || !moved {
		t.Fatalf("settle open = %v, %v", moved, err)
	}
	got, _ := store.GetNode(ctx, scanID, node.ID)
	if got.State != StateOpen || got.Owner != "" || got.LeaseUntil != nil {
		t.Fatalf("open row = state %s owner %q lease %v", got.State, got.Owner, got.LeaseUntil)
	}
	if got.BlockedReason != "" {
		t.Fatalf("open retry must not carry a terminal reason: %q", got.BlockedReason)
	}
	if got.CompletedAt != nil {
		t.Fatalf("open retry must clear completed_at")
	}
}

// TestSettleIntentRefusesDoneAndMissingOwner pins the two closed doors: done
// only through ConcludeIntent, and no owner means no settlement.
func TestSettleIntentRefusesDoneAndMissingOwner(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	scanID := "settle-guards"
	addIntentNode(t, store, scanID, "unit-c", `{"task_id":"unit-c"}`)
	node, _ := store.ClaimNextIntent(ctx, scanID, "work#1", time.Minute)
	if node == nil {
		t.Fatal("claim returned nil")
	}
	if _, err := store.SettleIntent(ctx, scanID, node.ID, "work#1", StateDone, "x"); err == nil {
		t.Fatal("settle to done must be refused")
	}
	if _, err := store.SettleIntent(ctx, scanID, node.ID, "  ", StateBlocked, "x"); err == nil {
		t.Fatal("settle without owner must be refused")
	}
}
