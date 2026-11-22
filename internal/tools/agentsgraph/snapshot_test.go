package agentsgraph

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSnapshotReportsConcurrentAndQueuedAgents pins the projection panels read:
// with a concurrency limit of 2 and three spawns, exactly two agents are
// running and the third reports a queue position of 1. This is the state the
// session page must render as "运行中 2/2 · 排队 1" rather than three identical
// spinners.
func TestSnapshotReportsConcurrentAndQueuedAgents(t *testing.T) {
	release := make(chan struct{})
	graph := NewWithConfig(context.Background(), 6, 2, func(_ context.Context, _ string, _ string, _ []string, _ string) (string, error) {
		<-release
		return "done", nil
	})
	t.Cleanup(graph.Stop)

	var ids []string
	for i := 0; i < 3; i++ {
		res, err := graph.spawnAgent(map[string]string{"name": "lane", "task": "work"})
		if err != nil {
			t.Fatalf("spawnAgent %d: %v", i, err)
		}
		ids = append(ids, agentIDFromResult(t, res.Metadata))
	}

	waitFor(t, func() bool {
		view := graph.Snapshot()
		return view.Running == 2 && view.Queued == 1
	})

	view := graph.Snapshot()
	if view.Limit != 2 {
		t.Fatalf("limit = %d, want 2", view.Limit)
	}
	if view.MaxAgents != 6 {
		t.Fatalf("max agents = %d, want 6", view.MaxAgents)
	}
	var queued *AgentState
	for i := range view.Agents {
		switch view.Agents[i].Status {
		case "queued":
			if queued != nil {
				t.Fatalf("more than one queued agent: %#v", view.Agents)
			}
			queued = &view.Agents[i]
		}
	}
	if queued == nil || queued.QueuedPosition != 1 {
		t.Fatalf("queued agent missing or wrong position: %#v", view.Agents)
	}
	if queued.Lane != "lane" {
		t.Fatalf("lane = %q, want lane", queued.Lane)
	}

	// Completion promotes the queued agent without exceeding the limit.
	close(release)
	waitFor(t, func() bool { return graph.Snapshot().Queued == 0 })
	waitFor(t, func() bool {
		done := 0
		for _, agent := range graph.Snapshot().Agents {
			if agent.Status == "completed" {
				done++
			}
		}
		return done == 3
	})
	if got := graph.Snapshot().Running; got != 0 {
		t.Fatalf("running after completion = %d, want 0", got)
	}
}

// TestStateSinkEmitsLifecycleEvents pins that the best-effort invalidation
// channel reports admission, promotion and completion without becoming the
// authority (snapshot stays consistent throughout).
func TestStateSinkEmitsLifecycleEvents(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var kinds []string
	graph := NewWithConfig(context.Background(), 4, 1, func(_ context.Context, _ string, _ string, _ []string, _ string) (string, error) {
		<-release
		return "done", nil
	})
	t.Cleanup(graph.Stop)
	graph.SetStateSink(func(ev StateEvent) {
		mu.Lock()
		kinds = append(kinds, ev.Kind)
		mu.Unlock()
		if ev.Kind == "" || ev.AgentID == "" || ev.Lane == "" {
			t.Errorf("incomplete state event: %#v", ev)
		}
	})

	for i := 0; i < 2; i++ {
		if _, err := graph.spawnAgent(map[string]string{"name": "lane", "task": "work"}); err != nil {
			t.Fatalf("spawnAgent %d: %v", i, err)
		}
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return containsKind(kinds, "agent_queued")
	})
	close(release)
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return countKind(kinds, "agent_completed") == 2
	})
	mu.Lock()
	defer mu.Unlock()
	if !containsKind(kinds, "agent_started") {
		t.Fatalf("missing agent_started: %#v", kinds)
	}
}

// TestReadCheckpointStateProjectsPersistedAgents pins the terminal fallback:
// after the live agent is released the same durable file the graph already
// writes is readable as a projection, and a persisted "running" row is not
// replayed as live work.
func TestReadCheckpointStateProjectsPersistedAgents(t *testing.T) {
	dir := t.TempDir()
	graph := NewWithConfig(context.Background(), 4, 2, func(_ context.Context, _ string, _ string, _ []string, _ string) (string, error) {
		return "done", nil
	})
	if err := graph.RestoreCheckpoint(dir); err != nil {
		t.Fatalf("RestoreCheckpoint: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := graph.spawnAgent(map[string]string{"name": "lane", "task": "work"}); err != nil {
			t.Fatalf("spawnAgent %d: %v", i, err)
		}
	}
	graph.WaitStopped(5 * time.Second)
	if err := graph.SaveCheckpoint(); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "execution-graph.json")); err != nil {
		t.Fatalf("checkpoint not written: %v", err)
	}

	view, ok := ReadCheckpointState(dir)
	if !ok {
		t.Fatal("ReadCheckpointState returned no state")
	}
	if len(view.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(view.Agents))
	}
	for _, agent := range view.Agents {
		if agent.Status != "completed" {
			t.Fatalf("persisted completed agent replayed as %q", agent.Status)
		}
		if agent.Lane != "lane" || agent.AgentID == "" {
			t.Fatalf("incomplete persisted projection: %#v", agent)
		}
	}
}

// TestReadCheckpointStateDowngradesStaleRunning keeps honesty: a checkpoint
// written mid-run cannot claim a worker is still working after the process (or
// the run) is gone.
func TestReadCheckpointStateDowngradesStaleRunning(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"version":1,"counter":1,"delegated":1,"agents":[{"id":"sub_1_1","name":"lane","task":"work","targets":["https://x"],"status":"running","started_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "execution-graph.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	view, ok := ReadCheckpointState(dir)
	if !ok || len(view.Agents) != 1 {
		t.Fatalf("unexpected projection: %#v ok=%v", view, ok)
	}
	if view.Agents[0].Status != "failed" {
		t.Fatalf("stale running projected as %q, want failed", view.Agents[0].Status)
	}
	if view.Running != 0 {
		t.Fatalf("running count = %d, want 0", view.Running)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}

func containsKind(kinds []string, kind string) bool { return countKind(kinds, kind) > 0 }

func countKind(kinds []string, kind string) int {
	count := 0
	for _, k := range kinds {
		if k == kind {
			count++
		}
	}
	return count
}
