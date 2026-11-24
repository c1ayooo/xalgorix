package engine

// Observability projection built on Eino's callback system.
//
// The runtime no longer hand-rolls model/tool instrumentation: Eino already
// wraps every chat-model Generate (adk/wrappers.go typedCallbackInjectedModel)
// and every tool InvokableRun (compose/tool_node.go invokableToolWithCallback)
// with callbacks. One handler per Runtime turns those lifecycle events into a
// TraceSink projection.
//
// Why per-run instead of per-process: callbacks.AppendGlobalHandlers keeps a
// process-wide slice. The runtime is constructed once per scan, so using it
// would append a handler per scan forever and every scan would observe every
// other scan. Instead the handler lives on the Runtime and is mounted only on
// that runtime's run/resume options via adk.WithCallbacks, so two scans never
// share a handler and nothing is registered globally.
//
// Nil TraceSink installs no handler at all: the framework then keeps its
// no-handler fast path and behaviour is unchanged.

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ModelUsage is the token accounting projected from an accepted model
// response. It mirrors the provider usage Eino attaches to
// schema.Message.ResponseMeta.Usage.
type ModelUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
}

// ModelTrace describes one model invocation observed through Eino callbacks.
// Duration and Usage are zero on start and populated on end. A failed
// invocation still produces exactly one ModelEnd, with Err set.
type ModelTrace struct {
	Duration time.Duration
	Usage    *ModelUsage
	Err      error
}

// ToolTrace describes one tool invocation observed through Eino callbacks.
// Name is the registered tool name; CallID is the model-emitted tool call id.
// Duration is zero on start and populated on end. A failed invocation still
// produces exactly one ToolEnd, with Err set.
type ToolTrace struct {
	Name     string
	CallID   string
	Duration time.Duration
	Err      error
}

// TraceSink receives the per-invocation projection of Eino's callback
// lifecycle for a single scan runtime. Every Start has exactly one matching
// End, including failed invocations (End carries a non-nil Err). Implementations
// are called synchronously from the graph goroutine, so they must be fast,
// concurrency-safe, and must not panic — a panic would propagate into the run.
type TraceSink interface {
	ModelStart(ctx context.Context, trace ModelTrace)
	ModelEnd(ctx context.Context, trace ModelTrace)
	ToolStart(ctx context.Context, trace ToolTrace)
	ToolEnd(ctx context.Context, trace ToolTrace)
}

// traceKind classifies a callback by the component that emitted it.
type traceKind int

const (
	traceKindNone traceKind = iota
	traceKindModel
	traceKindTool
)

// traceKindOf maps an Eino RunInfo to the projection bucket. Only chat-model
// and tool components are projected; the agent itself, graph/chain/lambda
// nodes and the ToolsNode wrapper are ignored. Returning traceKindNone is what
// keeps the agent's OnEnd iterator out of our handler: Eino would otherwise
// hand this handler its own copy of the agent event stream, and a copy that is
// never consumed blocks the producer (adk/callback.go copyTypedEventIterator).
func traceKindOf(info *callbacks.RunInfo) traceKind {
	if info == nil {
		return traceKindNone
	}
	switch info.Component {
	case components.ComponentOfChatModel:
		return traceKindModel
	case components.ComponentOfTool:
		return traceKindTool
	default:
		return traceKindNone
	}
}

// traceSpanKey carries the start time of the current invocation between
// OnStart and its terminal callback. Eino threads the context returned by
// OnStart through the component call and back into OnEnd/OnError, so a context
// value (not shared map state) is the race-free way to pair them.
type traceSpanKey struct{}

type traceSpan struct {
	kind  traceKind
	start time.Time
}

// traceHandler implements callbacks.Handler and callbacks.TimingChecker.
type traceHandler struct {
	sink TraceSink
}

var (
	_ callbacks.Handler       = (*traceHandler)(nil)
	_ callbacks.TimingChecker = (*traceHandler)(nil)
)

// newTraceHandler returns nil when sink is nil so callers can mount nothing.
func newTraceHandler(sink TraceSink) callbacks.Handler {
	if sink == nil {
		return nil
	}
	return &traceHandler{sink: sink}
}

