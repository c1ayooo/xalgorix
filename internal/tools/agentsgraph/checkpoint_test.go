package agentsgraph

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/storage"
)

func TestGraphCheckpointResumesUnfinishedIdentitiesOnce(t *testing.T) {
	dir := t.TempDir()
	saved := graphCheckpoint{Version: 1, Counter: 2, Delegated: 2, Agents: []savedAgent{
		{ID: "done", Name: "completed-lane", Task: "assigned task", Status: "completed", Result: "saved evidence"},
		{ID: "interrupted", Name: "remaining-lane", Task: "remaining task", Status: "running", Targets: []string{"https://example.invalid"}},
	}}
	data, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteAtomic(filepath.Join(dir, "execution-graph.json"), data); err != nil {
		t.Fatal(err)
	}
	runs := make(chan string, 3)
	g := NewWithConfig(context.Background(), 2, 1, func(_ context.Context, id, _ string, _ []string, _ string) (string, error) {
		runs <- id
		return "continued evidence", nil
	})
	t.Cleanup(g.Stop)
	if err := g.RestoreCheckpoint(dir); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatal("restoration launched work before the root was ready")
	}
	g.ResumeCheckpointedWork()
	g.ResumeCheckpointedWork()
	if !g.WaitStopped(time.Second) {
		t.Fatal("restored lane did not complete")
	}
	if len(runs) != 1 || <-runs != "interrupted" || g.DelegationCount() != 2 {
		t.Fatal("completed work reran or restart reset lifetime admission")
	}
	done, ok := g.snapshot("done", false)
	if !ok || done.Result != "saved evidence" || done.Observed {
		t.Fatalf("completed uncollected result was lost: %+v", done)
	}
}

func TestAsyncCheckpointFailureRemainsVisibleAtRootSafePoint(t *testing.T) {
	dir := t.TempDir()
	g := NewWithConfig(context.Background(), 1, 1, nil)
	t.Cleanup(g.Stop)
	if err := g.RestoreCheckpoint(dir); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.persistPath = filepath.Join(dir, "unavailable", "execution-graph.json")
	if err := g.persistLocked(); err == nil {
		t.Fatal("expected unavailable checkpoint directory")
	}
	if err := os.Mkdir(filepath.Join(dir, "unavailable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := g.persistLocked(); err != nil {
		t.Fatal(err)
	}
	g.mu.Unlock()
	if err := g.SaveCheckpoint(); err == nil {
		t.Fatal("a later write hid the earlier asynchronous durability failure")
	}
}

// A delegation whose checkpoint cannot be persisted must roll its admission
// back out entirely: without a durable record the delegation never happened,
// so the lifetime budget slot and the agent identity must not survive — a
// ghost queued agent would otherwise sit in PendingSummary/UncollectedCount
// and hold the finish gate open forever.
func TestAgentAdmissionRollsBackOnPersistFailure(t *testing.T) {
	dir := t.TempDir()
	g := NewWithConfig(context.Background(), 2, 2, func(_ context.Context, _, _ string, _ []string, _ string) (string, error) {
		return "done", nil
	})
	t.Cleanup(g.Stop)
	if err := g.RestoreCheckpoint(dir); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.persistPath = filepath.Join(dir, "missing-dir", "execution-graph.json")
	g.mu.Unlock()

	res, err := g.createAgent(map[string]string{"name": "lane", "task": "work"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" {
		t.Fatal("persist failure must surface an error result")
	}
	if got := g.DelegationCount(); got != 0 {
		t.Fatalf("delegated budget = %d after failed create, want 0 (rolled back)", got)
	}
	if running := g.RunningCount(); running != 0 {
		t.Fatalf("running agents = %d after failed create, want 0 (no ghost identity)", running)
	}
	if summary := g.PendingSummary(); summary != "" {
		t.Fatalf("ghost agent leaked into finish gating: %q", summary)
	}

	// Same contract for the async spawn path.
	res, err = g.spawnAgent(map[string]string{"name": "lane", "task": "work"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" {
		t.Fatal("persist failure must surface an error result")
	}
	if got := g.DelegationCount(); got != 0 {
		t.Fatalf("delegated budget = %d after failed spawn, want 0 (rolled back)", got)
	}
}

// When the queued-slot handoff inside onAgentDone cannot persist, the next
// agent must NOT stay marked "running" (it never started — a ghost that hangs
// wait_agent and the finish gate). It is rolled back to queued and requeued at
// the front, mirroring ResumeCheckpointedWork.
func TestOnAgentDonePersistFailureRequeuesNext(t *testing.T) {
	dir := t.TempDir()
	gate := make(chan struct{})
	var runs int32
	g := NewWithConfig(context.Background(), 2, 1, func(_ context.Context, _, _ string, _ []string, _ string) (string, error) {
		atomic.AddInt32(&runs, 1)
		<-gate
		return "done", nil
	})
	t.Cleanup(g.Stop)
	if err := g.RestoreCheckpoint(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spawnAgent(map[string]string{"name": "first", "task": "work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spawnAgent(map[string]string{"name": "second", "task": "work"}); err != nil {
		t.Fatal(err)
	}
	// First is running (concurrency 1), second is queued. Break the checkpoint
	// path before the first agent completes so the handoff persist inside
	// onAgentDone fails.
	checkpoint := filepath.Join(dir, "execution-graph.json")
	if err := os.Remove(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(checkpoint, 0o700); err != nil {
		t.Fatal(err)
	}
	close(gate)
	if !g.WaitStopped(2 * time.Second) {
		t.Fatal("first agent did not complete")
	}

	g.mu.Lock()
	status := ""
	for _, s := range g.agents {
		if s.Name == "second" {
			status = s.Status
		}
	}
	g.mu.Unlock()
	if status != "queued" {
		t.Fatalf("requeued agent status = %q, want \"queued\" (no ghost running)", status)
	}
	if got := g.RunningCount(); got != 1 {
		t.Fatalf("running+queued = %d, want 1 (the requeued agent)", got)
	}
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Fatalf("runner invocations = %d, want 1 (next not started while its checkpoint could not be saved)", n)
	}
}
