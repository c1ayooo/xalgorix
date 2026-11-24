package engine

// Streaming-mode wiring: a runtime built with Streaming=true must consume the
// model through Stream (events carry MessageStream) while keeping the approval
// interrupt/resume contract byte-for-byte the same. The ADK persists the mode
// in the checkpoint, so a resumed run keeps streaming (see
// typedRunnerResumeInternalImpl); this test pins the observable outcome.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// streamScriptClient serves the same two-turn script as scriptedClient but on
// the streaming wire, so RuntimeConfig.Streaming exercises ChatModel.Stream.
type streamScriptClient struct {
	mu    sync.Mutex
	turns int
}

func (c *streamScriptClient) ChatWithUsageToolCalls([]llm.Message, []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	return "", nil, nil, errors.New("engine: streaming client does not implement Generate")
}

func (c *streamScriptClient) ChatStreamWithTools(_ []llm.Message, _ []llm.ToolDefinition) <-chan llm.StreamChunk {
	c.mu.Lock()
	c.turns++
	turn := c.turns
	c.mu.Unlock()

	ch := make(chan llm.StreamChunk, 8)
	go func() {
		defer close(ch)
		if turn == 1 {
			ch <- llm.StreamChunk{Content: "calling "}
			ch <- llm.StreamChunk{Content: "tool"}
			ch <- llm.StreamChunk{ToolCalls: []llm.StreamToolCall{{
				Index: 0, ID: "call-1", Name: "danger", Arguments: `{"x":"1"}`,
			}}}
			ch <- llm.StreamChunk{Done: true, Usage: &llm.TokenUsage{TotalTokens: 3}}
			return
		}
		ch <- llm.StreamChunk{Content: "do"}
		ch <- llm.StreamChunk{Content: "ne"}
		ch <- llm.StreamChunk{Done: true, Usage: &llm.TokenUsage{TotalTokens: 4}}
	}()
	return ch
}

// drainStreamingEvents consumes an ADK iterator, draining every MessageStream
// (an unread Eino stream blocks its producer) and collecting assistant text.
func drainStreamingEvents(t *testing.T, iter *adk.AsyncIterator[*adk.AgentEvent]) ([]*adk.AgentEvent, string) {
	t.Helper()
	var events []*adk.AgentEvent
	var text strings.Builder
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, ev)
		if ev == nil || ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mv := ev.Output.MessageOutput
		if mv.IsStreaming && mv.MessageStream != nil {
			for {
				chunk, err := mv.MessageStream.Recv()
				if err != nil {
					break
				}
				if chunk != nil && mv.Role == schema.Assistant {
					text.WriteString(chunk.Content)
				}
			}
			mv.MessageStream.Close()
			continue
		}
		if mv.Message != nil && mv.Role == schema.Assistant {
			text.WriteString(mv.Message.Content)
		}
	}
	return events, text.String()
}

func TestRuntimeStreamingKeepsApprovalInterruptAndResume(t *testing.T) {
	reg := tools.NewRegistry()
	executed := 0
	reg.Register(&tools.Tool{
		Name: "danger", Description: "danger",
		Parameters: []tools.Parameter{{Name: "x", Required: true}},
		Execute: func(args map[string]string) (tools.Result, error) {
			executed++
			return tools.Result{Success: true, Output: "executed " + args["x"]}, nil
		},
	})
	broker := &fixedApprovalBroker{}
	client := &streamScriptClient{}
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client:          client,
		Registry:        reg,
		CheckpointStore: NewMemoryCheckpointStore(),
		Instruction:     "test",
		MaxIterations:   5,
		Approvals:       broker,
		NativeTools:     true,
		Streaming:       true,
		Guard: ToolGuardFunc(func(context.Context, string, map[string]string) GuardVerdict {
			return GuardVerdict{Allowed: true}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	events, firstText := drainStreamingEvents(t, rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "stream-checkpoint"))
	targetID, info := interruptedTarget(events)
	if targetID == "" {
		for _, ev := range events {
			t.Logf("event err=%v action=%+v output=%+v", ev.Err, ev.Action, ev.Output)
		}
		t.Fatalf("no interrupt emitted in streaming mode: %+v", events)
	}
	if ai, ok := info.(ApprovalInterruptInfo); !ok || ai.ApprovalID != "approval-1" || ai.ToolName != "danger" {
		t.Fatalf("interrupt info = %#v", info)
	}
	if executed != 0 {
		t.Fatalf("tool executed before approval: %d", executed)
	}
	if !strings.Contains(firstText, "calling tool") {
		t.Fatalf("streamed assistant text = %q, want the first turn's text", firstText)
	}

	iter, err := rt.Resume(context.Background(), "stream-checkpoint", targetID, ApprovalDecision{Approved: true, Status: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	_, resumedText := drainStreamingEvents(t, iter)
	if executed != 1 {
		t.Fatalf("approved tool executions = %d, want 1", executed)
	}
	if !strings.Contains(resumedText, "done") {
		t.Fatalf("resumed streamed text = %q, want the post-approval turn", resumedText)
	}
}

func TestRuntimeStreamingFlagDefaultsOff(t *testing.T) {
	// The engine only forwards the flag; the default zero value must keep the
	// non-streaming runner (existing tests / production without the env opt-in
	// exercise that path unchanged).
	reg := tools.NewRegistry()
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client:          &scriptedClient{},
		Registry:        reg,
		CheckpointStore: NewMemoryCheckpointStore(),
		Instruction:     "test",
		MaxIterations:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "non-stream"))
	for _, ev := range events {
		if ev.Output != nil && ev.Output.MessageOutput != nil && ev.Output.MessageOutput.IsStreaming {
			t.Fatalf("non-streaming runtime produced a streamed message output")
		}
	}
}
