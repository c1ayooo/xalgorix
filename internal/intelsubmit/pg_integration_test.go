package intelsubmit

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// PG integration: the SQL holds the invariants that matter most for scoring —
// a scored challenge is never downgraded by a later start/close, awarded only
// grows, and a closed container cannot leave a pending debt behind.
func TestPGStoreReceiptLifecycle(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	taskID := fmt.Sprintf("intelsubmit-task-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM intel_submit WHERE task_id = $1`, taskID)
	})

	// 1) start creates a pending receipt with the container endpoint.
	if _, err := store.Upsert(ctx, UpsertInput{
		TaskID: taskID, UniqueCode: "a-05", Endpoint: "10.0.177.16:80",
		State: StatePending, Response: "container started",
	}); err != nil {
		t.Fatalf("start upsert: %v", err)
	}
	pending, err := store.ListPending(ctx, taskID, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].UniqueCode != "a-05" || pending[0].Endpoint != "10.0.177.16:80" {
		t.Fatalf("pending = %+v", pending)
	}

	// 2) a correct submission scores it and records the awarded points.
	sub, err := store.Upsert(ctx, UpsertInput{
		TaskID: taskID, UniqueCode: "a-05", State: StateSubmitted,
		Awarded: 100, Attempted: true, Response: `{"correct":true,"awarded":100}`,
	})
	if err != nil {
		t.Fatalf("submit upsert: %v", err)
	}
	if sub.State != StateSubmitted || sub.Awarded != 100 || sub.Attempts != 1 {
		t.Fatalf("submit receipt = %+v", sub)
	}

	// 3) a later start (same challenge, new container) must NOT downgrade it.
	again, err := store.Upsert(ctx, UpsertInput{
		TaskID: taskID, UniqueCode: "a-05", Endpoint: "10.0.177.19:80",
		State: StatePending,
	})
	if err != nil {
		t.Fatalf("restart upsert: %v", err)
	}
	if again.State != StateSubmitted {
		t.Fatalf("scored receipt was downgraded to %q", again.State)
	}

	// 4) close must not invalidate a scored challenge either.
	if _, err := store.InvalidateByCode(ctx, taskID, "a-05", "container closed"); err != nil {
		t.Fatalf("invalidate scored: %v", err)
	}
	if got, ok, err := store.Get(ctx, taskID, "a-05"); err != nil || !ok {
		t.Fatalf("read back: %v %v", ok, err)
	} else if got.State != StateSubmitted {
		t.Fatalf("close downgraded a scored challenge to %q", got.State)
	}

	// 5) an unsubmitted challenge that is closed becomes invalid, not a debt.
	if _, err := store.Upsert(ctx, UpsertInput{
		TaskID: taskID, UniqueCode: "a-07", Endpoint: "10.0.177.18:80",
		State: StatePending,
	}); err != nil {
		t.Fatalf("pending a-07: %v", err)
	}
	if n, err := store.InvalidateByCode(ctx, taskID, "a-07", "container closed"); err != nil || n != 1 {
		t.Fatalf("invalidate pending = %d, %v", n, err)
	}
	pending, err = store.ListPending(ctx, taskID, 10)
	if err != nil {
		t.Fatalf("list pending after close: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("closed challenge still reported pending: %+v", pending)
	}

	// 6) a wrong flag invalidates, and awarded never regresses.
	if _, err := store.Upsert(ctx, UpsertInput{
		TaskID: taskID, UniqueCode: "a-09", State: StateInvalid,
		Attempted: true, Response: `{"correct":false,"awarded":0}`,
	}); err != nil {
		t.Fatalf("invalid upsert: %v", err)
	}
	// 7) duplicate keeps its own state rather than pretending a new score.
	if _, err := store.Upsert(ctx, UpsertInput{
		TaskID: taskID, UniqueCode: "a-09", State: StateDuplicate,
		Attempted: true, Response: `{"code":"duplicate"}`,
	}); err != nil {
		t.Fatalf("duplicate upsert: %v", err)
	}
	all, err := store.ListByTask(ctx, taskID, 20)
	if err != nil {
		t.Fatalf("list by task: %v", err)
	}
	byCode := map[string]Submit{}
	for _, r := range all {
		byCode[r.UniqueCode] = r
	}
	if byCode["a-09"].State != StateDuplicate {
		t.Fatalf("a-09 state = %q, want duplicate", byCode["a-09"].State)
	}
	if byCode["a-05"].Awarded != 100 {
		t.Fatalf("a-05 awarded regressed to %d", byCode["a-05"].Awarded)
	}
	if byCode["a-05"].Attempts != 1 {
		t.Fatalf("a-05 attempts = %d, want 1 (start/close are not attempts)", byCode["a-05"].Attempts)
	}
}

// TestPendingDigestNeedsAStore pins that the gate stays silent without PG
// rather than inventing work for legacy runs.
func TestPendingDigestNeedsAStore(t *testing.T) {
	SetStore(nil)
	if got := PendingDigest(context.Background(), "task-x", 10); got != "" {
		t.Fatalf("digest without store = %q", got)
	}
}
