package engine

// Runtime is the ADK execution seam. It is the only scan runtime. The runner
// is configured with a durable checkpoint store so an approval interrupt can
// resume the same run after a restart.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	einoTool "github.com/cloudwego/eino/components/tool"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// GuardVerdict is the application-level pre-tool decision. Allowed=false
// short-circuits with Output as the tool result (never a Go error): an
// expected rejection is not a tool failure.
type GuardVerdict struct {
	Allowed bool
	Output  string
	// Args optionally replaces the arguments sent to the tool endpoint. The
	// scan uses it for pre-gate terminal command normalization, so the guard
	// and the executed command see byte-identical input.
	Args map[string]string
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
	// ToolTimeout is the per-tool hard ceiling, applied with a 30s grace.
	ToolTimeout func(string) time.Duration
	// OnToolTimeout is invoked when that hard ceiling fires, so the agent can
	// record uncooperative-timeout telemetry.
	OnToolTimeout func(string)
	// StopCheck is consulted before every model request. The scan sets it to
	// "scan complete" so the graph cannot issue another paid model call after
	// a completed finish: the ADK's tool-node completion and the next model
	// turn can overlap, so a context cancellation alone races that hand-off.
	StopCheck func() bool
	// ModelRecovery owns provider retry/backoff, empty-response recovery and
	// token accounting. It runs in the ChatModel adapter, before the ADK sees
	// a failed or non-executable turn.
	ModelRecovery ModelRecovery
	// Compactor applies the scan's bounded-context policy to the ADK message
	// state before the provider request is serialized.
	Compactor func([]*schema.Message) []*schema.Message
	// NativeTools selects the provider wire protocol. false keeps the XML
	// fallback for Anthropic/Gemini/Responses endpoints.
	NativeTools bool
	// OnReasoning receives the reasoning model's raw thinking text for each
	// accepted provider response (fire-and-forget observability sink for the
	// sessions-page thinking role). Nil-safe; never gates or retries.
	OnReasoning func(string)
}

// Runtime owns one ADK agent + runner for an explicit eino scan.
type Runtime struct {
	agent  *adk.ChatModelAgent
	runner *adk.Runner
	// toolsNode is the exact ToolsNodeConfig the agent was built with, kept so
	// tests can pin the verifier-serial binding (see ToolsNodeConfig).
	toolsNode compose.ToolsNodeConfig
}

// NewRuntime constructs the runtime. It fails closed when required
// dependencies are missing; the caller reports a typed configuration error.
// It never silently falls back after construction.
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
	baseTools, err := registryTools(cfg.Registry, cfg.ToolTimeout, cfg.OnToolTimeout)
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
	// "0 = unlimited" (the config contract): the ADK applies the cap only when
	// the value is positive and silently defaults 0 to its own 20-iteration
	// floor (adk/react.go genReactState), so translate unlimited into a
	// sentinel the run cannot reach. Duration/token/tool-call budgets still
	// bound the scan; the old <=0 → 20 floor capped every unset scan at 20
	// model requests (CORE_LOOP_AUDIT §4.2).
	maxIterations := cfg.MaxIterations
	if maxIterations <= 0 {
		maxIterations = math.MaxInt32
	}
	chatModel := NewChatModel(cfg.Client).
		WithNativeTools(cfg.NativeTools).
		WithModelRecovery(cfg.ModelRecovery).
		WithCompactor(cfg.Compactor)
	if cfg.OnReasoning != nil {
		chatModel = chatModel.WithOnReasoning(cfg.OnReasoning)
	}
	if cfg.StopCheck != nil {
		chatModel = chatModel.WithStopCheck(cfg.StopCheck)
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "xalgorix-eino",
		Description:   "Xalgorix security assessment agent",
		Instruction:   cfg.Instruction,
		Model:         chatModel,
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
	return &Runtime{agent: agent, runner: runner, toolsNode: toolConfig.ToolsNodeConfig}, nil
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

// ToolsNodeConfig returns the exact tool-node configuration the agent was
// built with. ExecuteSequentially is load-bearing: report_vulnerability's
// Gate 4.5 blocks inside the tool node while the independent Verifier runs
// its own LLM turns on the SHARED client (verifier.go's timing invariant), so
// parallel tool execution would race those turns. Pinned by
// TestRuntimeToolsNodeConfigPinsVerifierSerialExecution.
func (r *Runtime) ToolsNodeConfig() compose.ToolsNodeConfig {
	return r.toolsNode
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
			if verdict.Args != nil {
				args = verdict.Args
			}
		}
		if m.observer != nil {
			verdict := m.observer.BeforeTool(callCtx, tCtx.Name, args)
			if !verdict.Allowed {
				return denyOutput(verdict, "the engine obligation denied this call"), nil
			}
			if verdict.Args != nil {
				args = verdict.Args
			}
		}
		normalizedJSON, err := encodeRegistryArgs(args)
		if err != nil {
			return "", err
		}
		meta := &toolExecutionMeta{}
		callCtx = context.WithValue(callCtx, toolExecutionMetaKey{}, meta)
		output, callErr := endpoint(callCtx, normalizedJSON, opts...)
		// Approval interrupts are control flow, not failures: they must reach
		// the graph verbatim or ResumeWithParams loses its target.
		isInterrupt := interruptError(callErr)
		if m.observer != nil {
			if !isInterrupt {
				output = m.observer.TransformToolOutput(callCtx, tCtx.Name, args, output, meta.metadata(), callErr)
			}
			m.observer.AfterTool(callCtx, tCtx.Name, args, output, meta.metadata(), callErr)
		}
		if callErr != nil && !isInterrupt {
			// Expected execution failures are tool results the model can
			// recover from, never graph-killing errors.
			if strings.TrimSpace(output) == "" {
				output = "⛔ TOOL EXECUTION FAILED — " + callErr.Error()
			}
			return output, nil
		}
		return output, callErr
	}, nil
}

type toolExecutionMetaKey struct{}

type toolExecutionMeta struct {
	mu     sync.Mutex
	result tools.Result
	set    bool
}

func (m *toolExecutionMeta) record(result tools.Result) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.result = result
	m.set = true
}

func (m *toolExecutionMeta) metadata() map[string]any {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.set {
		return nil
	}
	return m.result.Metadata
}

func recordToolExecutionResult(ctx context.Context, result tools.Result) {
	if meta, ok := ctx.Value(toolExecutionMetaKey{}).(*toolExecutionMeta); ok {
		meta.record(result)
	}
}

func encodeRegistryArgs(args map[string]string) (string, error) {
	if len(args) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("engine: encode tool args: %w", err)
	}
	return string(encoded), nil
}

// interruptError recognizes both the first interrupt signal and the rerun
// error Eino produces after ResumeWithParams. The first signal has no exported
// type in this module, so its stable "interrupt signal:" prefix is used.
func interruptError(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := compose.IsInterruptRerunError(err); ok {
		return true
	}
	return strings.HasPrefix(err.Error(), "interrupt signal:")
}

func denyOutput(verdict GuardVerdict, fallback string) string {
	if strings.TrimSpace(verdict.Output) != "" {
		return verdict.Output
	}
	return "⛔ TOOL BLOCKED — " + fallback + "."
}
