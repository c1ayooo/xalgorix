package engine

// unknown_tools_test.go — an unknown/hallucinated tool call must be a
// correctable tool result, never a fatal NodeRunError. The tool surface is
// narrower than the prose prompt for restricted lanes (control-plane workers
// get HTTP + notes only), so a model naming terminal_execute used to abort the
// whole lane with "tool terminal_execute not found in toolsNode indexes".

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

func TestRuntimeUnknownToolHandlerIsWired(t *testing.T) {
	reg := tools.NewRegistry()
	noop := func(map[string]string) (tools.Result, error) { return tools.Result{Success: true}, nil }
	reg.Register(&tools.Tool{Name: "http_request", Description: "http", Execute: noop})
	reg.Register(&tools.Tool{Name: "add_note", Description: "note", Execute: noop})

	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: &parallelToolClient{}, Registry: reg, CheckpointStore: NewMemoryCheckpointStore(),
		Instruction: "test", MaxIterations: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := rt.ToolsNodeConfig().UnknownToolsHandler
	if handler == nil {
		t.Fatal("UnknownToolsHandler is nil: a hallucinated tool name aborts the whole lane")
	}
	out, err := handler(context.Background(), "terminal_execute", `{"command":"id"}`)
	if err != nil {
		t.Fatalf("unknown-tool handler returned an error (still fatal): %v", err)
	}
	for _, want := range []string{"terminal_execute", "http_request", "add_note"} {
		if !strings.Contains(out, want) {
			t.Fatalf("unknown-tool result must name %q (got %q)", want, out)
		}
	}
}

type unknownToolThenDoneClient struct {
	turns  int
	second []llm.Message
}

func (c *unknownToolThenDoneClient) ChatWithUsageToolCalls(messages []llm.Message, _ []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	c.turns++
	if c.turns == 1 {
		return "", []llm.ToolCall{{
			ID: "call-1", Name: "terminal_execute", Args: map[string]string{"command": "id"},
		}}, nil, nil
	}
	c.second = append([]llm.Message(nil), messages...)
	return "done", nil, nil, nil
}

// TestRuntimeUnknownToolIsRecoverable pins the behavior: a call to an
// unregistered tool feeds the handler's result back to the model and the run
// continues to the next turn instead of aborting.
func TestRuntimeUnknownToolIsRecoverable(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{Name: "http_request", Description: "http", Execute: func(map[string]string) (tools.Result, error) {
		return tools.Result{Success: true, Output: "ok"}, nil
	}})
	client := &unknownToolThenDoneClient{}
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: client, Registry: reg, CheckpointStore: NewMemoryCheckpointStore(),
		Instruction: "test", MaxIterations: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "unknown-tool-cp"))
	for _, ev := range events {
		if ev != nil && ev.Err != nil {
			t.Fatalf("run aborted on an unknown tool call: %v", ev.Err)
		}
	}
	if client.turns < 2 {
		t.Fatalf("model was never given a recovery turn (turns=%d)", client.turns)
	}
	found := false
	for _, msg := range client.second {
		if strings.Contains(msg.Content, "unknown tool") && strings.Contains(msg.Content, "terminal_execute") {
			found = true
		}
	}
	if !found {
		t.Fatalf("recovery turn did not carry the unknown-tool result: %#v", client.second)
	}
}
