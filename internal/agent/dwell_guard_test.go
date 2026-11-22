package agent

// dwell_guard_test.go — pins the benchmark dwell budget: OFF by default,
// charges per private endpoint, blocks at the cap without charging, and
// resets after idle decay (container IP recycling).

import (
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

func TestDwellBudgetOffByDefault(t *testing.T) {
	ctx, state := newTestCtxState(t)
	a := NewAgent(&config.Config{MaxIterations: 10}, "dwell-off", make(chan Event, 16), scopeguard.Config{}, ctx)
	t.Cleanup(a.Stop)
	a.state = state
	for i := 0; i < 500; i++ {
		if blocked, _ := a.shouldBlockForDwellBudget("terminal_execute", map[string]string{"command": "curl http://10.0.0.9:8080/"}); blocked {
			t.Fatal("dwell budget must be OFF when unset")
		}
	}
}

func TestDwellBudgetBlocksAtCap(t *testing.T) {
	ctx, state := newTestCtxState(t)
	a := NewAgent(&config.Config{MaxIterations: 10, DwellMaxCalls: 3}, "dwell-cap", make(chan Event, 16), scopeguard.Config{}, ctx)
	t.Cleanup(a.Stop)
	a.state = state
	args := map[string]string{"command": `A="http://10.0.0.9:8080"; curl "$A/x"`}
	for i := 0; i < 3; i++ {
		if blocked, _ := a.shouldBlockForDwellBudget("terminal_execute", args); blocked {
			t.Fatalf("call %d blocked before the cap", i+1)
		}
	}
	blocked, reason := a.shouldBlockForDwellBudget("terminal_execute", args)
	if !blocked {
		t.Fatal("cap not enforced")
	}
	if !strings.Contains(reason, "10.0.0.9:8080") || !strings.Contains(reason, "rotate") {
		t.Fatalf("block reason lacks endpoint/rotate guidance: %q", reason)
	}
	// A different endpoint keeps its own budget.
	if blocked, _ := a.shouldBlockForDwellBudget("terminal_execute", map[string]string{"command": "curl http://10.0.0.10:9000/"}); blocked {
		t.Fatal("distinct endpoint must have an independent budget")
	}
	// Non-endpoint calls pass through without charging.
	if blocked, _ := a.shouldBlockForDwellBudget("add_note", map[string]string{"value": "no endpoint here"}); blocked {
		t.Fatal("endpoint-free call must pass")
	}
}

func TestDwellBudgetDecaysAfterIdle(t *testing.T) {
	ctx, state := newTestCtxState(t)
	a := NewAgent(&config.Config{MaxIterations: 10, DwellMaxCalls: 1}, "dwell-decay", make(chan Event, 16), scopeguard.Config{}, ctx)
	t.Cleanup(a.Stop)
	a.state = state
	args := map[string]string{"command": "curl http://10.0.0.9/"}
	a.shouldBlockForDwellBudget("terminal_execute", args)
	if blocked, _ := a.shouldBlockForDwellBudget("terminal_execute", args); !blocked {
		t.Fatal("cap must hold immediately after exhaustion")
	}
	// Simulate decay: age the entry past dwellDecay.
	a.dwellMu.Lock()
	for _, st := range a.dwellStats {
		st.lastSeen = st.lastSeen.Add(-2 * 60 * 60 * time.Second)
	}
	a.dwellMu.Unlock()
	if blocked, _ := a.shouldBlockForDwellBudget("terminal_execute", args); blocked {
		t.Fatal("recycled-IP budget must reset after idle decay")
	}
}
