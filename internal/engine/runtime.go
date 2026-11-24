package engine

// Runtime is the ADK execution seam. It is the only scan runtime. The runner
// is configured with a durable checkpoint store so an approval interrupt can
// resume the same run after a restart.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
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
	// Streaming makes the ADK Runner consume the model through Stream instead
	// of Generate, so each model turn arrives as an incremental MessageStream
	// (ev.Output.MessageOutput.IsStreaming) rather than one complete message.
	// The agent layer enables it for every run (no environment switch); the
	// engine only forwards the flag, and checkpointed runs persist the mode and
	// resume with it (ADK invariant).
	Streaming bool
	// DynamicTools is the B7 dynamic tool surface. When true (and NativeTools is
	// true and at least one tool is curated as searchable), the lane's tools are
	// split into a core set that is always visible and a searchable set mounted
	// through Eino's toolsearch middleware: the model sees a `tool_search`
	// meta-tool and discovers deferred tools on demand. The agent layer enables
	// it for every run; the engine decides whether it is effective (XML fallback
	// and lanes with nothing curated stay on the full tool list).
	//
	// Safety: the split is deny-by-curation (dynamic_tools.go). Only names in
	// dynamicToolSearchable, not in dynamicCoreTools and not read_/list_/search_
	// prefixed are hidden; every unknown/plugin/MCP tool stays visible. Core
	// tools also stay registered in the tool node, so a hidden-but-called tool
	// still executes — visibility, not capability, is filtered.
	DynamicTools bool
	// OnReasoning receives the reasoning model's raw thinking text for each
	// accepted provider response (fire-and-forget observability sink for the
	// sessions-page thinking role). Nil-safe; never gates or retries.
	OnReasoning func(string)
	// ToolAliases optionally replaces the built-in stable alias table
	// (canonical tool name → model-emitted aliases). Nil keeps the built-in
	// table (terminal/bash → terminal_execute, http → http_request, browser →
	// browser_action, python → python_action); a non-nil map replaces it, so an
	// empty map disables alias resolution. Aliases are only registered for
	// tools that exist in this lane's registry.
	ToolAliases map[string][]string
	// TraceSink, when non-nil, receives the Eino callback projection of every
	// model and tool invocation (start/end, duration, token usage). It is
	// mounted per run as an adk.AgentRunOption, so a handler lives only on this
	// runtime: it is never registered in the process-global handler list and
	// cannot leak into another scan. Nil installs no handler and leaves
	// behaviour and overhead unchanged.
	TraceSink TraceSink
}

