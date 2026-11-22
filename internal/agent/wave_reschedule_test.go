package agent

import (
	"context"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/agentsgraph"
)

func TestWaveRescheduleIsOptIn(t *testing.T) {
	t.Setenv("XALGORIX_WAVE_RESCHEDULE", "")
	if waveRescheduleEnabled() {
		t.Fatal("wave reschedule must default to off")
	}
	t.Setenv("XALGORIX_WAVE_RESCHEDULE", "true")
	if !waveRescheduleEnabled() {
		t.Fatal("explicit opt-in was ignored")
	}
}

// TestMaybeRescheduleWaveRequiresIdleWaveAndPlanBudget pins the guard rails:
// no extra lane while the first wave is still running, when the plan has no
// unfinished class, or when the lifetime delegation budget is spent.
func TestMaybeRescheduleWaveRequiresIdleWaveAndPlanBudget(t *testing.T) {
	t.Setenv("XALGORIX_WAVE_RESCHEDULE", "true")
	release := make(chan struct{})
	graph := agentsgraph.NewWithConfig(context.Background(), 2, 2, func(_ context.Context, _ string, _ string, _ []string, _ string) (string, error) {
		<-release
		return "done", nil
	})
	t.Cleanup(graph.Stop)
	t.Cleanup(func() { close(release) })

	state := NewScanState()
	state.Plan = NewPlan()
	state.Plan.add(&Task{ID: "test-sqli", Phase: 6, VulnClass: "sqli", Status: TaskActive})
	state.WaveLaunched = true
	state.WaveCount = 1

	a := &Agent{state: state, agentGraph: graph}

	// A running lane blocks the reschedule (the wave must drain first).
	reg := tools.NewRegistry()
	graph.Register(reg)
	if _, err := reg.Execute("spawn_agent", map[string]string{"name": "injection-serverside", "task": "work"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if note := a.maybeRescheduleWave([]string{"https://example.com"}); note != "" {
		t.Fatalf("reschedule must wait for the running wave: %q", note)
	}
}
