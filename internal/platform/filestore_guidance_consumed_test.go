package platform

// filestore_guidance_consumed_test.go — pins the idempotence contract the
// guidance consumed closed loop (REDESIGN §5.2, guidance_observer.go) relies
// on: repeated consumed flips keep the first consumed_at (the store never
// fabricates a fresh timestamp for an already-consumed row), so watcher
// retries and external replays cannot rewrite consumption history.

import (
	"context"
	"testing"
	"time"
)

func TestUpdateGuidanceStatusConsumedIdempotent(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "consumed idempotence"})
	if err != nil {
		t.Fatal(err)
	}
	g, created, err := store.CreateGuidance(ctx, GuidanceInput{
		TaskID:    task.ID,
		Message:   "check the staging host",
		RequestID: "req-idem",
	})
	if err != nil || !created {
		t.Fatalf("create guidance: created=%v err=%v", created, err)
	}
	if g.ConsumedAt != nil {
		t.Fatalf("fresh guidance carries consumed_at %v", g.ConsumedAt)
	}

	if _, err := store.UpdateGuidanceStatus(ctx, g.ID, GuidanceInjected); err != nil {
		t.Fatal(err)
	}
	consumed, err := store.UpdateGuidanceStatus(ctx, g.ID, GuidanceConsumed)
	if err != nil || consumed.Status != GuidanceConsumed || consumed.ConsumedAt == nil {
		t.Fatalf("first consumed flip = %+v err=%v, want consumed with consumed_at", consumed, err)
	}
	first := *consumed.ConsumedAt

	time.Sleep(5 * time.Millisecond) // a second flip must not advance the timestamp
	again, err := store.UpdateGuidanceStatus(ctx, g.ID, GuidanceConsumed)
	if err != nil || again.Status != GuidanceConsumed {
		t.Fatalf("repeat consumed flip = %+v err=%v", again, err)
	}
	if again.ConsumedAt == nil || !again.ConsumedAt.Equal(first) {
		t.Errorf("repeat consumed flip moved consumed_at: %v -> %v", first, *again.ConsumedAt)
	}
}
