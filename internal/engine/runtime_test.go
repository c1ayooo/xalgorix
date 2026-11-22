package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

type scriptedClient struct {
	mu    sync.Mutex
	turns int
}

func (c *scriptedClient) ChatWithUsageToolCalls([]llm.Message, []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns++
	if c.turns == 1 {
		return "calling tool", []llm.ToolCall{{
			ID: "call-1", Name: "danger", Args: map[string]string{"x": "1"},
		}}, &llm.TokenUsage{TotalTokens: 3}, nil
	}
	return "done", nil, &llm.TokenUsage{TotalTokens: 4}, nil
}

type contextualClient struct{}

func (c *contextualClient) ChatWithUsageToolCalls(messages []llm.Message, _ []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(messages[i].Role, "tool") {
			return "done", nil, &llm.TokenUsage{TotalTokens: 4}, nil
		}
	}
	return "calling tool", []llm.ToolCall{{
		ID: "call-1", Name: "danger", Args: map[string]string{"x": "1"},
	}}, &llm.TokenUsage{TotalTokens: 3}, nil
}

type fixedApprovalBroker struct {
	requested ApprovalRequest
}

func (b *fixedApprovalBroker) RequestApproval(_ context.Context, req ApprovalRequest) (string, error) {
	b.requested = req
	return "approval-1", nil
}

func drainEvents(iter *adk.AsyncIterator[*adk.AgentEvent]) []*adk.AgentEvent {
	var out []*adk.AgentEvent
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		out = append(out, ev)
	}
	return out
}

func interruptedTarget(events []*adk.AgentEvent) (targetID string, info any) {
	for _, ev := range events {
		if ev == nil || ev.Action == nil || ev.Action.Interrupted == nil {
			continue
		}
		for _, ictx := range ev.Action.Interrupted.InterruptContexts {
			if ictx != nil && ictx.IsRootCause {
				return ictx.ID, ictx.Info
			}
		}
	}
	return "", nil
}

func TestRuntimeApprovalInterruptAndResume(t *testing.T) {
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
	client := &scriptedClient{}
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: client, Registry: reg, CheckpointStore: NewMemoryCheckpointStore(),
		Instruction: "test", MaxIterations: 5, Approvals: broker,
		Guard: ToolGuardFunc(func(context.Context, string, map[string]string) GuardVerdict {
			return GuardVerdict{Allowed: true}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "checkpoint-1"))
	targetID, info := interruptedTarget(events)
	if targetID == "" {
		for _, ev := range events {
			t.Logf("event err=%v action=%+v output=%+v", ev.Err, ev.Action, ev.Output)
		}
		t.Fatalf("no interrupt emitted: %+v", events)
	}
	if ai, ok := info.(ApprovalInterruptInfo); !ok || ai.ApprovalID != "approval-1" || ai.ToolName != "danger" {
		t.Fatalf("interrupt info = %#v", info)
	}
	if executed != 0 {
		t.Fatalf("tool executed before approval: %d", executed)
	}

	iter, err := rt.Resume(context.Background(), "checkpoint-1", targetID, ApprovalDecision{
		Approved: true, Status: "approved",
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed := drainEvents(iter)
	if executed != 1 {
		t.Fatalf("approved tool executions = %d, want 1", executed)
	}
	foundDone := false
	for _, ev := range resumed {
		if ev.Output != nil && ev.Output.MessageOutput != nil &&
			ev.Output.MessageOutput.Message != nil && ev.Output.MessageOutput.Message.Content == "done" {
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("resumed run did not complete: %+v", resumed)
	}
}

func TestRuntimeApprovalResumesFromFileCheckpointAfterRestart(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry()
	executed := 0
	reg.Register(&tools.Tool{
		Name: "danger", Description: "danger",
		Execute: func(map[string]string) (tools.Result, error) {
			executed++
			return tools.Result{Success: true, Output: "executed"}, nil
		},
	})
	firstStore, err := NewFileCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: &contextualClient{}, Registry: reg, CheckpointStore: firstStore,
		Instruction: "test", MaxIterations: 5, Approvals: &fixedApprovalBroker{},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(first.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "restart-cp"))
	targetID, _ := interruptedTarget(events)
	if targetID == "" {
		t.Fatalf("no interrupt: %+v", events)
	}
	if executed != 0 {
		t.Fatalf("tool ran before approval: %d", executed)
	}

	// Simulate a process restart: fresh runtime/store instance, same directory
	// and checkpoint ID.
	secondStore, err := NewFileCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: &contextualClient{}, Registry: reg, CheckpointStore: secondStore,
		Instruction: "test", MaxIterations: 5, Approvals: &fixedApprovalBroker{},
	})
	if err != nil {
		t.Fatal(err)
	}
	iter, err := second.Resume(context.Background(), "restart-cp", targetID, ApprovalDecision{
		Approved: true, Status: "approved",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(iter)
	if executed != 1 {
		t.Fatalf("restart-resumed tool executions = %d, want 1", executed)
	}
}

func TestRuntimeApprovalDenialDoesNotExecute(t *testing.T) {
	reg := tools.NewRegistry()
	executed := 0
	reg.Register(&tools.Tool{
		Name: "danger", Description: "danger",
		Execute: func(map[string]string) (tools.Result, error) {
			executed++
			return tools.Result{Success: true, Output: "executed"}, nil
		},
	})
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: &scriptedClient{}, Registry: reg, CheckpointStore: NewMemoryCheckpointStore(),
		Instruction: "test", MaxIterations: 5, Approvals: &fixedApprovalBroker{},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "checkpoint-deny"))
	targetID, _ := interruptedTarget(events)
	if targetID == "" {
		t.Fatalf("no interrupt: %+v", events)
	}
	iter, err := rt.Resume(context.Background(), "checkpoint-deny", targetID, ApprovalDecision{
		Approved: false, Status: "rejected", Reason: "not on my watch",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(iter)
	if executed != 0 {
		t.Fatalf("denied tool executed: %d", executed)
	}
}
