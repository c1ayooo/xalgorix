package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// recordedTrace is a TraceSink that records every projection call behind a
// mutex (callbacks may run on graph goroutines).
type recordedTrace struct {
	mu          sync.Mutex
	modelStarts int
	modelEnds   []ModelTrace
	toolStarts  []ToolTrace
	toolEnds    []ToolTrace
}

func (r *recordedTrace) ModelStart(context.Context, ModelTrace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelStarts++
}

func (r *recordedTrace) ModelEnd(_ context.Context, trace ModelTrace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelEnds = append(r.modelEnds, trace)
}

func (r *recordedTrace) ToolStart(_ context.Context, trace ToolTrace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolStarts = append(r.toolStarts, trace)
}

func (r *recordedTrace) ToolEnd(_ context.Context, trace ToolTrace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolEnds = append(r.toolEnds, trace)
}

func (r *recordedTrace) snapshot() (int, []ModelTrace, []ToolTrace, []ToolTrace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.modelStarts, append([]ModelTrace(nil), r.modelEnds...),
		append([]ToolTrace(nil), r.toolStarts...), append([]ToolTrace(nil), r.toolEnds...)
}

// traceClient is a deterministic scripted client: the first turn emits a tool
// call for toolName, and any later turn that sees a tool result finishes. When
// toolName is empty it finishes on the first turn. err forces a model failure.
type traceClient struct {
	mu       sync.Mutex
	toolName string
	err      error
	usage    *llm.TokenUsage
	calls    int
}

func (c *traceClient) ChatWithUsageToolCalls(messages []llm.Message, _ []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return "", nil, nil, c.err
	}
	sawTool := false
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(messages[i].Role, "tool") {
			sawTool = true
			break
		}
	}
	usage := c.usage
	if usage == nil {
		usage = &llm.TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, CachedTokens: 1}
	}
	if c.toolName == "" || sawTool {
		return "done", nil, usage, nil
	}
	return "calling", []llm.ToolCall{{
		ID: "trace-call-1", Name: c.toolName, Args: map[string]string{"x": "1"},
	}}, usage, nil
}

func traceRegistry(t *testing.T, name string, execute func(map[string]string) (tools.Result, error)) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{Name: name, Description: name, Execute: execute})
	return reg
}

func allowAllGuard() ToolGuard {
	return ToolGuardFunc(func(context.Context, string, map[string]string) GuardVerdict {
		return GuardVerdict{Allowed: true}
	})
}

func newTraceRuntime(t *testing.T, client ChatClient, reg *tools.Registry, sink TraceSink) *Runtime {
	t.Helper()
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client:          client,
		Registry:        reg,
		CheckpointStore: NewMemoryCheckpointStore(),
		Instruction:     "test",
		MaxIterations:   5,
		// Native wire protocol keeps tool results as role "tool"; the XML
		// fallback rewrites them to user text, which the scripted client would
		// not recognise as a completed tool call.
		NativeTools: true,
		Guard:       allowAllGuard(),
		TraceSink:   sink,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

// A single model turn with no tool call must project exactly one start and one
// matching end, with the provider usage carried on the end event.
func TestRuntimeTraceModelStartEndOnce(t *testing.T) {
	sink := &recordedTrace{}
	rt := newTraceRuntime(t, &traceClient{}, traceRegistry(t, "unused", func(map[string]string) (tools.Result, error) {
		return tools.Result{Success: true, Output: "ok"}, nil
	}), sink)

	drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "trace-model"))

	starts, ends, toolStarts, toolEnds := sink.snapshot()
	if starts != 1 || len(ends) != 1 {
		t.Fatalf("model start/end = %d/%d, want 1/1", starts, len(ends))
	}
	if len(toolStarts) != 0 || len(toolEnds) != 0 {
		t.Fatalf("unexpected tool projections: %+v %+v", toolStarts, toolEnds)
	}
	end := ends[0]
	if end.Err != nil {
		t.Fatalf("model end error = %v", end.Err)
	}
	if end.Usage == nil {
		t.Fatal("model end missing usage")
	}
	if end.Usage.PromptTokens != 1 || end.Usage.CompletionTokens != 2 || end.Usage.TotalTokens != 3 || end.Usage.CachedTokens != 1 {
		t.Fatalf("usage projection = %+v", end.Usage)
	}
}

// One tool invocation must project exactly one tool start and one matching
// end, named after the registered tool, while both model turns each pair up.
func TestRuntimeTraceToolStartEndOnce(t *testing.T) {
	sink := &recordedTrace{}
	rt := newTraceRuntime(t, &traceClient{toolName: "echo"}, traceRegistry(t, "echo", func(map[string]string) (tools.Result, error) {
		return tools.Result{Success: true, Output: "ok"}, nil
	}), sink)

	drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "trace-tool"))

	starts, ends, toolStarts, toolEnds := sink.snapshot()
	if len(toolStarts) != 1 || len(toolEnds) != 1 {
		t.Fatalf("tool start/end = %d/%d, want 1/1", len(toolStarts), len(toolEnds))
	}
	if toolStarts[0].Name != "echo" || toolEnds[0].Name != "echo" {
		t.Fatalf("tool names = %q/%q", toolStarts[0].Name, toolEnds[0].Name)
	}
	if toolEnds[0].Err != nil {
		t.Fatalf("tool end error = %v", toolEnds[0].Err)
	}
	// Two model turns (tool call + final answer): every start pairs with one
	// end and no end is left unpaired.
	if starts != len(ends) || starts != 2 {
		t.Fatalf("model start/end = %d/%d, want 2/2", starts, len(ends))
	}
}