// Runtime owns one ADK agent + runner for an explicit eino scan.
type Runtime struct {
	agent  *adk.ChatModelAgent
	runner *adk.Runner
	// toolsNode is the static ToolsNodeConfig the agent was built with, kept so
	// tests can pin the verifier-serial binding (see ToolsNodeConfig). When the
	// dynamic tool surface is active it holds the always-visible core set; the
	// searchable tools are appended to the runtime tool node by the tool-search
	// middleware's BeforeAgent.
	toolsNode compose.ToolsNodeConfig
	// trace is the per-runtime Eino callback handler projecting model/tool
	// lifecycle into cfg.TraceSink. Nil when no sink was configured.
	trace callbacks.Handler
	// handlers is the exact middleware chain handed to the ADK agent, kept so
	// tests can pin dynamic-tool behaviour without reaching into Eino.
	handlers []adk.ChatModelAgentMiddleware
	// toolSearch is the mounted tool-search middleware; nil when the dynamic
	// surface is off or inert (XML fallback, nothing curated as searchable).
	toolSearch adk.ChatModelAgentMiddleware
	// dynamicSearchable is the canonical name set hidden until discovered.
	// Empty when toolSearch is nil.
	dynamicSearchable []string
	// store is the durable checkpoint backend the runner writes interrupt
	// checkpoints to. It is kept so lane-recovery helpers can probe/inspect a
	// checkpoint without re-reading the config. Nil never happens for a
	// successfully constructed runtime (NewRuntime rejects a nil store).
	store adk.CheckPointStore
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
	wrapped := make([]namedTool, 0, len(baseTools))
	available := make([]string, 0, len(baseTools))
	for _, inner := range baseTools {
		info, infoErr := inner.Info(ctx)
		if infoErr != nil {
			return nil, infoErr
		}
		if cfg.Approvals != nil {
			inner = NewApprovalTool(inner, info.Name, cfg.Approvals, cfg.InstanceID, cfg.ScanContextID)
		}
		wrapped = append(wrapped, namedTool{name: info.Name, tool: inner})
		available = append(available, info.Name)
	}
	sort.Strings(available)

	// B7 dynamic tool surface. Split the lane after wrapping (so searchable
	// tools keep the approval/serial-execution wrappers) and mount the
	// visibility-filtering middleware. Inert unless all three hold: the caller
	// enabled it, the provider channel carries a native tool array, and at
	// least one tool is curated as searchable.
	nodeTools := wrapped
	var (
		toolSearch        adk.ChatModelAgentMiddleware
		dynamicSearchable []string
	)
	if cfg.DynamicTools {
		switch {
		case !cfg.NativeTools:
			log.Printf("[eino] dynamic tool surface ignored: XML fallback mode does not send a tool array to the provider, so visibility filtering has no effect")
		default:
			core, searchable := splitToolSurface(wrapped)
			if len(searchable) == 0 {
				log.Printf("[eino] dynamic tool surface ignored: no tool in this lane is curated as searchable")
				break
			}
			mw, mwErr := newToolSearchMiddleware(ctx, searchable)
			if mwErr != nil {
				return nil, fmt.Errorf("engine: tool search middleware: %w", mwErr)
			}
			toolSearch = mw
			dynamicSearchable = toolNames(searchable)
			nodeTools = core
		}
	}

	// Short model-emitted names resolve to the registered tool, but only for
	// tools this lane actually exposes. The reverse map keeps name-scoped guard
	// policies seeing the canonical name (Eino hands middlewares the alias).
	aliasConfigs, aliasToCanonical := buildToolAliases(available, cfg.ToolAliases)

	handlers := make([]adk.ChatModelAgentMiddleware, 0, 4)
	// Repair dangling tool calls first: every later handler (and the provider
	// request) must see a history whose tool_calls all have responses.
	patchHandler, err := newPatchToolCallsMiddleware(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: patch tool calls middleware: %w", err)
	}
	handlers = append(handlers, patchHandler)
	if cfg.Obligations != nil {
		handlers = append(handlers, &obligationMiddleware{observer: cfg.Obligations})
	}
	if cfg.Guard != nil || cfg.Obligations != nil {
		handlers = append(handlers, &toolGuardMiddleware{guard: cfg.Guard, observer: cfg.Obligations, aliasToCanonical: aliasToCanonical})
	}
	// Tool search mounts last: it is the only handler that rewrites ToolInfos,
	// so it must see the final tool set produced by every earlier handler.
	if toolSearch != nil {
		handlers = append(handlers, toolSearch)
	}

	toolConfig := adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               toBaseToolsNamed(nodeTools),
			ExecuteSequentially: true, // report_vulnerability and live state stay serial
			// Canonicalize scalars (trimmed strings, numeric/bool text) on the
			// way in; the guard and the executed tool then see identical args.
			ToolArgumentsHandler: normalizeToolArguments,
			ToolAliases:          aliasConfigs,
			// A tool surface can be narrower than the prose prompt (e.g. the
			// control-plane lane gets HTTP + notes only), so the model may name a
			// tool it was told about elsewhere but that is not registered here.
			// Eino's default is a FATAL "not found in toolsNode indexes" error that
			// kills the whole lane; answer with a correctable tool result instead so
			// the next turn can pick a registered tool.
			UnknownToolsHandler: func(_ context.Context, name, _ string) (string, error) {
				allowed := strings.Join(available, ", ")
				log.Printf("[eino] rejected unknown tool call %q (registered: %s)", name, allowed)
				return fmt.Sprintf(
					"error: unknown tool %q is not available in this lane. Registered tools: %s. Retry with one of the registered tools; do not call tools that are not listed.",
					name, allowed,
				), nil
			},
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
		EnableStreaming: cfg.Streaming,
		CheckPointStore: cfg.CheckpointStore,
	})
	return &Runtime{
		agent:             agent,
		runner:            runner,
		toolsNode:         toolConfig.ToolsNodeConfig,
		trace:             newTraceHandler(cfg.TraceSink),
		handlers:          handlers,
		toolSearch:        toolSearch,
		dynamicSearchable: dynamicSearchable,
		store:             cfg.CheckpointStore,
	}, nil
}

