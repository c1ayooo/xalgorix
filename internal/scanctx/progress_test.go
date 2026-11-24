package scanctx

import (
	"sync"
	"testing"
	"time"
)

func TestProgressStoreUnknownAgent(t *testing.T) {
	var nilStore *ProgressStore
	// Nil-safe: every method must be callable on a nil store.
	nilStore.Touch("x")
	nilStore.MarkProgress("x")
	if _, _, ok := nilStore.AgentTimes("x"); ok {
		t.Fatal("nil store reported a known agent")
	}
	if got := nilStore.Snapshot(); got != nil {
		t.Fatalf("nil snapshot = %v", got)
	}
	nilStore.BindUnit("u", "a")
	if _, ok := nilStore.UnitAgent("u"); ok {
		t.Fatal("nil store bound a unit")
	}

	store := NewProgressStore()
	if _, _, ok := store.AgentTimes("missing"); ok {
		t.Fatal("unknown agent reported as known")
	}
	if _, _, ok := store.AgentTimes("  "); ok {
		t.Fatal("blank agent reported as known")
	}
	store.Touch("")
	store.MarkProgress("")
	if got := store.Snapshot(); len(got) != 0 {
		t.Fatalf("blank ids stored: %v", got)
	}
}

func TestProgressStoreMonotonic(t *testing.T) {
	store := NewProgressStore()
	store.Touch("agent-a")
	touch1, progress1, ok := store.AgentTimes("agent-a")
	if !ok {
		t.Fatal("agent-a not found after Touch")
	}
	if !progress1.IsZero() {
		t.Fatalf("Touch must not count as progress: %v", progress1)
	}

	store.MarkProgress("agent-a")
	_, progress2, _ := store.AgentTimes("agent-a")
	if progress2.IsZero() || progress2.Before(touch1) {
		t.Fatalf("progress = %v, touch = %v", progress2, touch1)
	}
	store.MarkProgress("agent-a")
	_, progress3, _ := store.AgentTimes("agent-a")
	if progress3.Before(progress2) {
		t.Fatalf("progress moved backwards: %v -> %v", progress2, progress3)
	}
	entry := store.Snapshot()["agent-a"]
	if entry.ProgressCount != 2 {
		t.Fatalf("progress count = %d, want 2", entry.ProgressCount)
	}
	if entry.Progress.IsZero() || entry.Touch.IsZero() {
		t.Fatalf("snapshot entry missing timestamps: %+v", entry)
	}

	// Progress is the information-layer clock; read-only touches after it must
	// not fake a newer progress time.
	store.Touch("agent-a")
	_, progress4, _ := store.AgentTimes("agent-a")
	if progress4 != progress3 {
		t.Fatalf("Touch changed the progress clock: %v -> %v", progress3, progress4)
	}
}

func TestProgressStoreConcurrent(t *testing.T) {
	store := NewProgressStore()
	const workers = 16
	const iterations = 200
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				store.Touch(id)
				if j%2 == 0 {
					store.MarkProgress(id)
				}
				_ = store.Snapshot()
				store.BindUnit("unit-"+id, id)
				_, _ = store.UnitAgent("unit-" + id)
			}
		}(string(rune('a' + i)))
	}
	close(start)
	wg.Wait()

	snapshot := store.Snapshot()
	if len(snapshot) != workers {
		t.Fatalf("agents = %d, want %d", len(snapshot), workers)
	}
	for id, entry := range snapshot {
		if entry.ProgressCount != iterations/2 {
			t.Fatalf("%s progress count = %d, want %d", id, entry.ProgressCount, iterations/2)
		}
	}
	for i := 0; i < workers; i++ {
		id := string(rune('a' + i))
		if got, ok := store.UnitAgent("unit-" + id); !ok || got != id {
			t.Fatalf("unit-%s bound to %q ok=%v", id, got, ok)
		}
	}
}

func TestProgressStoreRecentProgressWindow(t *testing.T) {
	store := NewProgressStore()
	store.MarkProgress("worker-1")
	_, progress, ok := store.AgentTimes("worker-1")
	if !ok {
		t.Fatal("worker-1 not found")
	}
	if time.Since(progress) > 2*time.Minute {
		t.Fatalf("fresh progress looked stale: %v", time.Since(progress))
	}
	if _, _, ok := store.AgentTimes("worker-2"); ok {
		t.Fatal("unrelated worker reported as known")
	}
}
