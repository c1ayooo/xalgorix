package web

// workers_backend_test.go — durable worker history (run_workers, 0018).
//
// Two seams are pinned here:
//   - processEvent projects each delegated-agent lifecycle boundary into the
//     backend (the row the session page reads after the run is gone);
//   - instanceAgentsView prefers those rows over the graph checkpoint and
//     merges rows the live graph never saw (sibling sessions of one run).

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
)

type fakeWorkerBackend struct {
	mu   sync.Mutex
	rows map[string]runstate.WorkerRecord // key: instance/agent
}

func newFakeWorkerBackend() *fakeWorkerBackend {
	return &fakeWorkerBackend{rows: map[string]runstate.WorkerRecord{}}
}

func (f *fakeWorkerBackend) UpsertWorker(_ context.Context, rec runstate.WorkerRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := rec.InstanceID + "/" + rec.AgentID
	prev, ok := f.rows[key]
	if ok {
		if rec.Lane == "" {
			rec.Lane = prev.Lane
		}
		if rec.Task == "" {
			rec.Task = prev.Task
		}
		if rec.Target == "" {
			rec.Target = prev.Target
		}
		if rec.StartedAt.IsZero() {
			rec.StartedAt = prev.StartedAt
		}
	}
	f.rows[key] = rec
	return nil
}

