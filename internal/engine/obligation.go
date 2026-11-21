package engine

// Obligation bridge. The legacy scan loop already owns phase obligations,
// finish gates, coverage/liveness, budgets and ledger hooks. Instead of
// reimplementing those rules here, the runtime exposes a narrow observer seam
// that the legacy Agent adapter can fill from the same pure functions.

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// TurnDecision is the per-model-turn obligation outcome.
type TurnDecision struct {
	Nudge       string
	EmitMessage string
	StopReason  string
}

// TurnObserver bridges legacy engine-owned obligations into the ADK loop.
type TurnObserver interface {
	BeforeTurn(ctx context.Context, turn int) TurnDecision
	BeforeTool(ctx context.Context, name string, args map[string]string) GuardVerdict
	AfterTool(ctx context.Context, name string, args map[string]string, output string, callErr error)
}

// ErrTurnStopped marks a clean engine obligation stop (budget, liveness,
// operator stop). Callers can distinguish it from a provider/tool failure.
type ErrTurnStopped struct {
	Reason string
}

func (e *ErrTurnStopped) Error() string {
	return "engine: turn stopped: " + e.Reason
}

// obligationMiddleware applies the observer at model-turn and tool boundaries.
// It is registered once at construction and cannot be mutated at runtime.
type obligationMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	observer TurnObserver
	mu       sync.Mutex
	turn     int
}

func (m *obligationMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	m.mu.Lock()
	m.turn++
	turn := m.turn
	m.mu.Unlock()

	decision := m.observer.BeforeTurn(ctx, turn)
	if decision.StopReason != "" {
		return ctx, state, &ErrTurnStopped{Reason: decision.StopReason}
	}
	if decision.Nudge != "" {
		state.Messages = append(state.Messages, schema.UserMessage(decision.Nudge))
	}
	// EmitMessage has no direct event channel in the middleware API; the caller's
	// observer owns the event sink and can emit it from BeforeTurn. Keeping the
	// field here makes that contract explicit.
	_ = decision.EmitMessage
	return ctx, state, nil
}
