package agentsgraph

import (
	"context"
	"testing"
	"time"
)

func TestDispatchRunsAndCollects(t *testing.T) {
	var seen int
	g := NewWithConfig(context.Background(), 4, 2, func(_ context.Context, agentID, name string, targets []string, task string) (string, error) {
		seen++
		if agentID == "" || name != "work#1" || task != "do it" {
			t.Errorf("runner args = %q %q %q %v", agentID, name, task, targets)
		}
		return "worker summary", nil
	})
	defer g.Stop()

	id, err := g.Dispatch("work#1", "do it", []string{"example.com"}, "unit-1")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if id == "" {
		t.Fatal("empty worker id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	summary, status, errText := g.DispatchResult(ctx, id)
	if status != "completed" || summary != "worker summary" || errText != "" {
		t.Fatalf("result = %q %q %q", summary, status, errText)
	}
	if seen != 1 {
		t.Fatalf("runner calls = %d", seen)
	}
	// The pool dispatch carries the authoritative intent (unit) into the
	// snapshot, so panels can key one visible worker per work unit.
	var unitID string
	for _, agent := range g.Snapshot().Agents {
		if agent.AgentID == id {
			unitID = agent.UnitID
		}
	}
	if unitID != "unit-1" {
		t.Fatalf("snapshot unit_id = %q, want unit-1", unitID)
	}
	// The pool consumes results, so the root's uncollected gate must not block.
	if got := g.UncollectedCount(); got != 0 {
		t.Fatalf("uncollected = %d, want 0", got)
	}
	if got := g.RunningCount(); got != 0 {
		t.Fatalf("running = %d, want 0", got)
	}
}

func TestSteerAndKillByUnit(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	var steered string
	g := NewWithConfig(context.Background(), 4, 2, func(ctx context.Context, agentID, name string, targets []string, task string) (string, error) {
		close(started)
		select {
		case <-release:
			return "steered result", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	defer g.Stop()
	id, err := g.Dispatch("work#1", "do it", []string{"example.com"}, "unit-steer")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-started
	g.RegisterSteer(id, func(msg string) error {
		steered = msg
		return nil
	})
	if err := g.SteerUnit("unit-steer", "focus on authz"); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if steered != "focus on authz" {
		t.Fatalf("steered = %q", steered)
	}
	if err := g.SteerUnit("missing-unit", "x"); err == nil {
		t.Fatal("steer must reject an unknown unit")
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, status, _ := g.DispatchResult(ctx, id); status != "completed" {
		t.Fatalf("status = %q", status)
	}
}

func TestKillUnitCancelsWorker(t *testing.T) {
	started := make(chan struct{})
	g := NewWithConfig(context.Background(), 4, 2, func(ctx context.Context, agentID, name string, targets []string, task string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	defer g.Stop()
	id, err := g.Dispatch("work#1", "do it", []string{"example.com"}, "unit-kill")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-started
	if err := g.KillUnit("unit-kill", "operator stop"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, status, _ := g.DispatchResult(ctx, id); status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
}
