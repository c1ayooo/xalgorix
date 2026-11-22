package engine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/xalgord/xalgorix/v4/internal/llm"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

type recordingObserver struct {
	mu           sync.Mutex
	turns        []int
	blocked      []string
	finishDenied int
	afters       []string
	stopTurn     int
	nudges       []string
}

func (o *recordingObserver) BeforeTurn(_ context.Context, turn int) TurnDecision {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.turns = append(o.turns, turn)
	if o.stopTurn > 0 && turn >= o.stopTurn {
		return TurnDecision{StopReason: "budget exhausted"}
	}
	nudge := "turn nudge"
	o.nudges = append(o.nudges, nudge)
	return TurnDecision{Nudge: nudge}
}

func (o *recordingObserver) BeforeTool(_ context.Context, name string, _ map[string]string) GuardVerdict {
	if name == "finish" {
		o.mu.Lock()
		o.finishDenied++
		o.mu.Unlock()
		return GuardVerdict{Allowed: false, Output: "⚠️ FINISH REJECTED — more testing required"}
	}
	o.mu.Lock()
	o.blocked = append(o.blocked, name)
	o.mu.Unlock()
	return GuardVerdict{Allowed: true}
}

func (o *recordingObserver) TransformToolOutput(_ context.Context, _ string, _ map[string]string, output string, _ map[string]any, _ error) string {
	return output
}

func (o *recordingObserver) AfterTool(_ context.Context, name string, _ map[string]string, output string, _ map[string]any, _ error) {
	o.mu.Lock()
	o.afters = append(o.afters, name+":"+output)
	o.mu.Unlock()
}

// finishClient drives exactly one finish tool call.
type finishClient struct{ turns int }

func (c *finishClient) ChatWithUsageToolCalls([]llm.Message, []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	c.turns++
	if c.turns == 1 {
		return "", []llm.ToolCall{{ID: "f-1", Name: "finish", Args: map[string]string{"summary": "done"}}}, nil, nil
	}
	return "done", nil, nil, nil
}

func TestObligationObserverBlocksFinishExecution(t *testing.T) {
	reg := tools.NewRegistry()
	executed := 0
	reg.Register(&tools.Tool{
		Name: "finish", Description: "finish",
		Execute: func(map[string]string) (tools.Result, error) {
			executed++
			return tools.Result{Success: true, Output: "finished"}, nil
		},
	})
	obs := &recordingObserver{}
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: &finishClient{}, Registry: reg, CheckpointStore: NewMemoryCheckpointStore(),
		Instruction: "test", MaxIterations: 5, Obligations: obs,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "obligation-1"))
	if executed != 0 {
		t.Fatalf("blocked finish executed %d times", executed)
	}
	if len(events) == 0 || len(obs.turns) == 0 || obs.finishDenied == 0 {
		t.Fatalf("observer was not exercised: turns=%v finishDenied=%d events=%d", obs.turns, obs.finishDenied, len(events))
	}
}

func TestObligationObserverStopIsExplicit(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{Name: "noop", Description: "noop", Execute: func(map[string]string) (tools.Result, error) {
		return tools.Result{Success: true, Output: "ok"}, nil
	}})
	obs := &recordingObserver{stopTurn: 1}
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: &scriptedClient{}, Registry: reg, CheckpointStore: NewMemoryCheckpointStore(),
		Instruction: "test", MaxIterations: 5, Obligations: obs,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "obligation-stop"))
	var stopped bool
	for _, ev := range events {
		var turnStop *ErrTurnStopped
		if ev.Err != nil && errors.As(ev.Err, &turnStop) && turnStop.Reason == "budget exhausted" {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("expected explicit ErrTurnStopped, events=%+v", events)
	}
}
