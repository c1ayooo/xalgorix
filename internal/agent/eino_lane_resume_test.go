package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// laneScriptClient drives one gated tool call on its first turn and a plain
// completion afterwards. It is shared across the "restart" runtimes so the
// resumed model turn is distinguishable from the interrupted one.
type laneScriptClient struct {
	mu    sync.Mutex
	turns int
}

func (c *laneScriptClient) ChatWithUsageToolCalls([]llm.Message, []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
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

// laneFixedBroker implements engine.ApprovalBroker; the resume path here never
// waits on it (the stale approval is denied), but the runtime needs a broker to
// wrap tools with the approval interrupt.
type laneFixedBroker struct{}

func (laneFixedBroker) RequestApproval(context.Context, engine.ApprovalRequest) (string, error) {
	return "approval-1", nil
}

func newLaneResumeRuntime(t *testing.T, client engine.ChatClient, store adk.CheckPointStore, executed *int) *engine.Runtime {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{
		Name: "danger", Description: "danger",
		Parameters: []tools.Parameter{{Name: "x", Required: true}},
		Execute: func(map[string]string) (tools.Result, error) {
			*executed++
			return tools.Result{Success: true, Output: "executed"}, nil
		},
	})
	rt, err := engine.NewRuntime(context.Background(), engine.RuntimeConfig{
		Client: client, Registry: reg, CheckpointStore: store,
		Instruction: "test", MaxIterations: 5, Approvals: laneFixedBroker{},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

func drainADKEvents(iter *adk.AsyncIterator[*adk.AgentEvent]) []*adk.AgentEvent {
	var out []*adk.AgentEvent
	for {
		ev, ok := iter.Next()
		if !ok {
			return out
		}
		out = append(out, ev)
	}
}

func adkInterruptTarget(events []*adk.AgentEvent) string {
	for _, ev := range events {
		if ev == nil || ev.Action == nil || ev.Action.Interrupted == nil {
			continue
		}
		for _, ictx := range ev.Action.Interrupted.InterruptContexts {
			if ictx != nil && ictx.IsRootCause {
				return ictx.ID
			}
		}
	}
	return ""
}

// TestEinoRunOrResumeLaneNoCheckpointUsesFreshRun pins the unchanged path: with
// no durable checkpoint the lane starts a fresh run and reaches the normal
// approval interrupt.
func TestEinoRunOrResumeLaneNoCheckpointUsesFreshRun(t *testing.T) {
	store := engine.NewMemoryCheckpointStore()
	executed := 0
	rt := newLaneResumeRuntime(t, &laneScriptClient{}, store, &executed)
	a := &Agent{ID: "lane", delegatedAgentID: "sub-1", events: make(chan Event, 8), ctx: context.Background()}

	iter := a.einoRunOrResumeLane(rt, store, "scan-1:agent:sub-1", context.Background(), func() int { return 0 })
	events := drainADKEvents(iter)
	if adkInterruptTarget(events) == "" {
		t.Fatalf("fresh run did not reach the approval interrupt: %+v", events)
	}
	if executed != 0 {
		t.Fatalf("tool executed before approval: %d", executed)
	}
}

// TestEinoLaneResumeUsesApprovalCheckpointTarget pins the recovery path: a
// checkpoint saved by an approval interrupt is resumed at its exact target with
// a denial, so the stale gated tool is never executed and the run continues
// (no re-interrupt).
func TestEinoLaneResumeUsesApprovalCheckpointTarget(t *testing.T) {
	const cpID = "scan-1:agent:sub-1"
	store := engine.NewMemoryCheckpointStore()
	client := &laneScriptClient{}
	executed := 0

	first := newLaneResumeRuntime(t, client, store, &executed)
	interrupted := drainADKEvents(first.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, cpID))
	wantTarget := adkInterruptTarget(interrupted)
	if wantTarget == "" {
		t.Fatalf("first run did not interrupt: %+v", interrupted)
	}
	if executed != 0 {
		t.Fatalf("tool executed before approval: %d", executed)
	}

	// Simulate a process restart: a fresh runtime over the same durable store.
	second := newLaneResumeRuntime(t, client, store, &executed)
	a := &Agent{ID: "lane", delegatedAgentID: "sub-1", events: make(chan Event, 8), ctx: context.Background()}

	iter := a.resumeLaneCheckpoint(second, store, cpID, context.Background(), func() int { return 0 })
	if iter == nil {
		t.Fatal("resumeLaneCheckpoint did not resume a valid approval checkpoint")
	}
	resumed := drainADKEvents(iter)
	if got := adkInterruptTarget(resumed); got != "" {
		t.Fatalf("resumed run re-interrupted at %q, want the same target consumed (%q)", got, wantTarget)
	}
	if executed != 0 {
		t.Fatalf("stale approval re-executed the gated tool: %d", executed)
	}
	// The denial is applied at the checkpoint target: the run continues to its
	// next model turn instead of re-interrupting (a mismatched target would make
	// the leaf re-interrupt itself), and the tool never ran.
	done := false
	for _, ev := range resumed {
		if ev.Output != nil && ev.Output.MessageOutput != nil && ev.Output.MessageOutput.Message != nil {
			if ev.Output.MessageOutput.Message.Content == "done" {
				done = true
			}
		}
	}
	if !done {
		t.Fatalf("resumed run did not continue past the denial: %+v", resumed)
	}
	if got := drainAgentEvents(a); len(got) == 0 || got[len(got)-1].Type != "message" {
		t.Fatalf("lane resume did not emit an operator-visible message event: %+v", got)
	}
}

// TestEinoLaneResumeGates pins every reason to fall back: root checkpoint, root
// agent, and an unsupported payload. None may panic or start a resume.
func TestEinoLaneResumeGates(t *testing.T) {
	const cpID = "scan-1:agent:sub-1"
	store := engine.NewMemoryCheckpointStore()
	client := &laneScriptClient{}
	executed := 0
	rt := newLaneResumeRuntime(t, client, store, &executed)
	drainADKEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, cpID))
	agent := &Agent{ID: "lane", delegatedAgentID: "sub-1", events: make(chan Event, 8), ctx: context.Background()}

	t.Run("root checkpoint", func(t *testing.T) {
		if iter := agent.resumeLaneCheckpoint(rt, store, "scan-1", context.Background(), func() int { return 0 }); iter != nil {
			t.Fatal("resumed a non-lane checkpoint")
		}
	})

	t.Run("root agent", func(t *testing.T) {
		root := &Agent{ID: "root", events: make(chan Event, 8), ctx: context.Background()}
		if iter := root.resumeLaneCheckpoint(rt, store, cpID, context.Background(), func() int { return 0 }); iter != nil {
			t.Fatal("resumed for the root agent")
		}
	})

	t.Run("unsupported checkpoint", func(t *testing.T) {
		bad := engine.NewMemoryCheckpointStore()
		if err := bad.Set(context.Background(), cpID, []byte("not a checkpoint")); err != nil {
			t.Fatalf("seed bad checkpoint: %v", err)
		}
		if iter := agent.resumeLaneCheckpoint(rt, bad, cpID, context.Background(), func() int { return 0 }); iter != nil {
			t.Fatal("resumed an unsupported checkpoint")
		}
	})
}