// Handlers are mounted per run, never process-wide: constructing a second
// runtime must not deliver that runtime's events to the first, and re-running
// a runtime must not accumulate duplicate handlers.
func TestRuntimeTraceHandlersIsolatedPerRuntime(t *testing.T) {
	sinkA := &recordedTrace{}
	sinkB := &recordedTrace{}

	rtA := newTraceRuntime(t, &traceClient{toolName: "alpha"}, traceRegistry(t, "alpha", func(map[string]string) (tools.Result, error) {
		return tools.Result{Success: true, Output: "a"}, nil
	}), sinkA)
	rtB := newTraceRuntime(t, &traceClient{toolName: "beta"}, traceRegistry(t, "beta", func(map[string]string) (tools.Result, error) {
		return tools.Result{Success: true, Output: "b"}, nil
	}), sinkB)

	// Run B first: A's handler must observe nothing while B runs.
	drainEvents(rtB.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "iso-b-1"))
	if starts, ends, ts, te := sinkA.snapshot(); starts != 0 || len(ends) != 0 || len(ts) != 0 || len(te) != 0 {
		t.Fatalf("runtime A observed runtime B's run: start=%d end=%d toolStart=%d toolEnd=%d", starts, len(ends), len(ts), len(te))
	}
	if _, _, ts, te := sinkB.snapshot(); len(ts) != 1 || len(te) != 1 || te[0].Name != "beta" {
		t.Fatalf("runtime B projections = %+v %+v", ts, te)
	}

	// Run A twice: A sees two runs (two tool ends), B stays at one.
	drainEvents(rtA.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "iso-a-1"))
	drainEvents(rtA.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "iso-a-2"))

	if _, _, ts, te := sinkA.snapshot(); len(ts) != 2 || len(te) != 2 || te[0].Name != "alpha" || te[1].Name != "alpha" {
		t.Fatalf("runtime A projections after two runs = %+v %+v", ts, te)
	}
	if starts, ends, ts, te := sinkB.snapshot(); starts != 2 || len(ends) != 2 || len(ts) != 1 || len(te) != 1 {
		t.Fatalf("runtime B counts changed by A's runs: modelStart=%d modelEnd=%d toolStart=%d toolEnd=%d", starts, len(ends), len(ts), len(te))
	}
}

// A failed tool invocation still produces a start and exactly one end, with
// the error carried on the end event.
func TestRuntimeTraceToolEndOnError(t *testing.T) {
	sink := &recordedTrace{}
	rt := newTraceRuntime(t, &traceClient{toolName: "broken"}, traceRegistry(t, "broken", func(map[string]string) (tools.Result, error) {
		return tools.Result{}, errors.New("tool exploded")
	}), sink)

	drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "trace-tool-err"))

	_, _, toolStarts, toolEnds := sink.snapshot()
	if len(toolStarts) != 1 || len(toolEnds) != 1 {
		t.Fatalf("tool start/end = %d/%d, want 1/1 on error", len(toolStarts), len(toolEnds))
	}
	if toolEnds[0].Err == nil || !strings.Contains(toolEnds[0].Err.Error(), "tool exploded") {
		t.Fatalf("tool end error = %v, want the execution error", toolEnds[0].Err)
	}
}

// A failed model invocation still produces a start and exactly one end, with
// the error carried on the end event.
func TestRuntimeTraceModelEndOnError(t *testing.T) {
	sink := &recordedTrace{}
	rt := newTraceRuntime(t, &traceClient{err: errors.New("provider down")}, traceRegistry(t, "unused", func(map[string]string) (tools.Result, error) {
		return tools.Result{Success: true, Output: "ok"}, nil
	}), sink)

	drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "trace-model-err"))

	starts, ends, _, _ := sink.snapshot()
	if starts != 1 || len(ends) != 1 {
		t.Fatalf("model start/end = %d/%d, want 1/1 on error", starts, len(ends))
	}
	if ends[0].Err == nil || !strings.Contains(ends[0].Err.Error(), "provider down") {
		t.Fatalf("model end error = %v, want the provider error", ends[0].Err)
	}
}

// A nil TraceSink must not mount a handler or change behaviour: the run still
// completes and no handler is constructed.
func TestRuntimeTraceNilSinkIsInert(t *testing.T) {
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client:          &traceClient{toolName: "echo"},
		Registry:        traceRegistry(t, "echo", func(map[string]string) (tools.Result, error) { return tools.Result{Success: true, Output: "ok"}, nil }),
		CheckpointStore: NewMemoryCheckpointStore(),
		Instruction:     "test",
		MaxIterations:   5,
		NativeTools:     true,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if rt.trace != nil {
		t.Fatal("nil sink mounted a callback handler")
	}
	events := drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "trace-nil"))
	for _, ev := range events {
		if ev != nil && ev.Err != nil {
			t.Fatalf("nil-sink run surfaced error: %v", ev.Err)
		}
	}
}
