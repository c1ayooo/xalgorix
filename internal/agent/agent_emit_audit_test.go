package agent

import (
	"testing"
	"time"
)

// Tool call/result events are the durable domain-audit substrate (they carry
// ledger claims, hypothesis status transitions and report outcomes into the
// per-scan events.jsonl journal), so they must use the same blocking send as
// finished/error and never be silently dropped when the event buffer is full.
func TestEmitDoesNotDropToolEventsWhenChannelFull(t *testing.T) {
	a := &Agent{ID: "audit", events: make(chan Event, 1)}
	a.events <- Event{Type: "message", Content: "occupant"}

	go func() {
		// The producer is now blocked in safeSend; unblock it by draining the
		// occupant so the tool event can be delivered.
		time.Sleep(50 * time.Millisecond)
		<-a.events
	}()

	emitted := make(chan struct{})
	go func() {
		a.emit(Event{Type: "tool_result", ToolName: "claim_next_hypothesis"})
		close(emitted)
	}()

	select {
	case <-emitted:
	case <-time.After(3 * time.Second):
		t.Fatal("tool_result event was dropped or never delivered — audit-carrying events must use the blocking send")
	}
	select {
	case evt := <-a.events:
		if evt.Type != "tool_result" {
			t.Fatalf("received %q, want tool_result", evt.Type)
		}
	default:
		t.Fatal("tool_result not present in the event channel after emit returned")
	}
}

// Narration stays best-effort: a full buffer still drops a plain message
// event immediately instead of stalling the agent loop.
func TestEmitStillDropsNarrationWhenChannelFull(t *testing.T) {
	a := &Agent{ID: "audit", events: make(chan Event, 1)}
	a.events <- Event{Type: "message", Content: "occupant"}

	done := make(chan struct{})
	go func() {
		a.emit(Event{Type: "message", Content: "best effort"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("narration emit must stay non-blocking")
	}
	evt := <-a.events
	if evt.Content != "occupant" {
		t.Fatal("narration event must not displace the channel occupant (best-effort drop preserved)")
	}
}
