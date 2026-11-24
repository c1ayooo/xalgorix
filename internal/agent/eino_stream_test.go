package agent

// Streaming projection of ADK model output (always on).
// These tests pin the consumer contract only: the ADK event shape is
// synthesised here, so no network or real runner is involved.

import (
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func drainAgentEvents(a *Agent) []Event {
	var out []Event
	for {
		select {
		case ev := <-a.events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func eventTypes(events []Event) []string {
	types := make([]string, 0, len(events))
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	return types
}

// Streaming is production behavior now: no environment variable decides it.
func TestEinoStreamingAlwaysOn(t *testing.T) {
	for _, value := range []string{"", "0", "false", "1"} {
		t.Setenv("XALGORIX_EINO_STREAM", value)
		if !einoStreamingEnabled() {
			t.Fatalf("einoStreamingEnabled() = false with env %q; streaming must be always on", value)
		}
	}
}

func TestEinoStreamingEmitsDeltasThenDone(t *testing.T) {
	a := &Agent{ID: "stream", events: make(chan Event, 16)}
	tokens := func() int { return 11 }
	stream := schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("Hello", nil),
		schema.AssistantMessage(", ", nil),
		schema.AssistantMessage("world", nil),
	})
	a.consumeEinoOutput(&adk.MessageVariant{IsStreaming: true, MessageStream: stream, Role: schema.Assistant}, tokens)

	events := drainAgentEvents(a)
	// Deltas are the live channel; assistant_done closes the turn with the full
	// text and the durable transcript folds it into one row. No duplicate
	// "message" is emitted for a streamed turn.
	wantTypes := []string{"assistant_delta", "assistant_delta", "assistant_delta", "assistant_done"}
	if got := eventTypes(events); strings.Join(got, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	messageID := events[0].MessageID
	if messageID == "" {
		t.Fatal("streamed turn has no message id")
	}
	var joined string
	for _, ev := range events {
		if ev.MessageID != messageID {
			t.Fatalf("%s MessageID = %q, want %q", ev.Type, ev.MessageID, messageID)
		}
		if ev.TotalTokens != 11 {
			t.Fatalf("%s TotalTokens = %d, want 11", ev.Type, ev.TotalTokens)
		}
	}
	for _, ev := range events[:3] {
		if ev.Delta != ev.Content {
			t.Fatalf("%s Delta = %q, Content = %q; both must carry the increment", ev.Type, ev.Delta, ev.Content)
		}
		joined += ev.Delta
	}
	if joined != "Hello, world" {
		t.Fatalf("delta text = %q", joined)
	}
	if events[3].Content != "Hello, world" {
		t.Fatalf("assistant_done content = %q, want the full turn text", events[3].Content)
	}
}

func TestEinoNonStreamingMessageUnchanged(t *testing.T) {
	a := &Agent{ID: "plain", events: make(chan Event, 4)}
	a.consumeEinoOutput(&adk.MessageVariant{
		Message: schema.AssistantMessage("hi", nil),
		Role:    schema.Assistant,
	}, func() int { return 5 })

	events := drainAgentEvents(a)
	if len(events) != 1 || events[0].Type != "message" || events[0].Content != "hi" {
		t.Fatalf("non-streaming projection = %+v, want one message event", events)
	}
}

func TestEinoStreamingToolRoleDrainedAndDiscarded(t *testing.T) {
	reader, writer := schema.Pipe[*schema.Message](2)
	writer.Send(schema.ToolMessage("tool output", "call-1"), nil)
	writer.Send(nil, errors.New("tool stream stop"))

	a := &Agent{ID: "tool", events: make(chan Event, 4)}
	a.consumeEinoOutput(&adk.MessageVariant{
		IsStreaming:   true,
		MessageStream: reader,
		Role:          schema.Tool,
		ToolName:      "danger",
	}, func() int { return 0 })

	if events := drainAgentEvents(a); len(events) != 0 {
		t.Fatalf("tool-role stream produced assistant events: %+v", events)
	}
	// The consumer must close the stream so Eino's producer cannot block.
	if !writer.Send(schema.ToolMessage("late", "call-1"), nil) {
		t.Fatal("stream reader was not closed after consuming a tool-role stream")
	}
}

func TestEinoStreamingKeepsSecretsOutOfDeltas(t *testing.T) {
	const secret = "Bearer super-secret-token"
	a := &Agent{ID: "secret", events: make(chan Event, 16), secretValues: []string{secret}}
	stream := schema.StreamReaderFromArray([]*schema.Message{
		// The secret is split across chunks; no single chunk contains it.
		schema.AssistantMessage("prefix Bearer super", nil),
		schema.AssistantMessage("-secret-token suffix", nil),
	})
	a.consumeEinoOutput(&adk.MessageVariant{IsStreaming: true, MessageStream: stream, Role: schema.Assistant}, func() int { return 0 })

	events := drainAgentEvents(a)
	var deltas strings.Builder
	for _, ev := range events {
		switch ev.Type {
		case "assistant_delta":
			for _, fragment := range []string{"Bearer", "super", "secret-token"} {
				if strings.Contains(ev.Content, fragment) {
					t.Fatalf("delta leaked secret fragment %q: %q", fragment, ev.Content)
				}
			}
			deltas.WriteString(ev.Content)
		case "assistant_done", "message":
			if strings.Contains(ev.Content, "super-secret-token") {
				t.Fatalf("%s leaked the secret: %q", ev.Type, ev.Content)
			}
		}
	}
	if deltas.String() != "prefix ***REDACTED*** suffix" {
		t.Fatalf("redacted delta text = %q", deltas.String())
	}
}
