package agentsgraph

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSetMaxConcurrentDrainsQueue(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	started := 0
	g := NewWithConfig(context.Background(), 4, 1, func(_ context.Context, agentID, name string, _ []string, _ string) (string, error) {
		mu.Lock()
		started++
		mu.Unlock()
		<-release
		return "ok", nil
	})
	defer g.Stop()

	id1, err := g.Dispatch("work#1", "one", []string{"10.0.1.5"}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := g.Dispatch("work#2", "two", []string{"10.0.1.6"}, "u2")
	if err != nil {
		t.Fatal(err)
	}
	waitRunning := func(want int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			g.mu.Lock()
			active := g.activeAgents
			g.mu.Unlock()
			if active == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		g.mu.Lock()
		active := g.activeAgents
		g.mu.Unlock()
		t.Fatalf("active = %d, want %d", active, want)
	}
	waitRunning(1)
	g.mu.Lock()
	active := g.activeAgents
	g.mu.Unlock()
	if active != 1 {
		t.Fatalf("initial active = %d, want 1", active)
	}
	g.SetMaxConcurrent(2)
	waitRunning(2)

	close(release)
	for _, id := range []string{id1, id2} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, status, errText := g.DispatchResult(ctx, id)
		cancel()
		if status != "completed" {
			t.Fatalf("%s status = %q %q", id, status, errText)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if started != 2 {
		t.Fatalf("runner started = %d, want 2", started)
	}
}
