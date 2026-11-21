package engine

// Runtime is the ADK execution seam. It is built only when the operator
// explicitly selects the eino engine; the legacy agent.Run path does not import
// this file. The runner is configured with a durable checkpoint store so an
// approval interrupt can resume the same run after a restart.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	einoTool "github.com/cloudwego/eino/components/tool"

	"github.com/xalgord/xalgorix/v4/internal/tools"
)

// GuardVerdict is the application-level pre-tool decision. Allowed=false
// short-circuits with Output as the tool result (never a Go error), preserving
// the legacy "expected rejection is not a tool failure" rule.
type GuardVerdict struct {
	Allowed bool
	Output  string
}

// ToolGuard is the six-ring scope/safety seam. The engine calls it before every
// tool endpoint; it must not be nil in production eino mode.
type ToolGuard interface {
	CheckTool(ctx context.Context, name string, args map[string]string) GuardVerdict
}

// ToolGuardFunc adapts a function to ToolGuard.
type ToolGuardFunc func(ctx context.Context, name string, args map[string]string) GuardVerdict

func (f ToolGuardFunc) CheckTool(ctx context.Context, name string, args map[string]string) GuardVerdict {
	return f(ctx, name, args)
}

// RuntimeConfig assembles one scan's ADK agent. Instruction is the already
// built system prompt; the runtime does not rebuild prompt or obligation state.
type RuntimeConfig struct {
	Client          ChatClient
	Registry        *tools.Registry
	CheckpointStore adk.CheckPointStore
	Instruction     string
	MaxIterations   int
	Guard           ToolGuard
	Obligations     TurnObserver
	Approvals       ApprovalBroker
	InstanceID      string
	ScanContextID   string
}

// Runtime owns one ADK agent + runner for an explicit eino scan.
type Runtime struct {
	agent  *adk.ChatModelAgent
	runner *adk.Runner
}

// NewRuntime constructs the Eino path. It fails closed when required
// dependencies are missing; the caller can then keep the legacy path or report
// a configuration error. It never silently falls back after construction.
func NewRuntime(ctx context.Context, cfg RuntimeConfig) (*Runtime, error) {
	if cfg.Client == nil {
		return nil, errors.New("engine: runtime requires a chat client")
	}
	if cfg.Registry == nil {
		return nil, errors.New("engine: runtime requires a tool registry")
	}
	if cfg.CheckpointStore == nil {
		return nil, errors.New("engine: runtime requires a checkpoint store")
	}
	baseTools, err := RegistryTools(cfg.Registry)
	if err != nil {
		return nil, err
	}
	wrapped := make([]einoTool.InvokableTool, 0, len(baseTools))
	for _, inner := range baseTools {
		info, infoErr := inner.Info(ctx)
		if infoErr != nil {
			return nil, infoErr
		}
		if cfg.Approvals != nil {
			inner = NewApprovalTool(inner, info.Name, cfg.Approvals, cfg.InstanceID, cfg.ScanContextID)
		}
		wrapped = append(wrapped, inner)
	}

	handlers := make([]adk.ChatModelAgentMiddleware, 0, 2)
	if cfg.Obligations != nil {
		handlers = append(handlers, &obligationMiddleware{observer: cfg.Obligations})
	}
	if cfg.Guard != nil || cfg.Obligations != nil {
		handlers = append(handlers, &toolGuardMiddleware{guard: cfg.Guard, observer: cfg.Obligations})
	}

	toolConfig := adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               toBaseTools(wrapped),
			ExecuteSequentially: true, // report_vulnerability and live state stay serial
		},
	}
	maxIterations := cfg.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 20
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "xalgorix-eino",
		Description:   "Xalgorix security assessment agent",
		Instruction:   cfg.Instruction,
		Model:         NewChatModel(cfg.Client),
		ToolsConfig:   toolConfig,
		Handlers:      handlers,
		MaxIterations: maxIterations,
	})
	if err != nil {
		return nil, fmt.Errorf("engine: build ADK agent: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		CheckPointStore: cfg.CheckpointStore,
	})
	return &Runtime{agent: agent, runner: runner}, nil
}

// Run starts a fresh agent execution. The checkpoint ID must be stable across a
// process restart for approval resume to work; callers should bind it to the
// scan instance/run identity.
func (r *Runtime) Run(ctx context.Context, messages []*schema.Message, checkpointID string) *adk.AsyncIterator[*adk.AgentEvent] {
	opts := []adk.AgentRunOption{}
	if strings.TrimSpace(checkpointID) != "" {
		opts = append(opts, adk.WithCheckPointID(checkpointID))
	}
	return r.runner.Run(ctx, messages, opts...)
}

// Resume continues an interrupted run. targetID is the interrupt context ID
// (the leaf address returned in Action.Interrupted.InterruptContexts); decision
// becomes the approval wrapper's resume data.
func (r *Runtime) Resume(ctx context.Context, checkpointID, targetID string, decision ApprovalDecision) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	if strings.TrimSpace(checkpointID) == "" || strings.TrimSpace(targetID) == "" {
		return nil, errors.New("engine: resume requires checkpoint and interrupt target IDs")
	}
	return r.runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{
		Targets: map[string]any{targetID: decision},
	})
}

// Agent exposes the constructed ADK agent for tests and advanced callers.
func (r *Runtime) Agent() *adk.ChatModelAgent {
	if r == nil {
		return nil
	}
	return r.agent
}

func toBaseTools(tools []einoTool.InvokableTool) []einoTool.BaseTool {
	out := make([]einoTool.BaseTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, t)
	}
	return out
}

// toolGuardMiddleware applies the scope/safety verdict before the tool endpoint
// (and therefore before our approval interrupt). It returns a normal tool result
// for a denial so the model can choose an alternative path.
type toolGuardMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	guard    ToolGuard
	observer TurnObserver
}

func (m *toolGuardMiddleware) WrapInvokableToolCall(_ context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(callCtx context.Context, argumentsInJSON string, opts ...einoTool.Option) (string, error) {
		args, err := decodeRegistryArgs(argumentsInJSON)
		if err != nil {
			return "", err
		}
		if m.guard != nil {
			verdict := m.guard.CheckTool(callCtx, tCtx.Name, args)
			if !verdict.Allowed {
				return denyOutput(verdict, "the application safety guard denied this call"), nil
			}
		}
		if m.observer != nil {
			verdict := m.observer.BeforeTool(callCtx, tCtx.Name, args)
			if !verdict.Allowed {
				return denyOutput(verdict, "the engine obligation denied this call"), nil
			}
		}
		output, callErr := endpoint(callCtx, argumentsInJSON, opts...)
		if m.observer != nil {
			m.observer.AfterTool(callCtx, tCtx.Name, args, output, callErr)
		}
		return output, callErr
	}, nil
}

func denyOutput(verdict GuardVerdict, fallback string) string {
	if strings.TrimSpace(verdict.Output) != "" {
		return verdict.Output
	}
	return "⛔ TOOL BLOCKED — " + fallback + "."
}
