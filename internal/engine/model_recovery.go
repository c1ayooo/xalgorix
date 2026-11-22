package engine

// Model recovery seam. The Eino ChatModel adapter owns the provider request,
// while the scan owns provider-wait state, retry policy and the recovery hooks.
// Keeping that policy behind an interface lets the engine issue no side effects
// of its own and keeps the ADK independent of agent internals.

import (
	"context"

	"github.com/xalgord/xalgorix/v4/internal/llm"
)

// ModelRequestAction is the pre-request decision.
type ModelRequestAction struct {
	// Category labels the request for token attribution.
	Category string
}

// ModelErrorAction decides what to do after a failed provider request.
type ModelErrorAction struct {
	Retry        bool
	Nudge        string
	ResetContext bool
	// CompactContext keeps the system prompt and a bounded recent window
	// instead of resetting the whole conversation. Used for context-window
	// overflow, where the old history is too large but still useful.
	CompactContext bool
	Fatal          error
}

// ModelResponseAction decides what to do after a successful provider response.
type ModelResponseAction struct {
	// Retry re-issues the same model turn with Nudge appended. Used for empty
	// responses and reasoning-only turns, where the provider call succeeded but
	// produced nothing executable.
	Retry        bool
	Nudge        string
	ResetContext bool
	Fatal        error
}

// ModelRecovery is implemented by the scan's obligation adapter. All callbacks
// run on the model-call goroutine and are serialized by the ADK tool node.
type ModelRecovery interface {
	BeforeModel(ctx context.Context, attempt int) ModelRequestAction
	OnModelError(ctx context.Context, err error) ModelErrorAction
	OnModelResponse(ctx context.Context, request []llm.Message, content string, calls []llm.ToolCall, usage *llm.TokenUsage) ModelResponseAction
}

// ErrScanAborted is a terminal, typed outcome produced by recovery policy.
// It is distinct from a graph/tool failure so the scan loop can preserve its
// typed abort reasons and partial-result event shape.
type ErrScanAborted struct {
	Reason  string
	Content string
}

func (e *ErrScanAborted) Error() string {
	if e == nil {
		return "engine: scan aborted"
	}
	return e.Content
}

// ErrProviderPaused means the bounded provider-wait budget was exhausted. The
// scan emits its paused event and returns; it is not an aborted terminal scan.
type ErrProviderPaused struct {
	Reason string
}

func (e *ErrProviderPaused) Error() string {
	if e == nil {
		return "engine: provider paused"
	}
	return "engine: provider paused: " + e.Reason
}