// Needed lets the framework skip timings we do not observe. Crucially, agent
// and graph components report false for every timing, so Eino never allocates
// a stream/iterator copy for us.
func (h *traceHandler) Needed(_ context.Context, info *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	if h == nil || h.sink == nil {
		return false
	}
	if traceKindOf(info) == traceKindNone {
		return false
	}
	switch timing {
	case callbacks.TimingOnStart, callbacks.TimingOnEnd, callbacks.TimingOnError, callbacks.TimingOnEndWithStreamOutput:
		return true
	default:
		return false
	}
}

func (h *traceHandler) OnStart(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
	kind := traceKindOf(info)
	if kind == traceKindNone {
		return ctx
	}
	ctx = context.WithValue(ctx, traceSpanKey{}, &traceSpan{kind: kind, start: time.Now()})
	switch kind {
	case traceKindModel:
		h.sink.ModelStart(ctx, ModelTrace{})
	case traceKindTool:
		h.sink.ToolStart(ctx, ToolTrace{Name: toolTraceName(info), CallID: compose.GetToolCallID(ctx)})
	}
	return ctx
}

func (h *traceHandler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	h.finish(ctx, info, output, nil)
	return ctx
}

func (h *traceHandler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	h.finish(ctx, info, nil, err)
	return ctx
}

// OnEndWithStreamOutput keeps the start/end pairing invariant if a streaming
// component is ever used. The scan runtime currently never streams (see
// ChatModel.Stream's docstring), but when it did, Eino fires a plain OnStart
// and then this timing. The reader is a private copy that must be drained and
// closed; draining in a goroutine avoids blocking the stream setup.
func (h *traceHandler) OnEndWithStreamOutput(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	kind := traceKindOf(info)
	if kind == traceKindNone {
		output.Close()
		return ctx
	}
	var start time.Time
	if span, ok := ctx.Value(traceSpanKey{}).(*traceSpan); ok && span.kind == kind {
		start = span.start
	}
	name := toolTraceName(info)
	callID := compose.GetToolCallID(ctx)
	go func() {
		defer output.Close()
		var streamErr error
		for {
			if _, err := output.Recv(); err != nil {
				if !errors.Is(err, io.EOF) {
					streamErr = err
				}
				break
			}
		}
		elapsed := time.Duration(0)
		if !start.IsZero() {
			elapsed = time.Since(start)
		}
		switch kind {
		case traceKindModel:
			h.sink.ModelEnd(ctx, ModelTrace{Duration: elapsed, Err: streamErr})
		case traceKindTool:
			h.sink.ToolEnd(ctx, ToolTrace{Name: name, CallID: callID, Duration: elapsed, Err: streamErr})
		}
	}()
	return ctx
}

// OnStartWithStreamInput is never needed for chat-model or tool components
// (they use Collect/Transform-style stream input only at the graph level, which
// traceKindOf rejects), so it stays unregistered.
func (h *traceHandler) OnStartWithStreamInput(ctx context.Context, _ *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	input.Close()
	return ctx
}

func (h *traceHandler) finish(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput, err error) {
	kind := traceKindOf(info)
	if kind == traceKindNone {
		return
	}
	var elapsed time.Duration
	if span, ok := ctx.Value(traceSpanKey{}).(*traceSpan); ok && span.kind == kind {
		elapsed = time.Since(span.start)
	}
	switch kind {
	case traceKindModel:
		trace := ModelTrace{Duration: elapsed, Err: err}
		if err == nil {
			if msg, ok := output.(*schema.Message); ok {
				trace.Usage = modelUsageOf(msg)
			}
		}
		h.sink.ModelEnd(ctx, trace)
	case traceKindTool:
		h.sink.ToolEnd(ctx, ToolTrace{
			Name:     toolTraceName(info),
			CallID:   compose.GetToolCallID(ctx),
			Duration: elapsed,
			Err:      err,
		})
	}
}

func toolTraceName(info *callbacks.RunInfo) string {
	if info == nil {
		return ""
	}
	return info.Name
}

func modelUsageOf(msg *schema.Message) *ModelUsage {
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return nil
	}
	usage := msg.ResponseMeta.Usage
	return &ModelUsage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CachedTokens:     usage.PromptTokenDetails.CachedTokens,
	}
}
