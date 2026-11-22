package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

// injectTurnObserver returns a BeforeTurn observer over a minimal agent whose
// plan still has open work, so no stop path (budget, liveness, plan-complete,
// goal) can fire ahead of the assertions.
func injectTurnObserver(t *testing.T) (*Agent, *einoObserver) {
	t.Helper()
	sctx := scanctx.New("inject-delivery", t.TempDir())
	t.Cleanup(sctx.Close)
	a := NewAgent(&config.Config{MaxIterations: 20}, "inject-delivery",
		make(chan Event, 64), scopeguard.Config{}, sctx)
	t.Cleanup(a.Stop)
	a.state = NewScanState()
	a.state.Plan = NewPlan()
	a.state.Plan.add(&Task{ID: "inject-open", Phase: 7, VulnClass: "xss", Status: TaskActive})
	a.state.PlanBuilt = true
	a.state.ReconCoverage.HTTPProbed = true
	return a, &einoObserver{agent: a}
}

// TestSendMessageReachesNextModelTurn pins the mid-run injection seam. The
// Eino ADK owns the conversation state, so an operator message sent via
// SendMessage must ride the pending-injection queue and surface in the next
// BeforeTurn nudge. Appending it to the Run-start seed buffer (the
// pre-Eino mechanism) is a dead letter no model call ever reads — the
// regression that shipped with the Eino migration and silently ate operator
// guidance on running scans.
func TestSendMessageReachesNextModelTurn(t *testing.T) {
	a, obs := injectTurnObserver(t)

	if nudge := obs.BeforeTurn(context.Background(), 1).Nudge; strings.Contains(nudge, "你是谁") {
		t.Fatalf("operator message delivered before it was sent: %q", nudge)
	}

	if _, err := a.SendMessage("你是谁"); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	got := obs.BeforeTurn(context.Background(), 2).Nudge
	if !strings.Contains(got, "[USER MESSAGE DURING SCAN]: 你是谁") {
		t.Fatalf("operator message missing from the next turn nudge: %q", got)
	}
	if again := obs.BeforeTurn(context.Background(), 3).Nudge; strings.Contains(again, "你是谁") {
		t.Fatalf("operator message re-delivered on a later turn: %q", again)
	}
}

// TestHookNudgeRidesInjectionQueue pins the second dead-letter victim: hook
// nudges fired from the tool/stuck guards appendUserNudge mid-turn, which the
// old loop re-read every iteration but the ADK never will. They must surface
// on the next BeforeTurn exactly like operator messages.
func TestHookNudgeRidesInjectionQueue(t *testing.T) {
	a, obs := injectTurnObserver(t)

	a.appendUserNudge("STUCK-TASK RECOVERY: move to a different task")
	got := obs.BeforeTurn(context.Background(), 1).Nudge
	if !strings.Contains(got, "STUCK-TASK RECOVERY") {
		t.Fatalf("hook nudge missing from the next turn nudge: %q", got)
	}
	if again := obs.BeforeTurn(context.Background(), 2).Nudge; again != "" {
		t.Fatalf("queue not drained, nudge re-delivered: %q", again)
	}
}

// TestAppendUserNudgeDropsConsecutiveDuplicate keeps the original dedup
// contract: identical consecutive nudges must not stack, but a repeated
// nudge after different content is an intentional re-delivery.
func TestAppendUserNudgeDropsConsecutiveDuplicate(t *testing.T) {
	a, _ := injectTurnObserver(t)

	a.appendUserNudge("nudge-a")
	a.appendUserNudge("nudge-a")
	a.appendUserNudge("nudge-b")
	a.appendUserNudge("nudge-a")
	if got := a.drainPendingInjects(); got != "nudge-a\n\nnudge-b\n\nnudge-a" {
		t.Fatalf("dedup or ordering wrong: %q", got)
	}
	if again := a.drainPendingInjects(); again != "" {
		t.Fatalf("drain did not empty the queue: %q", again)
	}
}

// TestSendMessageStoppedAgentRejected keeps the lifecycle contract: after
// Stop there is no delivery channel, and the caller must see the error.
func TestSendMessageStoppedAgentRejected(t *testing.T) {
	a, _ := injectTurnObserver(t)
	a.Stop()
	if _, err := a.SendMessage("too late"); err == nil {
		t.Fatal("SendMessage on a stopped agent must return an error")
	}
	if got := a.drainPendingInjects(); got != "" {
		t.Fatalf("stopped agent accepted a message: %q", got)
	}
}
