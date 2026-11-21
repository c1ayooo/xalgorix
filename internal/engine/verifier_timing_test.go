package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/xalgord/xalgorix/v4/internal/llm"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

type parallelToolClient struct{ turns int }

func (c *parallelToolClient) ChatWithUsageToolCalls([]llm.Message, []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	c.turns++
	if c.turns == 1 {
		return "", []llm.ToolCall{
			{ID: "a", Name: "report_vulnerability", Args: map[string]string{"id": "a"}},
			{ID: "b", Name: "verify_finding", Args: map[string]string{"id": "b"}},
		}, nil, nil
	}
	return "done", nil, nil, nil
}

func TestRuntimeExecutesToolsSequentiallyForVerifierSafety(t *testing.T) {
	reg := tools.NewRegistry()
	var active, maxActive int32
	run := func(args map[string]string) (tools.Result, error) {
		n := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&maxActive)
			if n <= old || atomic.CompareAndSwapInt32(&maxActive, old, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return tools.Result{Success: true, Output: args["id"]}, nil
	}
	reg.Register(&tools.Tool{Name: "report_vulnerability", Description: "report", Execute: run})
	reg.Register(&tools.Tool{Name: "verify_finding", Description: "verify", Execute: run})

	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: &parallelToolClient{}, Registry: reg, CheckpointStore: NewMemoryCheckpointStore(),
		Instruction: "test", MaxIterations: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "timing-cp"))
	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("concurrent tools peaked at %d, want exactly 1 (Verifier safety)", got)
	}
}