// Run starts a fresh agent execution. The checkpoint ID must be stable across a
// process restart for approval resume to work; callers should bind it to the
// scan instance/run identity.
func (r *Runtime) Run(ctx context.Context, messages []*schema.Message, checkpointID string) *adk.AsyncIterator[*adk.AgentEvent] {
	opts := r.runOptions(checkpointID)
	return r.runner.Run(ctx, messages, opts...)
}

// runOptions composes the per-run ADK options. Callback handlers are attached
// here (and nowhere global): each run receives only this runtime's handler.
func (r *Runtime) runOptions(checkpointID string) []adk.AgentRunOption {
	opts := make([]adk.AgentRunOption, 0, 2)
	if strings.TrimSpace(checkpointID) != "" {
		opts = append(opts, adk.WithCheckPointID(checkpointID))
	}
	opts = append(opts, r.callbackOptions()...)
	return opts
}

// callbackOptions mounts this runtime's trace handler when one was configured.
// Used by both Run and Resume so a resumed turn keeps the same projection; the
// handler is never installed process-wide.
func (r *Runtime) callbackOptions() []adk.AgentRunOption {
	if r == nil || r.trace == nil {
		return nil
	}
	return []adk.AgentRunOption{adk.WithCallbacks(r.trace)}
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
	}, r.callbackOptions()...)
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

func toBaseToolsNamed(tools []namedTool) []einoTool.BaseTool {
	out := make([]einoTool.BaseTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.tool)
	}
	return out
}

// toolGuardMiddleware applies the scope/safety verdict before the tool endpoint
// (and therefore before our approval interrupt). It returns a normal tool result
// for a denial so the model can choose an alternative path. aliasToCanonical
// maps a model-emitted alias (Eino passes the alias as ToolContext.Name) back to
// the registered tool name, so name-scoped guard/observer policies keep firing.
type toolGuardMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	guard            ToolGuard
	observer         TurnObserver
	aliasToCanonical map[string]string
}

func (m *toolGuardMiddleware) WrapInvokableToolCall(_ context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(callCtx context.Context, argumentsInJSON string, opts ...einoTool.Option) (string, error) {
		name := canonicalToolName(tCtx.Name, m.aliasToCanonical)
		args, err := decodeRegistryArgs(argumentsInJSON)
		if err != nil {
			return "", err
		}
		if m.guard != nil {
			verdict := m.guard.CheckTool(callCtx, name, args)
			if !verdict.Allowed {
				return denyOutput(verdict, "the application safety guard denied this call"), nil
			}
			if verdict.Args != nil {
				args = verdict.Args
			}
		}
		if m.observer != nil {
			verdict := m.observer.BeforeTool(callCtx, name, args)
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
				output = m.observer.TransformToolOutput(callCtx, name, args, output, meta.metadata(), callErr)
			}
			m.observer.AfterTool(callCtx, name, args, output, meta.metadata(), callErr)
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
