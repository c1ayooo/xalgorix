package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/xalgord/xalgorix/v4/internal/llm"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

// scriptedIterationClient emits a noop tool call each turn and finishes on the
// limitTurns-th model request, recording how many requests the runtime made.
type scriptedIterationClient struct {
	limitTurns int
	turns      int
}

func (c *scriptedIterationClient) ChatWithUsageToolCalls([]llm.Message, []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	c.turns++
	if c.turns < c.limitTurns {
		return "", []llm.ToolCall{{ID: fmt.Sprintf("t-%d", c.turns), Name: "noop", Args: map[string]string{}}}, nil, nil
	}
	if c.turns == c.limitTurns {
		return "", []llm.ToolCall{{ID: "f-1", Name: "finish", Args: map[string]string{"summary": "done"}}}, nil, nil
	}
	// Post-finish closing message: the ADK makes one more model call after the
	// finish tool executes; a plain-text reply ends the run (another finish
	// call here would loop forever).
	return "done", nil, nil, nil
}

func iterationTestRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{Name: "noop", Description: "noop", Execute: func(map[string]string) (tools.Result, error) {
		return tools.Result{Success: true, Output: "ok"}, nil
	}})
	reg.Register(&tools.Tool{Name: "finish", Description: "finish", Execute: func(map[string]string) (tools.Result, error) {
		return tools.Result{Success: true, Output: "finished"}, nil
	}})
	return reg
}

// Config contract (config.go: "XALGORIX_MAX_ITERATIONS — 0 = unlimited"):
// MaxIterations=0 must reach the ADK as unlimited, not the historical hard
// floor of 20 — a benchmark run scripted past 20 tool calls died at exactly
// call 20 with ErrExceedMaxIterations (CORE_LOOP_AUDIT §4.2).
func TestMaxIterationsZeroMeansUnlimited(t *testing.T) {
	client := &scriptedIterationClient{limitTurns: 21}
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: client, Registry: iterationTestRegistry(),
		CheckpointStore: NewMemoryCheckpointStore(),
		Instruction:     "test", MaxIterations: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "iter-unlimited"))
	// 20 noop rounds + 1 finish request + 1 post-finish closing request.
	if client.turns != 22 {
		t.Fatalf("run stopped after %d model requests, want 22 (0 must mean unlimited)", client.turns)
	}
	for _, ev := range events {
		if ev != nil && ev.Err != nil {
			t.Fatalf("unlimited run surfaced error: %v", ev.Err)
		}
	}
}

// Control: a positive MaxIterations still caps the run — the fix must not
// remove limiting entirely.
func TestMaxIterationsPositiveStillCaps(t *testing.T) {
	client := &scriptedIterationClient{limitTurns: 999}
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: client, Registry: iterationTestRegistry(),
		CheckpointStore: NewMemoryCheckpointStore(),
		Instruction:     "test", MaxIterations: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "iter-capped"))
	if client.turns != 5 {
		t.Fatalf("capped run made %d model requests, want exactly 5", client.turns)
	}
	capped := false
	for _, ev := range events {
		if ev != nil && errors.Is(ev.Err, adk.ErrExceedMaxIterations) {
			capped = true
		}
	}
	if !capped {
		t.Fatalf("cap at 5 did not surface adk.ErrExceedMaxIterations (events=%d)", len(events))
	}
}