func (f *fakeWorkerBackend) ListWorkersByInstance(_ context.Context, instanceID string) ([]runstate.WorkerRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runstate.WorkerRecord
	for _, rec := range f.rows {
		if rec.InstanceID == instanceID {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (f *fakeWorkerBackend) get(instanceID, agentID string) (runstate.WorkerRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.rows[instanceID+"/"+agentID]
	return rec, ok
}

func useFakeWorkerBackend(t *testing.T) *fakeWorkerBackend {
	t.Helper()
	fake := newFakeWorkerBackend()
	SetWorkerStateBackend(fake)
	t.Cleanup(func() { SetWorkerStateBackend(nil) })
	return fake
}

func waitForWorkerRow(t *testing.T, fake *fakeWorkerBackend, instanceID, agentID, status string) runstate.WorkerRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rec, ok := fake.get(instanceID, agentID); ok && rec.Status == status {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, ok := fake.get(instanceID, agentID)
	t.Fatalf("worker row %s/%s never reached %q (last=%#v ok=%v)", instanceID, agentID, status, rec, ok)
	return runstate.WorkerRecord{}
}

// TestPersistWorkerLifecycle_ProjectsBoundaries pins the write seam: started
// and completed notifications each upsert one row keyed by (instance, agent),
// and the spawn-time task/target survive the later completion notification.
func TestPersistWorkerLifecycle_ProjectsBoundaries(t *testing.T) {
	s := newTestServer(t, nil)
	fake := useFakeWorkerBackend(t)
	sess := &scanSession{id: "scan-1", instanceID: "run-1"}

	s.persistWorkerLifecycle(agent.Event{
		Type: "agent_started",
		Meta: map[string]any{
			"agent_id": "sub_1", "lane": "injection", "status": "running",
			"task": "test sqli on a-17", "target": "10.0.0.17",
		},
	}, sess)
	started := waitForWorkerRow(t, fake, "run-1", "sub_1", "running")
	if started.ScanID != "scan-1" || started.Lane != "injection" {
		t.Fatalf("row provenance lost: %#v", started)
	}
	if started.Task != "test sqli on a-17" || started.Target != "10.0.0.17" {
		t.Fatalf("spawn task/target lost: %#v", started)
	}
	if started.StartedAt.IsZero() || !started.FinishedAt.IsZero() {
		t.Fatalf("started row times wrong: %#v", started)
	}

	// The completion notification carries no task/target; the row must keep them.
	s.persistWorkerLifecycle(agent.Event{
		Type: "agent_completed",
		Meta: map[string]any{"agent_id": "sub_1", "lane": "injection", "status": "completed"},
	}, sess)
	done := waitForWorkerRow(t, fake, "run-1", "sub_1", "completed")
	if done.Task != "test sqli on a-17" || done.Target != "10.0.0.17" {
		t.Fatalf("completion erased spawn task/target: %#v", done)
	}
	if done.FinishedAt.IsZero() {
		t.Fatalf("completed row missing finished_at: %#v", done)
	}
}

// TestPersistWorkerLifecycle_IgnoresNonLifecycleEvents keeps the event path
// quiet: ordinary messages/tool results must not create worker rows.
func TestPersistWorkerLifecycle_IgnoresNonLifecycleEvents(t *testing.T) {
	s := newTestServer(t, nil)
	fake := useFakeWorkerBackend(t)
	sess := &scanSession{id: "scan-1", instanceID: "run-1"}
	s.persistWorkerLifecycle(agent.Event{Type: "message", Content: "hi"}, sess)
	s.persistWorkerLifecycle(agent.Event{
		Type: "agent_started", Meta: map[string]any{"lane": "x"}, // no agent_id
	}, sess)
	time.Sleep(100 * time.Millisecond)
	if len(fake.rows) != 0 {
		t.Fatalf("non-lifecycle events created rows: %#v", fake.rows)
	}
}

// TestInstanceAgentsProjection_PrefersWorkerHistory pins the terminal read: a
// run with durable rows serves them (newest-updated input rendered oldest
// first) and a row still marked running from a dead process renders failed.
func TestInstanceAgentsProjection_PrefersWorkerHistory(t *testing.T) {
	s := newTestServer(t, nil)
	fake := useFakeWorkerBackend(t)
	now := time.Now().UTC()
	if err := fake.UpsertWorker(context.Background(), runstate.WorkerRecord{
		InstanceID: "run-hist", ScanID: "scan-a", AgentID: "sub_2", Lane: "xss",
		Task: "test xss", Status: "running", StartedAt: now.Add(-2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := fake.UpsertWorker(context.Background(), runstate.WorkerRecord{
		InstanceID: "run-hist", ScanID: "scan-a", AgentID: "sub_1", Lane: "injection",
		Task: "test sqli", Status: "completed",
		StartedAt: now.Add(-5 * time.Minute), FinishedAt: now.Add(-4 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	view := s.instanceAgentsView("run-hist")
	if !view.Available {
		t.Fatal("history-only run must be available")
	}
	if view.PartialReason != "" {
		t.Fatalf("PG-backed read must not be partial: %q", view.PartialReason)
	}
	if len(view.Agents) != 2 {
		t.Fatalf("agents = %d, want 2: %#v", len(view.Agents), view.Agents)
	}
	byID := map[string]agent.DelegatedAgent{}
	for _, worker := range view.Agents {
		byID[worker.AgentID] = worker.DelegatedAgent
	}
	if got := byID["sub_1"].Status; got != "completed" {
		t.Fatalf("sub_1 status = %q", got)
	}
	stale := byID["sub_2"]
	if stale.Status != "failed" || stale.Error == "" {
		t.Fatalf("dead-process running row must render failed: %#v", stale)
	}
	if view.Concurrency.Running != 0 || view.Concurrency.Queued != 0 {
		t.Fatalf("terminal run must not claim live work: %#v", view.Concurrency)
	}
}

// TestInstanceAgentsProjection_MergesHistoryWithCheckpoint pins the merge: the
// checkpoint covers the current session's workers, PG rows cover earlier
// sibling sessions of the same run (1:1 re-dispatch), no duplicate rows.
func TestInstanceAgentsProjection_MergesHistoryWithCheckpoint(t *testing.T) {
	s := newTestServer(t, nil)
	fake := useFakeWorkerBackend(t)
	id := "agents-hist-merge"
	dir := ledgerTestScan(t, s, id)
	checkpoint := `{"version":1,"counter":1,"delegated":1,"agents":[
		{"id":"sub_1","name":"injection","task":"test sqli","targets":["10.0.0.17"],"status":"completed","started_at":"2026-01-02T00:00:00Z","completed_at":"2026-01-02T00:10:00Z"}
	]}`
	if err := os.WriteFile(filepath.Join(dir, "execution-graph.json"), []byte(checkpoint), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rec := range []runstate.WorkerRecord{
		{InstanceID: id, ScanID: "sibling-1", AgentID: "sub_0", Lane: "recon", Task: "map surface", Status: "completed"},
		{InstanceID: id, ScanID: "sibling-2", AgentID: "sub_1", Lane: "injection", Task: "test sqli", Status: "completed"},
	} {
		if err := fake.UpsertWorker(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}

	view := s.instanceAgentsView(id)
	if len(view.Agents) != 2 {
		t.Fatalf("agents = %d, want 2 (checkpoint + sibling row): %#v", len(view.Agents), view.Agents)
	}
	if view.Agents[0].AgentID != "sub_1" {
		t.Fatalf("checkpoint worker must stay first: %#v", view.Agents)
	}
	if view.Agents[1].AgentID != "sub_0" || view.Agents[1].Lane != "recon" {
		t.Fatalf("sibling history row missing: %#v", view.Agents[1])
	}
}
