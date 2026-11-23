// Package agent provides the core agent loop.
package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"

	"github.com/c1ayooo/xalgorix/v4/internal/attacksurface"
	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/safe"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/agentsgraph"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/browser"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/codesearch"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/fileedit"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/finish"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/httpclient"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
	mcptool "github.com/c1ayooo/xalgorix/v4/internal/tools/mcp"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/notes"
	oobtool "github.com/c1ayooo/xalgorix/v4/internal/tools/oob"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/pageagent"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/portscan"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/python"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
	skillstool "github.com/c1ayooo/xalgorix/v4/internal/tools/skills"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/terminal"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/websearch"
)

var thinkRegex = regexp.MustCompile(`(?s)<think>.*?</think>`)

// toolHardTimeout maps tool names to their per-invocation hard ceiling.
// Tools not in the map fall back to defaultToolHardTimeout (15 min).
var toolHardTimeout = map[string]time.Duration{
	"terminal_execute": 65 * time.Minute,
	"browser_action":   10 * time.Minute,
	// port_scan accepts timeout up to 3600s internally; the default 15-minute
	// ceiling would cancel a legitimate full-port sweep first.
	"port_scan": 65 * time.Minute,
}

// init injects the notes-blob accessor into the hooks package so the planner
// hook can read the saved endpoint inventory without hooks.go importing the
// notes package (which would form an import cycle: notes imports tools, and
// the agent tool registry is wired from this package). agent.go already
// imports notes for pruning, so this is the natural place to bridge it.
func init() {
	notesBlobForContext = func(scanContextID string) string {
		return endpointInventoryNotes(notes.GetAllNotesForContext(scanContextID))
	}
}

// defaultToolHardTimeout is the per-invocation hard ceiling applied to any
// tool not explicitly listed in toolHardTimeout.
const defaultToolHardTimeout = 15 * time.Minute

// maxCumulativeRateLimitWait is the safe fallback when a caller constructs a
// Config literal instead of loading the environment-backed configuration.
// Set to 6 hours by default so rolling 5-hour provider quota windows
// can refresh and allow scans to resume without premature termination.
const maxCumulativeRateLimitWait = 6 * time.Hour

// maxRateLimitWait returns the per-scan ceiling for provider rate-limit waits.
// A negative configured value disables the ceiling for operators who
// explicitly need the old behavior; zero means "use the safe default" for
// programmatic Config literals.
func (a *Agent) maxRateLimitWait() time.Duration {
	if a.cfg == nil || a.cfg.MaxRateLimitWaitSec == 0 {
		return maxCumulativeRateLimitWait
	}
	if a.cfg.MaxRateLimitWaitSec < 0 {
		return 0
	}
	return time.Duration(a.cfg.MaxRateLimitWaitSec) * time.Second
}

// hardTimeoutFor returns the configured hard-timeout ceiling for the given
// tool name, falling back to defaultToolHardTimeout when the tool is not
// listed in toolHardTimeout.
func (a *Agent) hardTimeoutFor(tool string) time.Duration {
	if isExecChannelTool(tool) && int(a.execTimeoutStreak.Load()) >= execChannelBreakerThreshold {
		return execChannelDegradedTimeout
	}
	if t, ok := toolHardTimeout[tool]; ok {
		return t
	}
	return defaultToolHardTimeout
}

// ▬▬ Exec-channel circuit breaker ▬▬
//
// Reaching the agent-level hard timeout is pathological for exec-channel
// tools: terminal_execute enforces its own per-command ceiling (10m default,
// 60m heavy, plus stream/kill grace), so the force-return only fires when
// the tool layer itself is stuck — e.g. commands queued pre-launch on an
// exhausted resource lease (now bounded at resources.DefaultToolLeaseMaxWait)
// or a backend that stopped returning results. Observed in production: a
// wedged channel returned "[TIMEOUT exceeded 1h5m0s]" for even `echo hi`,
// twelve times in a row, burning ~5.5h of one scan session with the model
// blindly retrying. The breaker degrades subsequent exec calls to a short
// ceiling so each further attempt costs minutes, not hours, and quick
// health-probe commands still succeed against a healthy channel (which
// resets the breaker immediately).
const (
	// execChannelBreakerThreshold is the number of consecutive exec-tool
	// hard-timeouts after which the exec channel is presumed unhealthy.
	execChannelBreakerThreshold = 2

	// execChannelDegradedTimeout replaces the per-tool hard ceiling for exec
	// tools while the breaker is open. Long-running commands will fail
	// inside this window; that is the point — a wedged channel must not be
	// fed 65-minute timeouts, and a healthy one resets the breaker on the
	// first success.
	execChannelDegradedTimeout = 3 * time.Minute
)

// targetUnresponsiveKillThreshold is how long a continuous
// target-unresponsive streak may persist before the Run loop ends the
// scan with a typed "target_unresponsive" finish reason.
// hookTargetHealthDetector stamps state.TargetUnresponsiveSince at the
// first failure of a streak and clears it on the first healthy response;
// the kill additionally requires ConsecutiveTargetErrors >= 3 so a
// single blip followed by local-only work can never end a scan. Ten
// minutes leaves the model ~5 minutes of wrap-up (evidence notes, ledger,
// finish) after the count-3 nudge — observed wrap-up completes well
// inside that — while ending dead-target scans promptly (production
// cases ran 21–32 hours with zero progress).
const targetUnresponsiveKillThreshold = 10 * time.Minute

// isExecChannelTool reports whether a tool runs subprocesses through the
// shared terminal/lease launch path that the exec-channel breaker guards.
func isExecChannelTool(name string) bool {
	switch name {
	case "terminal_execute", "python_action":
		return true
	}
	return false
}

// Event represents an agent event (for UI updates).
type Event struct {
	Type        string // "thinking", "tool_call", "tool_result", "message", "error", "finished"
	Content     string
	ToolName    string
	ToolArgs    map[string]string
	ToolResult  tools.Result
	AgentID     string
	Timestamp   time.Time
	TotalTokens int

	// Aborted marks a "finished" event that is NOT a clean completion: the
	// agent bailed out because of an LLM-side malfunction (refused to call
	// tools, empty responses, repeated errors, provider rate-limit exhaustion).
	// Consumers use this to record the scan as failed instead of completed — a
	// forced abort produced no real result, so reporting it as "completed" is
	// misleading (and, on hosted deployments, must trigger a refund).
	// AbortReason is a short machine-readable tag (e.g. "llm_no_tool_calls").
	Aborted     bool
	AbortReason string
	// Resumable distinguishes external cancellation from a terminal outcome.
	Resumable bool
	// Meta carries structured telemetry for panel invalidation (agent_* and
	// plan_updated lifecycle frames). It is projected to WSEvent.ResultMeta and
	// is never the state authority: consumers re-read the projection APIs.
	Meta map[string]any
}

func (a *Agent) protocolResetNudge(nudge string) string {
	if a.registry == nil {
		return nudge
	}
	index := a.registry.RecoveryToolIndex()
	if index == "" {
		return nudge
	}
	return nudge + "\n\nRegistered tools (required parameters in parentheses): " + index +
		"\nAfter the reset note, call read_notes and read_ledger to recover saved progress. Use terminal_execute(command) for shell commands; do not invent tool names."
}

func (a *Agent) unknownToolRecoveryError(toolError string) string {
	if !strings.HasPrefix(toolError, "unknown tool: ") || a.registry == nil {
		return toolError
	}
	return toolError + "\nRegistered tools (required parameters in parentheses): " + a.registry.RecoveryToolIndex()
}

// Agent runs the LLM agent loop.
type Agent struct {
	ID     string
	Name   string
	cfg    *config.Config
	client *llm.Client
	// nativeToolCalling selects the OpenAI-style tool channel instead of the
	// XML-in-text dialect. Resolved once at the start of Run from the operator
	// flag plus the resolved endpoint, so it is stable for the whole scan.
	nativeToolCalling bool
	registry          *tools.Registry
	// checkpointStore/approvalBroker are the durable ADK seams. scan_session
	// injects them at construction; a caller that omits them gets a per-scan
	// file/memory store at run time.
	checkpointStore            adk.CheckPointStore
	approvalBroker             engine.ApprovalBroker
	scanFinished               atomic.Bool
	finishAccepted             atomic.Bool // set when the finish gate accepts a finish call; StopCheck reads it because AfterTool (completeWithFinish) can race the ADK's next model turn
	goalAchievedEmitted        atomic.Bool // once-only guard for the R-goal auto-stop (goal_achieved)
	forceContinuation          atomic.Bool
	dwellMaxCalls              int      // XALGORIX_DWELL_MAX_CALLS (0=off, dwell_guard.go)
	controlPlaneHosts          []string // XALGORIX_CONTROL_PLANE_HOSTS (delegation_claim.go)
	dwellMu                    sync.Mutex
	dwellStats                 map[string]*dwellStat              // 1:1 re-open of a finished run (REDESIGN D11): suppress the plan-complete auto-finalize the restored checkpoint would re-fire
	runCancel                  atomic.Pointer[context.CancelFunc] // set by runLoop; completeWithFinish cancels it so the ADK graph stops before another model call
	nextTemperature            atomic.Pointer[float64]            // one-shot per-role temperature for the next model turn
	scanCtx                    *scanctx.ScanContext               // per-session state (vulns, notes, terminal, browser)
	userSkills                 []skillstool.UserSkill             // operator-authored skills injected for THIS scan only
	userProfiles               []specialistProfile                // operator-authored delegation profiles injected for THIS scan only
	mcpServers                 []mcptool.ServerSpec               // operator-registered MCP servers for THIS scan only
	mcpManager                 *mcptool.Manager                   // per-scan MCP session owner (nil until Run)
	messages                   []llm.Message                      // Run-start conversation seed; the only reader is einoMessages() before rt.Run
	msgMu                      sync.Mutex                         // guards messages AND pendingInjects
	pendingInjects             []string                           // mid-run injections (operator messages via SendMessage, hook nudges); drained by BeforeTurn into the ADK turn nudge
	events                     chan Event
	maxIter                    int
	stopped                    atomic.Bool
	iterationDelayMs           atomic.Int64
	droppedTelemetry           atomic.Int64
	uncooperativeTimeouts      atomic.Int64
	childrenMu                 sync.Mutex
	children                   map[*Agent]struct{}
	ctx                        context.Context
	cancel                     context.CancelFunc
	lastActivity               time.Time
	activityMu                 sync.Mutex
	scanStart                  time.Time      // when Run() was called
	discoveryMode              bool           // When true, allow finish at any iteration (for Phase 1 enumeration)
	singleHostScope            bool           // single-target scan: no subdomain discovery or subdomain obligations
	allowedPhases              []int          // selected methodology phases, empty means all
	objective                  *ObjectiveSpec // operator-stated goal of this session (intent, not authority)
	reconMode                  string         // active or passive reconnaissance
	scanIntensity              string         // active or passive testing/scanning
	activityHosts              []string       // normalized target hosts used by passive policy
	targets                    []string       // raw scan targets from Run(), used to resolve a base URL for probe_hypothesis
	passiveReconGuardActive    bool           // full scans with passive recon block direct access until passive evidence is collected
	passiveReconGuardDone      bool
	passiveReconPassiveLookups int
	passiveReconBlockedActive  int
	passiveReconSourceKeys     map[string]bool
	hooks                      *HookRegistry     // extensible lifecycle hooks
	state                      *ScanState        // shared mutable scan state for hooks
	localGuard                 scopeguard.Config // operator's listener identity, consulted by shouldBlockForOutOfScope to detect Local_Or_Listener_Host references in Gated_Tool args
	oobPollFn                  oobPollFunc       // optional per-agent poller; nil uses the live OOB backend

	// Per-scan whitebox/auth config. Default to the global cfg values but can
	// be overridden PER SCAN via SetTargetAuth/SetSourceRepo before Run() so a
	// multi-tenant host never shares one target's credentials/source with
	// another target's scan.
	targetAuth       string
	targetAuthB      string // optional SECOND account, for horizontal IDOR/BOLA proof
	sourceRepo       string
	scanContext      string       // path to OpenAPI/HAR/Postman artifact(s) seeding the attack surface
	scanContextPaths []string     // all task-level + request-level context paths (multi-upload)
	codeScanMode     CodeScanMode // code-first scan mode (source review / provision), see SetCodeScanMode
	secretValues     []string     // auth values to redact from emitted telemetry

	// scanContextBriefing is built from scanContext in prepareScanEnvironment
	// and injected as a high-priority user message at the start of Run.
	scanContextBriefing string

	// initialIter and resumeBriefing preserve iteration count and previous execution
	// history when a scan is resumed/restarted across server restarts.
	initialIter    int
	resumeBriefing string
	// replayTranscript carries the 1:1 hot-resume projection of the previous
	// session's conversation (built by the web layer from events.jsonl). It is
	// prompt context only — the state authority remains scan.json + notes +
	// ledger (charter §6: events cannot replay the same decision).
	replayTranscript    string
	assignedClasses     []string
	laneScoped          bool
	checkpointLoaded    bool
	executionRestored   bool
	terminalOutcome     atomic.Pointer[terminalCheckpoint]
	resumeFindings      []reporting.Vulnerability
	publishedAssessment atomic.Pointer[assessmentSnapshot]
	// publishedPlan is the immutable read projection of the root plan. It is
	// rebuilt on the owner loop at turn boundaries and read verbatim by HTTP
	// handlers (never from mutable ScanState).
	publishedPlan   atomic.Pointer[PlanView]
	livenessExpired atomic.Bool

	// deferNoteSuppressed restores the specialist-wave defer detail latch
	// across server restarts: a resumed run emits only compact defer notes
	// instead of repeating the full-detail wall. Set from the persisted
	// scan record via SetDeferNoteDetailEmitted before Run().
	deferNoteSuppressed bool

	// agentGraph is shared by every agent delegated from this root, but owned
	// by the root scan only. The graph itself is scan-scoped, so concurrent
	// scans cannot overwrite runners or consume one another's worker slots.
	agentGraph        *agentsgraph.Graph
	ownsAgentGraph    bool
	delegatedAgentID  string
	ctfMission        bool
	benchmarkIsolated bool
	scanBudget        *scanBudget
	lastBudgetTokens  int
	// lastBudgetCostNanoUSD is this agent's reported cost already folded
	// into scanBudget.costNanoUSD. Both deltas are guarded by budgetMu: on the
	// eino path completeWithFinish folds tokens from the ADK's tool goroutine
	// while the run loop's terminal emit folds from the scan goroutine.
	lastBudgetCostNanoUSD int64
	budgetMu              sync.Mutex
	compactionCount       int
	rateLimitBackoffFn    func(int) time.Duration

	// execTimeoutStreak counts consecutive exec-channel tools
	// (terminal_execute/python_action) that hit the agent-level hard-timeout
	// force-return. It backs the exec-channel circuit breaker (see
	// execChannelBreakerThreshold): reaching the hard cap is pathological by
	// definition — the terminal tool enforces its own lower per-command
	// ceilings — so repeated hits mean the launch/exec backend is wedged.
	// Any successful exec result resets the streak.
	execTimeoutStreak atomic.Int32
}

// AgentOption configures optional behavior on a *Agent. The
// pattern lets callers (chiefly the per-scan code path in
// internal/web/server.go's runScanInstance) inject a pre-built
// llm.Client carrying a per-scan endpoint resolver, without
// reshuffling NewAgent's required-parameter signature.
//
// Validates: B1 (per-scan provider_profile resolver wiring).
type AgentOption func(*Agent)

// WithLLMClient swaps the default llm.NewClient(cfg) construction
// for a caller-supplied client. The web layer uses this to inject
// a client wrapped with llm.WithResolver(NewFixedResolver(ep)) so
// the operator's resolved provider_profile actually steers the
// scan's outbound traffic. nil resets to default behavior.
//
// Validates: B1 (per-scan provider_profile resolver wiring).
func WithLLMClient(c *llm.Client) AgentOption {
	return func(a *Agent) {
		if c != nil {
			a.client = c
		}
	}
}

// WithBenchmarkIsolation constrains an agent-driven benchmark to evidence
// obtainable through the declared target interface (plus an explicitly
// attached source tree for white-box cases). It prevents a locally hosted
// fixture from being "solved" by introspecting its Docker/container runtime.
func WithBenchmarkIsolation() AgentOption {
	return func(a *Agent) {
		a.benchmarkIsolated = true
	}
}

// WithUserSkills carries this scan's operator-authored skill set (from the
// platform store) into the per-scan skill state. Per-scan by construction:
// concurrent scans never observe each other's sets. nil = corpus only.
func WithUserSkills(skills []skillstool.UserSkill) AgentOption {
	return func(a *Agent) {
		a.userSkills = skills
	}
}

// WithAgentProfiles carries this scan's operator-authored delegation
// profiles (from the platform store; enabled filter applied by the caller).
// They merge behind the builtin specialists and participate in both the
// automatic wave and spawn_agent by role name. Per-scan by construction.
func WithAgentProfiles(profiles []specialistProfile) AgentOption {
	return func(a *Agent) {
		a.userProfiles = profiles
	}
}

// WithMcpServers carries this scan's operator-registered MCP server specs.
// Connection happens at Run start (bounded); a server that fails to connect
// or enumerate is skipped with a visible event — the scan continues without
// its tools (charter §6.6: an unavailable integration never blocks scanning).
func WithMcpServers(specs []mcptool.ServerSpec) AgentOption {
	return func(a *Agent) {
		a.mcpServers = specs
	}
}

// setupMcpTools connects the scan's MCP servers and bridges their tools into
// the registry. Called once per Run before the prompt schema snapshot so a
// bridged tool looks exactly like any registered tool to the model. The
// manager is closed when Run returns (stdio children die with the scan).
func (a *Agent) setupMcpTools() {
	if a == nil || len(a.mcpServers) == 0 {
		return
	}
	if a.mcpManager != nil {
		a.mcpManager.Close()
	}
	mgr := mcptool.NewManager()
	a.mcpManager = mgr
	for _, spec := range a.mcpServers {
		// HTTP servers dial through the same guards the HTTP tools use:
		// redirect hops re-checked, post-DNS addresses re-checked.
		spec.RequestGuard = a.checkHTTPHop
		spec.DialGuard = a.checkDialAddr
		connectCtx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
		descriptors, err := mgr.Connect(connectCtx, spec)
		cancel()
		if err != nil {
			log.Printf("[mcp] server %q unavailable: %v", spec.Name, err)
			a.emit(Event{Type: "message", Content: a.opf("⚠️ MCP server %q unavailable (%v) — its tools are not available this scan.", "⚠️ MCP 服务器 %q 不可用（%v）——本次扫描无法使用其工具。", spec.Name, err)})
			continue
		}
		names := make([]string, 0, len(descriptors))
		for _, td := range descriptors {
			names = append(names, td.Name)
		}
		mgr.BridgeTools(a.registry)
		a.emit(Event{Type: "message", Content: a.opf("🔌 MCP server %q connected: %d tool(s) registered (%s).", "🔌 MCP 服务器 %q 已连接：注册 %d 个工具（%s）。", spec.Name, len(descriptors), strings.Join(names, ", "))})
	}
}

// mergedSpecialistProfiles returns the builtin specialists followed by this
// scan's operator-authored profiles — the single delegation vocabulary the
// engine spawns from (wave, spawn_agent, nudge prompt).
func (a *Agent) mergedSpecialistProfiles() []specialistProfile {
	if a == nil {
		return defaultSpecialistProfiles
	}
	out := make([]specialistProfile, 0, len(defaultSpecialistProfiles)+len(a.userProfiles))
	out = append(out, defaultSpecialistProfiles...)
	out = append(out, a.userProfiles...)
	return out
}

// lookupUserProfile resolves an operator-authored profile by exact role
// name (spawn vocabulary). Builtin roles never match here — they resolve
// through determineAgentType.
func (a *Agent) lookupUserProfile(role string) (specialistProfile, bool) {
	if a == nil {
		return specialistProfile{}, false
	}
	for _, p := range a.userProfiles {
		if p.Role == role {
			return p, true
		}
	}
	return specialistProfile{}, false
}

// withRateLimitBackoff overrides the progressive rate-limit backoff policy.
// Used by tests to verify retry loops without sleeping real seconds.
func withRateLimitBackoff(fn func(int) time.Duration) AgentOption {
	return func(a *Agent) {
		a.rateLimitBackoffFn = fn
	}
}

// rateLimitBackoff calculates the progressive backoff interval for a 429 episode.
// 15s (attempt 1) -> 30s (attempt 2) -> 60s (attempt 3+).
func (a *Agent) rateLimitBackoff(consecutive int) time.Duration {
	if a.rateLimitBackoffFn != nil {
		return a.rateLimitBackoffFn(consecutive)
	}
	if consecutive <= 1 {
		return 15 * time.Second
	}
	if consecutive == 2 {
		return 30 * time.Second
	}
	return 60 * time.Second
}

// withAgentGraph makes a delegated agent join its root scan's graph. It is
// intentionally package-private: only the root runner should construct graph
// children, and delegated agents must never stop or replace the shared graph.
func withAgentGraph(graph *agentsgraph.Graph, budget *scanBudget, agentID string) AgentOption {
	return func(a *Agent) {
		a.agentGraph = graph
		a.scanBudget = budget
		a.delegatedAgentID = agentID
	}
}

// withParentContext makes a delegated agent's LLM and tool loop cancel when
// its root graph is stopped.
func withParentContext(parent context.Context) AgentOption {
	return func(a *Agent) {
		if parent != nil {
			a.ctx = parent
		}
	}
}

func delegatedAgentOptions(ctx context.Context, graph *agentsgraph.Graph, budget *scanBudget, agentID string, benchmarkIsolated bool) []AgentOption {
	opts := []AgentOption{
		withParentContext(ctx),
		withAgentGraph(graph, budget, agentID),
	}
	if benchmarkIsolated {
		opts = append(opts, WithBenchmarkIsolation())
	}
	return opts
}

// NewAgent creates a new agent.
// If sc is nil, a default ScanContext is used (CLI mode backward compatibility).
//
// localGuard carries the operator's listener identity (bind address +
// listener port) and is consulted by shouldBlockForOutOfScope to
// classify a Gated_Tool argument's host portion as a
// Local_Or_Listener_Host. Pass scopeguard.Config{BindAddr: "127.0.0.1",
// Port: 0} for callers that don't have a real listener (CLI / tests);
// the listener-port rule only fires when the test feeds a matching
// bind:port, so the default is safe.
//
// scOrOpts is a polymorphic tail used to keep the existing call
// sites compiling unchanged. Callers may pass:
//   - nothing (CLI default ScanContext, no options),
//   - a single *scanctx.ScanContext (existing web-server call site),
//   - one or more AgentOption (new B1 path), or
//   - a *scanctx.ScanContext followed by one or more AgentOption.
//
// Mixing both flavors keeps every old call site working while
// letting the per-scan resolver injection skip building a separate
// constructor.
func NewAgent(cfg *config.Config, name string, events chan Event, localGuard scopeguard.Config, scOrOpts ...any) *Agent {
	if cfg == nil {
		cfg = config.Get()
	}

	// A small iteration cap starves a single-target scan: recon alone consumes
	// most of it and the report never lands. 0 means unlimited (the engine
	// translates it past the ADK's implicit 20-iteration floor); a low positive
	// override is honored but flagged, because the symptom
	// (completion=partial, zero findings) looks like a model failure.
	if cfg.MaxIterations > 0 && cfg.MaxIterations < 20 {
		log.Printf("[agent] XALGORIX_MAX_ITERATIONS=%d is below the recommended minimum 20 (0 = unlimited) — single-target scans will likely end partial before reporting", cfg.MaxIterations)
	}
	fixHttpxConflict()

	// Resolve ScanContext — use provided or fall back to default.
	// Also collect any AgentOption values from the polymorphic
	// tail so callers can mix the ScanContext-only call shape with
	// AgentOption-bearing call shapes without either side knowing about the
	// other.
	var sctx *scanctx.ScanContext
	var opts []AgentOption
	for _, v := range scOrOpts {
		switch t := v.(type) {
		case *scanctx.ScanContext:
			if t != nil {
				sctx = t
			}
		case AgentOption:
			if t != nil {
				opts = append(opts, t)
			}
		case nil:
			// tolerate explicit nils so callers passing a
			// (*scanctx.ScanContext)(nil) don't surprise
			// themselves
		default:
			// Unknown type — log once and skip rather than
			// panic. This branch is unreachable under
			// vet-clean callers, but keeps the function
			// robust against future drift.
			log.Printf("[agent] NewAgent: ignoring unsupported scOrOpts argument %T", v)
		}
	}
	if sctx == nil {
		sctx = scanctx.Default()
	}

	reg := tools.NewRegistry()
	reg.SetScanContextID(sctx.ID)

	terminal.Register(reg)
	// port_scan wraps the external gogo CLI (see internal/tools/portscan).
	// Registered on the MAIN registry only: the Verifier's read-only tool
	// surface is frozen (charter §7) and a port sweep is not a re-test.
	portscan.Register(reg)
	fileedit.Register(reg)
	proxy.Register(reg)
	httpclient.Register(reg)
	browser.Register(reg)
	pageagent.Register(reg)
	// NOTE: playwright.Register removed — it registered the same "browser_action" name
	// and overwrote the enhanced rod browser with a weaker curl-based stub.
	notes.Register(reg)
	intelligence.Register(reg)
	finish.Register(reg)
	python.Register(reg)
	websearch.Register(reg)
	// NOTE: skill tools register AFTER the AgentOption tail is applied (see
	// below) so this scan's operator-authored skill set can ride WithUserSkills
	// into the per-scan skill state.
	oobtool.Register(reg)
	codesearch.Register(reg)
	reg.SetLeanSchema(cfg.LeanToolSchema)

	hookReg := NewHookRegistry()
	RegisterDefaultHooks(hookReg)

	a := &Agent{
		ID:                fmt.Sprintf("agent_%d", time.Now().UnixNano()),
		Name:              name,
		cfg:               cfg,
		client:            llm.NewClient(cfg),
		registry:          reg,
		scanCtx:           sctx,
		events:            events,
		maxIter:           cfg.MaxIterations,
		dwellMaxCalls:     cfg.DwellMaxCalls,
		controlPlaneHosts: cfg.ControlPlaneHosts,
		ctx:               context.Background(),
		lastActivity:      time.Now(),
		hooks:             hookReg,
		state:             NewScanState(),
		localGuard:        localGuard,
		targetAuth:        cfg.TargetAuth,
		targetAuthB:       cfg.TargetAuthSecondary,
		sourceRepo:        cfg.SourceRepo,
		scanContext:       cfg.ScanContext,
		children:          make(map[*Agent]struct{}),
	}
	// Redirect hops get the same local-machine and port-pin checks as the
	// URL a tool is called with, and every direct connection gets the
	// local-machine check on the address it actually dials.
	reg.SetRequestGuard(a.checkHTTPHop)
	reg.SetDialGuard(a.checkDialAddr)
	if cfg.IterationDelaySec > 0 {
		a.iterationDelayMs.Store(int64(cfg.IterationDelaySec * 1000))
	}

	// Apply per-call AgentOption values (e.g. WithLLMClient). Options
	// run after the default client is constructed so a nil option
	// leaves the default in place; WithLLMClient overwrites it with a
	// caller-supplied client carrying a per-scan resolver.
	for _, opt := range opts {
		opt(a)
	}
	// Skill tools register after the option tail: the per-scan
	// operator-authored skill set (WithUserSkills) must be on the agent
	// before the per-scan skill state is built.
	skillstool.Register(reg, cfg.SkillsDir, a.userSkills)
	if a.scanBudget == nil {
		a.scanBudget = newScanBudget()
	}
	if a.registry != nil {
		a.registry.SetContentChecker(a.hasActiveContent)
	}

	// Register the structural planner tools (build_plan / update_plan). These
	// mutate a.state.Plan, so they're registered after the agent exists. They
	// let the LLM author/refine the task graph with knowledge only it has
	// (live recon output the engine can't fully parse); the auto-plan + the
	// per-iteration nudge + the finish gate all consult the same plan.
	a.registerPlanTools(reg)

	// Register the durable hypothesis/evidence ledger tools (record_hypothesis,
	// add_hypothesis_evidence, update_hypothesis, read_ledger). Unlike the plan
	// (per-agent ScanState), the ledger lives on the shared ScanContext, so the
	// coordinator and every delegated specialist read/write the same graph and
	// it persists across restart/resume.
	a.registerLedgerTools(reg)

	// Read-only InfoItem activation over the optional PostgreSQL cold path.
	// This tool never participates in verdict or finding acceptance.
	a.registerInfoTools(reg)

	// Register the multi-role authorization matrix (authz_matrix). It replays a
	// request as role A / role B / anonymous and records the access-control
	// differential to the ledger. Agent-bound because it needs both configured
	// account credentials, the scope config, and the ledger.
	a.registerAuthzMatrixTool(reg)

	// Register the out-of-band blind-vulnerability verifier (verify_oob). It
	// polls the OAST oracle for a planted token and records blind-execution
	// proof to the ledger. Agent-bound for ledger access; makes no outbound
	// request itself, so no scope gate is needed.
	a.registerOOBVerifyTool(reg)

	// Register the bounded timing-differential verifier (verify_timing). It is
	// the in-band fallback for blind server-side injection when reconnaissance
	// yields a safe delay primitive but OAST is unavailable or ambiguous.
	a.registerVerifyTimingTool(reg)

	// Register HAR ingestion (ingest_har): turns a logged-in HAR into live
	// authenticated scan context — registers session auth and seeds the ledger
	// with authenticated-endpoint authorization hypotheses.
	a.registerHARIngestTool(reg)

	// Register the whitebox source-to-runtime bridge (scan_source_sinks): sweeps
	// the attached source tree for dangerous sinks and seeds each as a
	// source->sink hypothesis (Endpoint=file:line, Origin=source-sink) so the
	// evidence-driven loop can trace it to a reachable route and exploit it.
	a.registerScanSourceSinksTool(reg)

	// Register the second half of the bridge (scan_source_routes): extracts HTTP
	// route declarations from the attached source and seeds each as a hypothesis
	// with a REAL reachable path (Origin=source-route), correlating a route with
	// dangerous sinks in the same handler file so the sink gets an attackable
	// endpoint.
	a.registerScanSourceRoutesTool(reg)

	// Register the third part of the bridge (probe_hypothesis): issues ONE
	// scope-gated baseline request against the live target for a ledger
	// hypothesis that carries a real HTTP path (e.g. a source-route hypothesis)
	// and records the response as evidence — turning a seeded path into a
	// confirmed-reachable (or blocked) lead. Agent-bound: needs the scope
	// config, session auth, the scan target, and the ledger.
	a.registerProbeHypothesisTool(reg)

	// Register the deterministic error-based SQL-injection confirmer
	// (verify_sqli): the SQLi counterpart of verify_xss. Given a seeded hypothesis
	// or a url + parameter, it sends a benign baseline, a single-quote payload,
	// and a doubled-quote payload, and confirms injection when a DBMS error
	// appears on the broken request but not on the baseline — recording
	// exploit-proven evidence in the ledger. Agent-bound: needs the scope config,
	// session auth, the scan target, and the ledger.
	a.registerVerifySQLiTool(reg)

	// Register the deterministic server-side template-injection confirmer
	// (verify_ssti): the SSTI sibling of verify_sqli. Given a seeded hypothesis
	// or a url + parameter, it sends a benign baseline plus {{a*b}} / ${a*b}
	// payloads with randomized operands, and confirms injection when the
	// evaluated product appears in the probe response but not the baseline —
	// recording exploit-proven evidence in the ledger. Agent-bound: needs the
	// scope config, session auth, the scan target, and the ledger.
	a.registerVerifySSTITool(reg)
	a.registerVerifyPathTraversalTool(reg)

	// Register the JSON body-contract discovery probe (probe_body_contract):
	// arjun and the surface machinery only see query/form parameters, so a JSON
	// API that answers with its own validation error is a dead end. The probe
	// mutates body fields (remove/add/retype) and diffs the app's status and
	// error signatures to classify required/recognized/rejected fields, so the
	// agent can build a valid body and inject into the real parameters.
	// Agent-bound: needs the scope config, the scan target, and the ledger.
	a.registerProbeBodyContractTool(reg)

	// Register the deterministic authentication/authorization enforcement-bypass
	// confirmer (verify_authbypass): the path-shape sibling of authz_matrix.
	// Given a protected canonical route (or hypothesis), it re-requests the
	// canonical path unauthenticated as a denied baseline, then tries equivalent
	// path-shape variants (case fold, percent-encoding, trailing separators)
	// unauthenticated and without normalization, confirming a bypass only on a
	// deny→allow status flip whose served body diverges from the denied baseline.
	// Agent-bound: needs the scope config, the scan target, and the ledger.
	a.registerVerifyAuthBypassTool(reg)

	// Register the deterministic XML External Entity confirmer (verify_xxe): the
	// XXE sibling of verify_sqli/verify_ssti. Given a seeded hypothesis or a url,
	// it sends a randomized benign baseline, a nonexistent-file control, and
	// two repeated local-file entity probes. It requires stable file-derived
	// evidence absent from both controls, and automatically tries a target-
	// attributed OAST callback for non-reflective parsers before recording
	// exploit-proven evidence. Agent-bound: needs the scope config, session auth,
	// the scan target, and the ledger.
	a.registerVerifyXXETool(reg)

	// Register the Cross-Site Request Forgery confirmer (verify_csrf): given a
	// url (or hypothesis) and the state-change body, it replays the request with
	// a forged cross-site Origin/Referer and no anti-CSRF token, and confirms
	// CSRF when the server accepts it — declining when the endpoint is protected
	// by an Authorization header or has no ambient cookie session (neither is
	// proof of CSRF). Agent-bound: needs the scope config, session auth, the scan
	// target, and the ledger.
	a.registerVerifyCSRFTool(reg)

	// Bounded working context retrieval tool: returns the byte-identical
	// archived original of any stubbed tool result (flag-gated stubs, but the
	// tool is always registered so archived outputs are reachable).
	a.registerToolArchiveTool(reg)

	// Create cancellable context
	a.ctx, a.cancel = context.WithCancel(a.ctx)
	// Wire context to LLM client so cancel interrupts pending HTTP requests
	a.client.SetContext(a.ctx)

	if a.agentGraph == nil {
		a.ownsAgentGraph = true
		// The lifetime cap must cover every engine lane plus one manual
		// model-spawned agent: the early recon-discovery lane launches before
		// the testing wave, so the old default (3) would strand the wave.
		// Operator-authored profiles join the wave, so the merged set sizes
		// the cap.
		a.agentGraph = agentsgraph.NewWithLimit(a.ctx, len(a.mergedSpecialistProfiles())+1, func(ctx context.Context, agentID, subName string, targets []string, task string) (string, error) {
			subEvents := make(chan Event, 256)
			// All descendants join this root's graph and inherit its cancellation
			// context. Creating a child therefore cannot overwrite another scan's
			// runner or leave an uncancellable worker behind.
			subArgs := []any{sctx}
			for _, opt := range delegatedAgentOptions(ctx, a.agentGraph, a.scanBudget, agentID, a.benchmarkIsolated) {
				subArgs = append(subArgs, opt)
			}
			// Specialists run the same runtime as the coordinator. Their unique
			// delegatedAgentID gives them a per-agent checkpoint namespace.
			if a.checkpointStore != nil {
				subArgs = append(subArgs, WithRuntime(a.checkpointStore, a.approvalBroker))
			}
			if a.client != nil {
				subArgs = append(subArgs, WithLLMClient(a.client.Clone()))
			}
			subAgent := NewAgent(cfg, subName, subEvents, a.localGuard, subArgs...)
			a.registerChildAgent(subAgent)
			defer a.unregisterChildAgent(subAgent)
			subAgent.SetPhaseRestrictions(a.allowedPhases)
			// The specialist inherits the operator objective so its lane plan
			// marks the tasks that serve a criterion (marking only).
			if a.objective != nil {
				subAgent.SetObjective(a.objective)
			}
			for _, profile := range a.mergedSpecialistProfiles() {
				if profile.Role == subName {
					subAgent.laneScoped = true
					subAgent.assignedClasses = append([]string(nil), profile.VulnClasses...)
					break
				}
			}
			subAgent.SetActivityPolicy(a.reconMode, a.scanIntensity, a.activityHosts)
			subAgent.SetTargetAuth(a.targetAuth)
			subAgent.SetTargetAuthSecondary(a.targetAuthB)
			subAgent.SetSourceRepo(a.sourceRepo)
			subAgent.SetScanContext(a.scanContext)
			subAgent.SetCodeScanMode(a.codeScanMode)
			if a.discoveryMode {
				subAgent.SetDiscoveryMode(true)
			}
			subAgent.SetSingleHostScope(a.singleHostScope)
			// Role-scoped tool documentation (flag-gated): withhold
			// role-foreign tool docs from this specialist's prompt. Tools stay
			// registered/callable and remain listed in a compact index line.
			subAgent.applyRoleToolScope()

			var results strings.Builder
			var delegatedErr error
			done := make(chan struct{})
			safe.Go("agent.subagent_stream", a.scanCtx.ID, func() {
				defer close(done)
				for evt := range subEvents {
					if evt.Type == "tool_result" && evt.ToolResult.Output != "" {
						partial := fmt.Sprintf("[%s] %s", evt.ToolName, truncStr(evt.ToolResult.Output, 200))
						results.WriteString(partial)
						results.WriteByte('\n')
						a.agentGraph.AddPartialResult(agentID, partial)
					}
					if evt.Type == "finished" {
						results.WriteString("\nCompleted: ")
						results.WriteString(truncStr(evt.Content, 500))
						results.WriteString("\n")
						if evt.Resumable {
							delegatedErr = context.Canceled
						} else if evt.Aborted {
							delegatedErr = fmt.Errorf("delegated agent aborted: %s", valueOr(evt.AbortReason, evt.Content))
						}
					}
					if a.events != nil {
						parentEvt := evt
						parentEvt.AgentID = agentID
						safeSend(a.events, parentEvt, 0)
					}
				}
			})
			// These defers also run if Agent.Run panics and the graph's panic
			// boundary recovers it, so the event-forwarder cannot become a zombie.
			defer subAgent.Stop()
			streamClosed := false
			defer func() {
				if !streamClosed {
					close(subEvents)
					<-done
				}
			}()
			delegatedTask := buildDelegatedTaskInstruction(task, agentID, a.ctfMission, a.cfgDelegationUnlocked())
			subAgent.Run(targets, delegatedTask)
			close(subEvents)
			<-done
			streamClosed = true
			return results.String(), delegatedErr
		})
	}
	// Delegation lifecycle audit → per-scan domain journal (best-effort: a
	// journal-less context simply never records; delegation itself is never
	// blocked by audit failures).
	a.agentGraph.SetAuditSink(func(op domainjournal.Operation, summary string) {
		sc := scanctx.Get(a.scanCtx.ID)
		if sc == nil || sc.Ledger == nil {
			return
		}
		if err := sc.Ledger.AppendInvocationAudit(op, domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "agentsgraph"}, summary); err != nil {
			log.Printf("[audit] delegation record skipped: %v", err)
		}
	})
	// Delegation lifecycle telemetry → the scan event stream. These frames only
	// invalidate the client's cached /agents projection; the projection stays
	// authoritative (charter §5.1: telemetry is not state).
	//
	// Only the ROOT installs the sink: every child shares the same graph, and a
	// child closure would emit into its own subEvents channel (closed when the
	// child exits), silently killing later notifications for every sibling.
	if a.delegatedAgentID == "" {
		a.agentGraph.SetStateSink(func(ev agentsgraph.StateEvent) {
			a.emit(Event{
				Type:    ev.Kind,
				Content: ev.Lane,
				Meta: map[string]any{
					"agent_id": ev.AgentID,
					"lane":     ev.Lane,
					"status":   ev.Status,
					"task":     ev.Task,
					"target":   ev.Target,
					"limit":    ev.Limit,
					"running":  ev.Running,
					"queued":   ev.Queued,
				},
			})
		})
	}
	// Only the root coordinator can delegate. Prompt wording alone is not a
	// sufficient boundary: removing the graph tools from child registries makes
	// nested delegation impossible and keeps the one-wave lifetime budget exact.
	if a.delegatedAgentID == "" {
		a.agentGraph.Register(reg)
	}

	// A coordinator cannot silently finish while delegated evidence is still
	// running or has not been collected. Descendants skip this gate because the
	// root coordinator is responsible for collecting the whole graph.
	if a.ownsAgentGraph {
		a.hooks.Register(OnToolCall, a.delegatedWorkReDoSGuard)
		a.hooks.Register(OnFinishAttempt, a.delegatedWorkFinishGate)
	}

	// Bind verification to THIS registry/agent. Parallel hunters can now report
	// concurrently without borrowing another agent's LLM client or overwriting a
	// scan-context-global verifier callback.
	reporting.RegisterWithVerifier(reg, a.verifyFinding)

	return a
}

func (a *Agent) delegatedWorkFinishGate(_ *ScanState, _ map[string]string) HookResult {
	if a == nil || a.agentGraph == nil {
		return HookResult{}
	}
	if running := a.agentGraph.RunningCount(); running > 0 {
		return HookResult{Block: true, BlockReason: fmt.Sprintf("%d delegated agent(s) are still running. Collect their evidence before finishing:\n%s", running, a.agentGraph.PendingSummary())}
	}
	if pending := a.agentGraph.UncollectedCount(); pending > 0 {
		return HookResult{Block: true, BlockReason: fmt.Sprintf("%d delegated result(s) have not been collected. Read them before finishing:\n%s", pending, a.agentGraph.PendingSummary())}
	}
	return HookResult{}
}

// The final ReDoS stage also waits for specialist results; the root plan can
// be settled while a delegated lane is still exercising its own hypotheses.
func (a *Agent) delegatedWorkReDoSGuard(state *ScanState, args map[string]string) HookResult {
	if a == nil || !isReDoSAction(state, args) {
		return HookResult{}
	}
	if gate := a.delegatedWorkFinishGate(state, args); gate.Block {
		return HookResult{ForceSkip: true, Nudge: "⛔ ReDoS testing is the final availability stage. " + gate.BlockReason}
	}
	return HookResult{}
}

// noteDelegationDefer records why the specialist wave has not launched and
// emits a diagnostic message whenever the reason changes. Without this, a wave
// that never fired was invisible in the scan event stream and indistinguishable
// from a wave that ran and finished — production scans lost their specialist
// acceleration with no trace of why.
func (a *Agent) noteDelegationDefer(reason string) {
	if a == nil || a.state == nil {
		return
	}
	// Surface operator-disabled lanes in the defer message so the log
	// explains itself: the operator turned them off, the engine didn't lose
	// them.
	if a.cfg != nil && len(a.cfg.DisabledSpecialists) > 0 && strings.Contains(reason, "not complete") {
		reason += " (operator-disabled lanes: " + strings.Join(a.cfg.DisabledSpecialists, ", ") + ")"
	}
	if a.state.DelegationDeferReason == reason {
		return
	}
	a.state.DelegationDeferReason = reason
	// The reason enumerates every missing recon milestone. Repeating
	// near-identical walls of text on every reason change drowned the
	// operator feed, and the in-memory latch was lost on every server
	// restart so the wall re-appeared from scratch. Emit the full detail
	// ONCE per scan lifetime (persisted via the scan record so resumes
	// stay quiet), then keep change-triggered follow-ups to one line.
	if !a.state.DelegationDeferDetailEmitted {
		a.state.DelegationDeferDetailEmitted = true
		a.emit(Event{Type: "message", Content: a.opf("⏸️ Specialist wave deferred: %s. The wave launches automatically once the blocker clears (configuration blockers need an operator change).", "⏸️ 专家组暂缓：%s。阻碍解除后自动启动（配置类阻碍需要操作者介入）。", reason)})
		return
	}
	a.emit(Event{Type: "message", Content: a.op("⏸️ Specialist wave still deferred: ", "⏸️ 专家组仍处暂缓：") + compactDeferReason(reason)})
}

// compactDeferReason shortens a defer reason to a single line for
// follow-up notes. Rune-safe: the reasons contain non-ASCII punctuation.
func compactDeferReason(reason string) string {
	const maxRunes = 140
	runes := []rune(reason)
	if len(runes) <= maxRunes {
		return reason
	}
	return string(runes[:maxRunes-1]) + "…"
}

// reconPhaseComplete reports whether the comprehensive reconnaissance
// milestone is met: fingerprinting/banners, a saved endpoint inventory, real
// content discovery, and at least one detected technology. The specialist
// wave and the coordinator's hypothesis-claiming lane wait for this so every
// run partitions the SAME fully mapped surface. Launching testing earlier —
// from whatever thin inventory exists a minute into the scan — was the
// largest source of run-to-run result variance: identical targets produced
// specialist lanes grounded in 13 paths one run and 17 the next.
func (a *Agent) reconPhaseComplete() bool {
	if a == nil || a.state == nil {
		return false
	}
	// THE authoritative comprehensive-recon predicate (recon_predicate.go).
	// The wave gate, the recon plan task, and the professional finish gate
	// all share this single definition, so enabling or disabling specialists
	// can never change what reconnaissance is owed.
	return ComprehensiveReconComplete(a.state)
}

// isDeepMode reports whether the scan runs in deep-intensity mode. Deep used
// to be inferred from len(AllowedPhases) >= 20, which misread the EMPTY
// selection (meaning the full methodology is allowed) as "not deep" and
// silently disabled deep-mode recon requirements for full scans. The explicit
// ScanDepth field is now derived once at scan start; this accessor stays for
// compatibility.
func (a *Agent) isDeepMode() bool {
	if a == nil || a.state == nil {
		return false
	}
	return a.state.ScanDepth == "deep"
}

// reconIncompleteReasons lists the reconnaissance milestones still missing,
// for wave/claim block messages.
func (a *Agent) reconIncompleteReasons() []string {
	if a == nil || a.state == nil {
		return nil
	}
	// One authoritative reason generator (recon_predicate.go) serves the
	// wave deferral, the recon plan task, and the finish contract.
	return ComprehensiveReconMissing(a.state)
}

// maybeAutoDelegate launches the one deterministic specialist wave once the
// comprehensive reconnaissance milestone (reconPhaseComplete) has produced a
// final surface, grounded plan, and shared ledger. Requiring the coordinator LLM
// to remember spawn_agent proved nondeterministic in real runs: it could ignore
// several explicit nudges and continue serial work until the scan deadline.
// Engine-owned launch makes coverage parallel by construction while the graph's
// lifetime cap and child tool registry still make additional/nested waves
// impossible. Roles with hard prerequisites are filtered deterministically:
// authorization work is not launched without a configured or ingested
// authenticated identity, because an anonymous-only specialist otherwise
// degenerates into password spraying and disabled-signup probing.
func (a *Agent) maybeAutoDelegate(targets []string) string {
	if a == nil || a.state == nil || a.registry == nil || a.agentGraph == nil ||
		a.delegatedAgentID != "" || a.ctfDelegationBlocked() || a.state.DiscoveryMode ||
		a.state.ReconOnlyMode {
		return ""
	}
	// DisableAutoDelegate = no deterministic wave; manual spawn stays open.
	if a.cfg != nil && a.cfg.DisableAutoDelegate {
		return ""
	}
	// Opt-in bounded reschedule (P2, XALGORIX_WAVE_RESCHEDULE): after the first
	// wave drains, still-unfinished plan classes may justify ONE more lane.
	// Checked before the DelegationAttempted early return below, which the
	// initial wave sets and which would otherwise end delegation permanently.
	if a.state.WaveLaunched {
		if !a.state.DelegationEnabled {
			return ""
		}
		if note := a.maybeRescheduleWave(targets); note != "" {
			return note
		}
	}
	if a.state.DelegationAttempted {
		return ""
	}
	// Single-agent mode (XALGORIX_DISABLE_AUTO_DELEGATE=true, or every
	// specialist lane operator-disabled): an intentional configuration, not
	// a failure state. No wave, no defer diagnostics, no reminders — and
	// DelegationAttempted must NEVER be set, because finish/adaptive logic
	// reads it as "a specialist wave ran" and would inflate iteration
	// requirements for a scan that never had specialists. The root agent
	// owns the complete methodology; every coverage gate applies unchanged.
	if !a.state.DelegationEnabled {
		if !a.state.SingleAgentNoted {
			a.state.SingleAgentNoted = true
			a.emit(Event{Type: "message", Content: a.op("🧭 Single-agent mode: specialist delegation is disabled by configuration. The root agent owns the complete methodology — reconnaissance, plan, testing, verification, and reporting — and every coverage gate applies unchanged. No specialist wave will be launched.", "🧭 单代理模式：专家委派已被配置禁用。根代理独立完成全方法论——侦察、规划、测试、验证与报告——所有覆盖闸门照常生效，不会启动专家组。")})
		}
		return ""
	}
	if _, ok := a.registry.Get("spawn_agent"); !ok {
		a.noteDelegationDefer("spawn_agent tool is not available in this agent's registry")
		return ""
	}

	// ── Stage E: early discovery lane ──
	// The recon-discovery specialist launches as soon as the plan + ledger
	// exist — in PARALLEL with the root's remaining baseline recon (bounded
	// dirbust, tech detection). Deep enumeration beyond the baseline is the
	// highest-leverage coverage lever: undiscovered surface is silently
	// untested surface. Its discoveries flow back through the shared
	// Discovery Manifest note, which the root's planner re-extracts into the
	// plan's endpoint set every iteration. Skipped when the model already
	// spawned anything itself (manual delegation disables engine launches).
	if !a.state.ReconLaneLaunched && a.state.Plan != nil && a.state.PlanBuilt &&
		a.state.LedgerSeeded && a.state.EndpointInventorySaved && a.state.Iteration >= 5 &&
		a.agentGraph.DelegationCount() == 0 {
		for _, profile := range defaultSpecialistProfiles {
			if profile.Role != "recon-discovery" || a.specialistDisabled(profile.Role) {
				continue
			}
			if id := a.spawnSpecialistProfile(profile, targets); id != "" {
				a.state.ReconLaneLaunched = true
				a.state.DelegationNudgeFired = true
				a.state.DelegationDeferReason = ""
				return fmt.Sprintf("🔭 ENGINE DISCOVERY LANE STARTED: launched the recon-discovery specialist (%s). It enumerates BEYOND your baseline — extensions, params, JS routes, backups, source files — and folds live-verified routes into the 'Discovery Manifest' note; your plan absorbs them automatically. YOU continue the baseline recon NOW (bounded wordlist pass + tech detection): the testing wave still waits for YOUR recon milestones. Do not spawn another discovery agent.", id)
			}
		}
	}

	// ── Stage W: the testing wave ──
	// One wave over the testing lanes, gated on comprehensive recon as before.
	// DelegationCount is compared against the engine's own launches: if the
	// model manually spawned anything beyond the discovery lane, the engine
	// stays out of delegation entirely (the manual wave owns the graph).
	engineLaunched := 0
	if a.state.ReconLaneLaunched {
		engineLaunched++
	}
	if a.state.WaveLaunched || !a.reconPhaseComplete() || a.state.Iteration < 5 || a.state.Plan == nil ||
		!a.state.PlanBuilt || !a.state.LedgerSeeded || a.agentGraph.DelegationCount() > engineLaunched {
		if a.state.Iteration >= 5 && !a.state.WaveLaunched {
			a.noteDelegationDefer("comprehensive recon not complete (missing: " + strings.Join(a.reconIncompleteReasons(), ", ") + ")")
		}
		return ""
	}

	// The wave is grounded in the FINAL mapped surface because plan refresh
	// moved into the planner: hookPlanner refreshes the engine-owned plan on
	// every surface-revision change, with or without specialists, so by the
	// time the wave launches the plan already reflects the completed
	// surface. Delegation code no longer rebuilds plans — disabling every
	// specialist leaves plan completeness byte-identical.

	profiles := a.eligibleSpecialistProfiles()
	spawned := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if a.specialistDisabled(profile.Role) {
			continue
		}
		if id := a.spawnSpecialistProfile(profile, targets); id != "" {
			spawned = append(spawned, id)
		}
	}
	if len(spawned) == 0 {
		return ""
	}
	a.state.DelegationAttempted = true
	a.state.WaveLaunched = true
	a.state.WaveCount = 1
	a.state.DelegationNudgeFired = true
	a.state.DelegationDeferReason = ""
	return fmt.Sprintf("🚀 ENGINE DELEGATION STARTED: launched %d non-overlapping specialists (%s). "+
		"Continue the root's highest-value remaining work NOW — the specialist wave is a parallel "+
		"accelerator, not a substitute for your own testing. While they run: (1) test endpoint/class "+
		"pairs they do NOT cover, (2) do your own deep payload work on high-value routes, (3) "+
		"periodically collect results with check_agent/wait_agent, and (4) after ALL specialists are "+
		"collected, independently verify every claimed finding and CONTINUE TESTING uncovered surface — "+
		"specialist completion is NOT a finish signal. Do not launch another wave.",
		len(spawned), strings.Join(spawned, ", "))
}

// waveRescheduleEnabled is the opt-in switch for a bounded second wave. It is
// env-only on purpose: this changes delegation dynamics, so it stays off unless
// an operator deliberately enables it.
func waveRescheduleEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XALGORIX_WAVE_RESCHEDULE"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// maybeRescheduleWave launches at most one additional specialist lane after the
// first wave has fully drained, and only for a role whose assigned classes are
// still unfinished in the plan. Bounds that keep it safe:
//   - opt-in (XALGORIX_WAVE_RESCHEDULE) and at most 2 waves per scan;
//   - the previous wave must be idle (no running/queued agent);
//   - the lifetime delegation budget must still have room;
//   - one lane per call, so the coordinator keeps integrating evidence.
//
// The finish gate is untouched: an extra lane only adds work the plan already
// owes, so it cannot shorten or bypass any obligation.
func (a *Agent) maybeRescheduleWave(targets []string) string {
	if a == nil || a.agentGraph == nil || a.state == nil || a.state.Plan == nil {
		return ""
	}
	if !waveRescheduleEnabled() || a.state.WaveCount == 0 || a.state.WaveCount >= 2 {
		return ""
	}
	if a.agentGraph.RunningCount() > 0 {
		return ""
	}
	if a.agentGraph.DelegationCount() >= a.agentGraph.MaxAgents() {
		return ""
	}
	unfinished := make(map[string]bool)
	for _, task := range a.state.Plan.Tasks {
		if task == nil || task.VulnClass == "" {
			continue
		}
		if task.Status == TaskPending || task.Status == TaskActive {
			unfinished[strings.ToLower(task.VulnClass)] = true
		}
	}
	if len(unfinished) == 0 {
		return ""
	}
	liveLanes := make(map[string]bool)
	for _, agent := range a.agentGraph.Snapshot().Agents {
		if agent.Status == "running" || agent.Status == "queued" {
			liveLanes[agent.Lane] = true
		}
	}
	for _, profile := range a.mergedSpecialistProfiles() {
		if profile.Role == "recon-discovery" || liveLanes[profile.Role] || a.specialistDisabled(profile.Role) {
			continue
		}
		overlap := false
		for _, class := range profile.VulnClasses {
			if unfinished[strings.ToLower(class)] {
				overlap = true
				break
			}
		}
		if !overlap {
			continue
		}
		if id := a.spawnSpecialistProfile(profile, targets); id != "" {
			a.state.WaveCount++
			return fmt.Sprintf("🔁 ENGINE WAVE RESCHEDULE (opt-in): the first wave drained while %d plan class(es) are still unfinished, so lane %q was relaunched (%s). Keep integrating evidence; specialist completion is still NOT a finish signal.",
				len(unfinished), profile.Role, id)
		}
	}
	return ""
}

func (a *Agent) eligibleSpecialistProfiles() []specialistProfile {
	profiles := make([]specialistProfile, 0, len(defaultSpecialistProfiles)+len(a.userProfiles))
	for _, profile := range defaultSpecialistProfiles {
		// The discovery lane launches EARLY (stage E), not with the testing
		// wave — its work is the wave's prerequisite, not a testing lane.
		if profile.Role == "recon-discovery" {
			continue
		}
		if profile.Role == "authz-logic" && len(a.authzIdentities()) <= 1 {
			continue
		}
		// Operator-toggled lanes are excluded from the wave entirely.
		if a.specialistDisabled(profile.Role) {
			continue
		}
		profiles = append(profiles, profile)
	}
	// Operator-authored profiles join the testing wave unconditionally: the
	// enabled filter was applied at fetch (integration_inputs) and the
	// operator toggle still applies. They get no special prerequisite
	// gating — their contract is the operator's responsibility.
	for _, profile := range a.userProfiles {
		if a.specialistDisabled(profile.Role) {
			continue
		}
		profiles = append(profiles, profile)
	}
	return profiles
}

// spawnSpecialistProfile launches one specialist with the standard lane
// contract and returns its agent id ("" when the spawn was rejected).
func (a *Agent) spawnSpecialistProfile(profile specialistProfile, targets []string) string {
	target := ""
	if len(targets) > 0 {
		target = strings.TrimSpace(targets[0])
	}
	brief, laneHypothesisIDs := a.specialistLaneBrief(profile)
	task := fmt.Sprintf(`Own ONLY the %q lane against %s.
Assigned vulnerability classes: %s.
Start with read_ledger(filter=schedulable), then claim_next_hypothesis separately for every assigned class. Do not repeat root reconnaissance or work outside this lane.
Local workspace: create and use tmp/%s/ for every scanner-side artifact. Never use host /tmp and never read or overwrite another lane's scratch files.
Required proof: %s.
Stopping rule: %s.%s`, profile.Role, target, strings.Join(profile.VulnClasses, ", "), profile.Role, profile.EvidenceContract, profile.StoppingRule, brief)
	args := map[string]string{
		"name": profile.Role,
		"task": task,
	}
	if target != "" {
		args["target"] = target
	}
	a.emit(Event{Type: "tool_call", ToolName: "spawn_agent", ToolArgs: args})
	result, err := a.registry.Execute("spawn_agent", args)
	if err != nil {
		result = tools.Result{Error: err.Error()}
	}
	a.emit(Event{Type: "tool_result", ToolName: "spawn_agent", ToolResult: result})
	if result.Metadata == nil || result.Metadata["spawned"] != true {
		return ""
	}
	id, _ := result.Metadata["agent_id"].(string)
	if id != "" && len(laneHypothesisIDs) > 0 {
		if l := a.ledger(); l != nil {
			for _, h := range laneHypothesisIDs {
				// Soft lane ownership: the hypotheses stay queued and
				// claimable, but the child's finish gate can now see
				// assigned-but-never-claimed work instead of an empty lane
				// looking exhausted.
				l.MarkAssigned(h, id)
			}
		}
	}
	return id
}

// specialistLaneBrief grounds a specialist's spawn task in CONCRETE schedulable
// work: the live ledger's queued hypotheses for its assigned classes, plus the
// resolved methodology skills for those classes. "Test business logic
// everywhere" becomes "H-18 business-logic POST /api/checkout", and the class
// knowledge loads deterministically instead of hoping the child remembers the
// catalog.
func (a *Agent) specialistLaneBrief(profile specialistProfile) (string, []string) {
	var b strings.Builder
	l := a.ledger()

	// Concrete hypotheses per assigned class (bounded).
	var lines []string
	var ids []string
	if l != nil {
		for _, class := range profile.VulnClasses {
			count := 0
			for _, h := range l.All() {
				if count >= 3 {
					break
				}
				if h.Status != scanctx.HypothesisQueued && h.Status != scanctx.HypothesisBlocked {
					continue
				}
				if !vulnClassMatches(h.VulnClass, class) {
					continue
				}
				if strings.TrimSpace(h.AssignedTo) != "" {
					continue // already owned by another lane
				}
				loc := strings.TrimSpace(h.VulnClass + " " + h.Endpoint)
				if h.Parameter != "" {
					loc += " [" + h.Parameter + "]"
				}
				lines = append(lines, fmt.Sprintf("  • %s: %s", h.ID, loc))
				ids = append(ids, h.ID)
				count++
			}
		}
	}
	if len(lines) > 0 {
		sort.Strings(lines)
		b.WriteString("\nAssigned concrete hypotheses (claim these FIRST, one per class, until the lane is exhausted):\n")
		b.WriteString(strings.Join(lines, "\n"))
	}

	// Resolved lane methodology (1-3 skills).
	var skills []string
	for _, class := range profile.VulnClasses {
		if name, ok := VulnClassSkill(class); ok && name != "" {
			dup := false
			for _, s := range skills {
				if s == name {
					dup = true
					break
				}
			}
			if !dup {
				skills = append(skills, name)
			}
		}
		if len(skills) >= 3 {
			break
		}
	}
	if len(skills) > 0 {
		quoted := make([]string, 0, len(skills))
		for _, s := range skills {
			quoted = append(quoted, strconv.Quote(s))
		}
		b.WriteString("\nLane methodology: load read_skill(name=" + strings.Join(quoted, "), then read_skill(name=") + ") before your first probe.")
	}
	return b.String(), ids
}

// vulnClassMatches compares a ledger hypothesis class with a lane class,
// normalizing both through the canonical registry so "bola" matches "idor" and
// "race-condition" matches "race-conditions".
func vulnClassMatches(have, want string) bool {
	have, want = strings.TrimSpace(have), strings.TrimSpace(want)
	if have == "" || want == "" {
		return false
	}
	if strings.EqualFold(have, want) {
		return true
	}
	hc, wc := CanonicalVulnClassID(have), CanonicalVulnClassID(want)
	return hc != "" && hc == wc
}

// scannerTemperatureFor computes the sampling temperature for the next
// request from the live recovery state. The scan defaults to fully
// deterministic (TempScanner); a small 0.2 bump is applied when stuck,
// retrying failed calls, or recovering from malformed tool output — a
// deterministic sample tends to re-emit the exact same broken markup turn
// after turn, so the bump is the cheapest way to break that loop while the
// protocol nudges do the formatting work.
func scannerTemperatureFor(state *ScanState) float64 {
	t := 0.0
	if state == nil {
		return t
	}
	if state.ConsecutiveSameCall >= 2 || state.ConsecutiveSameResult >= 2 || state.ConsecutiveErrors > 0 {
		t = 0.2
	}
	if state.MalformedToolOutputCount > 0 && t < 0.2 {
		t = 0.2
	}
	return t
}

// appendUserNudge queues a user-role recovery nudge for delivery on the next
// model turn (BeforeTurn drains the pending-injection queue into the ADK
// turn nudge). A nudge identical to the last queued one is dropped: a
// deterministic model that re-emits the same malformed output between
// attempts would otherwise stack duplicate recovery instructions, burning
// context without changing what the model sees; one copy of each distinct
// nudge is enough. Before the Eino migration this appended directly to
// a.messages, which the old loop re-read every iteration — the ADK runtime
// reads the conversation state itself, so anything that must reach the model
// mid-run has to ride the turn nudge seam.
func (a *Agent) appendUserNudge(content string) {
	a.msgMu.Lock()
	defer a.msgMu.Unlock()
	if n := len(a.pendingInjects); n > 0 && a.pendingInjects[n-1] == content {
		return
	}
	a.pendingInjects = append(a.pendingInjects, content)
}

// queueUserMessage queues an operator message for delivery on the next model
// turn. Same seam as appendUserNudge; kept separate so the operator channel
// and hook nudges can evolve independently.
func (a *Agent) queueUserMessage(content string) {
	a.msgMu.Lock()
	defer a.msgMu.Unlock()
	a.pendingInjects = append(a.pendingInjects, content)
}

// drainPendingInjects returns all queued injections joined as one turn-nudge
// block and empties the queue. Callers that are about to stop the loop must
// NOT drain — queued content has to survive for the next turn.
func (a *Agent) drainPendingInjects() string {
	a.msgMu.Lock()
	defer a.msgMu.Unlock()
	if len(a.pendingInjects) == 0 {
		return ""
	}
	joined := strings.Join(a.pendingInjects, "\n\n")
	a.pendingInjects = nil
	return joined
}

// delegationEnabled reports whether ANY specialist delegation is possible
// this scan: auto-delegation not disabled by configuration, the spawn tool
// available, a lane-using scan type, and at least one lane not
// operator-disabled. False means intentional single-agent mode: no waves, no
// decomposition nudges, no reminders — and DelegationAttempted must never be
// cfgDelegationUnlocked reports whether XALGORIX_ENABLE_CTF_DELEGATE unlocked
// specialist delegation for CTF/benchmark missions (O1.2,
// CORE_LOOP_OPTIMIZATION.md). Default off: every single-agent semantic stays
// byte-identical; DisableAutoDelegate still overrides the unlock.
func (a *Agent) cfgDelegationUnlocked() bool {
	return a.cfg != nil && a.cfg.EnableCTFDelegate
}

// ctfDelegationBlocked reports whether this CTF/benchmark mission is barred
// from specialist delegation: the default posture (depth-first single-flag
// doctrine runs one agent) unless the operator unlocked delegation. All other
// CTF semantics (ProfessionalAssessment=false, advisory-commitment off,
// single-flag stop-loss doctrine) are untouched by the unlock.
func (a *Agent) ctfDelegationBlocked() bool {
	return a.ctfMission && !a.cfgDelegationUnlocked()
}

// set, because consumers read it as "a specialist wave ran".
func (a *Agent) delegationEnabled() bool {
	if a == nil || a.registry == nil {
		return false
	}
	// NOTE: XALGORIX_DISABLE_AUTO_DELEGATE suppresses the automatic specialist
	// WAVE only (checked in maybeAutoDelegate). It must NOT disable manual
	// spawn_agent: operators use it precisely to keep the engine from
	// generating lanes while still delegating per-unit work themselves.
	if a.ctfDelegationBlocked() {
		return false
	}
	if a.state != nil && (a.state.DiscoveryMode || a.state.ReconOnlyMode) {
		return false
	}
	if _, ok := a.registry.Get("spawn_agent"); !ok {
		return false
	}
	for _, profile := range a.mergedSpecialistProfiles() {
		if !a.specialistDisabled(profile.Role) {
			return true
		}
	}
	return false
}

// specialistDisabled reports whether the named specialist lane is turned off
// via XALGORIX_DISABLED_SPECIALISTS (comma-separated lane names). Names match
// the profile Role exactly and are matched case-insensitively.
func (a *Agent) specialistDisabled(role string) bool {
	if a == nil || a.cfg == nil || len(a.cfg.DisabledSpecialists) == 0 {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(role))
	for _, d := range a.cfg.DisabledSpecialists {
		if d == want {
			return true
		}
	}
	return false
}

// PlanDisposition returns the root plan's final task dispositions for
// reporting: total tasks, completed, skipped, and unfinished (pending+active).
// Completed-vs-skipped is the honest coverage distinction (skips are not
// executed coverage), and unfinished work at terminal time is the visible
// signal of an incomplete assessment.
func (a *Agent) PlanDisposition() (total, completed, skipped, unfinished int) {
	if a != nil {
		if snapshot := a.publishedAssessment.Load(); snapshot != nil {
			return snapshot.Total, snapshot.Completed, snapshot.Skipped, snapshot.Unfinished
		}
	}
	if a == nil || a.state == nil || a.state.Plan == nil {
		return 0, 0, 0, 0
	}
	pending, active, completed, skipped := a.state.Plan.Counts()
	return len(a.state.Plan.Tasks), completed, skipped, pending + active
}

// dedupeBatchCalls drops byte-identical (tool, args) repeats from a single
// model response. The model cannot have seen the first call's result when it
// emits the duplicate, so re-executing it is pure waste — and the stuck-call
// tracker previously counted those in-batch duplicates toward the loop-limit
// kill. Cross-turn repetition (the model saw the result and re-issued anyway)
// is unaffected and still guarded.
func dedupeBatchCalls(calls []llm.ToolCall) []llm.ToolCall {
	if len(calls) < 2 {
		return calls
	}
	seen := make(map[string]bool, len(calls))
	kept := calls[:0]
	for _, tc := range calls {
		key := tc.Name + "\x00" + hashToolArgs(tc.Name, tc.Args)
		if seen[key] {
			continue
		}
		seen[key] = true
		kept = append(kept, tc)
	}
	return kept
}

// PlanWorkedPhases returns the phases of plan tasks that were COMPLETED.
// The plan's own phase attribution is the richest per-phase work signal:
// AutoPlan and model-authored tasks both carry explicit phase numbers, so a
// completed auth-session task marks phase 5 worked, a completed business-
// logic task marks phase 12, and so on - classes the command heuristics and
// phase-mention parsing cannot see. Only completed tasks count; skips are
// dispositions, not work.
func (a *Agent) PlanWorkedPhases() []int {
	if a != nil {
		if snapshot := a.publishedAssessment.Load(); snapshot != nil {
			return append([]int(nil), snapshot.WorkedPhases...)
		}
	}
	if a == nil || a.state == nil || a.state.Plan == nil {
		return nil
	}
	var phases []int
	seen := map[int]bool{}
	for _, t := range a.state.Plan.Tasks {
		if t.Status != TaskCompleted || t.Phase <= 0 || seen[t.Phase] {
			continue
		}
		seen[t.Phase] = true
		phases = append(phases, t.Phase)
	}
	return phases
}

// SetSingleHostScope marks a single-target scan: the configured host is the
// whole scope, so subdomains are not enumerated. Call before Run().
func (a *Agent) SetSingleHostScope(enabled bool) { a.singleHostScope = enabled }

// SetDiscoveryMode configures the agent to skip minimum iteration checks on finish.
// Used for Phase 1 subdomain enumeration where we want the agent to exit immediately.
func (a *Agent) SetDiscoveryMode(enabled bool) {
	a.discoveryMode = enabled
	a.refreshPassiveReconGuard()
}

// CompletionOutcome returns the engine's honest terminal completion state
// ("" while running; completed / completed_with_blocked_work / incomplete
// once the finish gate releases the scan). The web layer consults this so a
// finish-gate-exhausted scan is never recorded as full coverage.
func (a *Agent) CompletionOutcome() string {
	if a != nil {
		if snapshot := a.publishedAssessment.Load(); snapshot != nil {
			return snapshot.Completion
		}
	}
	if a == nil || a.state == nil {
		return ""
	}
	return a.state.CompletionStatus
}

// CompletionStopReason returns the typed reason an "incomplete" assessment
// carries to the scan record (finish_gate_exhausted, untested_at_finish), or
// "" when none applies.
func (a *Agent) CompletionStopReason() string {
	if a != nil {
		if snapshot := a.publishedAssessment.Load(); snapshot != nil {
			return snapshot.StopReason
		}
	}
	if a == nil || a.state == nil {
		return ""
	}
	return a.state.CompletionStopReason
}

// SetPhaseRestrictions configures the selected methodology phases for policy hooks.
// An empty slice means the full methodology is allowed.
func (a *Agent) SetPhaseRestrictions(phases []int) {
	a.allowedPhases = append([]int(nil), phases...)
	a.refreshPassiveReconGuard()
}

const (
	activityModeActive  = "active"
	activityModePassive = "passive"

	passiveReconMinLookups      = 2
	passiveReconMinSourceKinds  = 2
	passiveReconFallbackLookups = 3
)

// SetActivityPolicy configures passive/active access controls for recon and testing.
func (a *Agent) SetActivityPolicy(reconMode, scanIntensity string, targets []string) {
	a.reconMode = normalizeActivityMode(reconMode)
	a.scanIntensity = normalizeActivityMode(scanIntensity)
	if a.scanIntensity == activityModePassive {
		a.reconMode = activityModePassive
	}
	a.activityHosts = normalizeActivityHosts(targets)
	a.refreshPassiveReconGuard()
}

// canonicalizeAssistantTurn renders an assistant turn in the canonical tool-
// call format: the model's clean prose (tool XML already stripped) followed by
// one well-formed <function=NAME>…</function> block per parsed/recovered call.
// Persisting this instead of the model's raw (possibly malformed) output keeps
// the conversation self-consistent, so the model — which few-shot-mimics its
// own prior turns — stays on-format instead of drifting into dropped-tag
// malformations that eventually become unrecoverable and stall the scan.
func canonicalizeAssistantTurn(cleanText string, toolCalls []llm.ToolCall) string {
	var b strings.Builder
	// Strip any malformed tool-call residue (orphaned <parameter> blocks, a
	// bare `name>` line, stray </function>) so the rebuilt turn is clean prose
	// only — otherwise the very malformation we're canonicalizing away leaks
	// back in via the prose.
	if prose := llm.StripToolResidue(cleanText); prose != "" {
		b.WriteString(prose)
		b.WriteString("\n")
	}
	for _, tc := range toolCalls {
		b.WriteString(llm.FormatToolCall(tc.Name, tc.Args))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// authoritativeFinishSummary keeps the deterministic vulnerability store as
// the source of truth. Agent-written summaries are useful narrative, but the
// model may miscount duplicated IDs (the production pentest-ground run claimed
// 24 while the store held 23). Prefixing the exact persisted count makes that
// mismatch visible and prevents downstream UIs from treating model arithmetic
// as authoritative.
func authoritativeFinishSummary(summary string, findingCount int) string {
	prefix := fmt.Sprintf("Authoritative verified finding count: %d", findingCount)
	if strings.TrimSpace(summary) == "" {
		return prefix
	}
	return prefix + "\n\nAgent narrative:\n" + strings.TrimSpace(summary)
}

// stripThink removes <think>...</think> blocks from the response.
func stripThink(s string) string {
	return thinkRegex.ReplaceAllString(s, "")
}

// touchActivity updates the last activity timestamp (thread-safe).
func (a *Agent) touchActivity() {
	a.activityMu.Lock()
	a.lastActivity = time.Now()
	a.activityMu.Unlock()
}

// sinceActivity returns how long since last activity (thread-safe).
func (a *Agent) sinceActivity() time.Duration {
	a.activityMu.Lock()
	defer a.activityMu.Unlock()
	return time.Since(a.lastActivity)
}

// startWatchdog starts a background monitor that enforces:
// 1. Per-process timeout: kills individual commands running > 30 minutes
// 2. Scan-level timeout: force-stops entire scan after scanMaxDuration (0 = infinite)
// 3. Idle detection: kills agent stuck with no processes and no LLM response (0 = disabled)
func (a *Agent) startWatchdog() func() {
	stopChan := make(chan struct{})

	const (
		processMaxDuration = 30 * time.Minute // kill single process after this
		scanMaxDuration    = 0                // 0 = infinite (no scan-level timeout — needed for 300+ domain scans)
		idleKillThreshold  = 0 * time.Minute  // 0 = disabled (stuck-loop detection handles per-target stalls)
	)

	// Watchdog is a long-running background monitor; wrap with safe.Go
	// so a panic inside the tick loop (e.g. from terminal/scancontext
	// state mutation) is logged and counted instead of crashing the
	// process.
	safe.Go("agent.watchdog", a.scanCtx.ID, func() {
		// Check every 30 seconds
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		var lastStatusMsg string
		var lastStatusTime time.Time

		for {
			select {
			case <-stopChan:
				return
			case <-ticker.C:
				if a.stopped.Load() {
					return
				}

				// ── Check 1: Scan-level timeout (0 = infinite/disabled) ──
				if scanMaxDuration > 0 {
					scanDuration := time.Since(a.scanStart)
					if scanDuration > time.Duration(scanMaxDuration)*time.Hour {
						a.emit(Event{Type: "error", Content: fmt.Sprintf("⛔ Scan timeout: scan has been running for %s (max %s). Force stopping.", scanDuration.Round(time.Minute), time.Duration(scanMaxDuration)*time.Hour)})
						a.stopped.Store(true)
						if a.cancel != nil {
							a.cancel()
						}
						activeCmd, _ := a.scanCtx.Terminal.GetActiveCommand()
						safe.IncWatchdogKill()
						log.Printf("[watchdog] WARN kill command=%q duration=%s scan=%s reason=scan_max_duration",
							activeCmd, scanDuration.Round(time.Second), a.scanCtx.ID)
						a.scanCtx.Terminal.KillAll()
						return
					}
				}

				// ── Reap dead processes that weren't properly untracked ──
				reaped := a.scanCtx.Terminal.ReapDead()
				if reaped > 0 {
					a.emit(Event{Type: "message", Content: a.opf("🧹 Watchdog: reaped %d dead process(es) from tracker", "🧹 看门狗：已清理 %d 个失活进程", reaped)})
				}

				activeProcs := a.scanCtx.Terminal.ActiveProcessCount()
				activeCmd, cmdDuration := a.scanCtx.Terminal.GetActiveCommand()

				// ── Check 2: Per-process timeout ──
				// If a single process has been running too long, kill it
				if activeProcs > 0 && cmdDuration > processMaxDuration {
					a.emit(Event{Type: "error", Content: fmt.Sprintf("⚠️ Watchdog: Process running for %s (limit: %s), killing it: %s", cmdDuration.Round(time.Minute), processMaxDuration, activeCmd)})
					safe.IncWatchdogKill()
					log.Printf("[watchdog] WARN kill command=%q duration=%s scan=%s reason=process_max_duration",
						activeCmd, cmdDuration.Round(time.Second), a.scanCtx.ID)
					a.scanCtx.Terminal.KillAll()
					a.touchActivity() // reset idle timer since we just intervened
					continue
				}

				// ── If processes are actually running and within limits, update activity ──
				if activeProcs > 0 {
					a.touchActivity()

					// Emit status about what's running (every 5 minutes, deduplicated)
					if cmdDuration > 5*time.Minute && time.Since(lastStatusTime) > 5*time.Minute {
						statusMsg := fmt.Sprintf("⏳ Active: %d process(es)", activeProcs)
						if activeCmd != "" {
							cmdPreview := activeCmd
							if len(cmdPreview) > 100 {
								cmdPreview = cmdPreview[:100] + "..."
							}
							statusMsg += fmt.Sprintf(" | Running: %s (%s)", cmdPreview, cmdDuration.Round(time.Minute))
						}
						if statusMsg != lastStatusMsg {
							a.emit(Event{Type: "message", Content: statusMsg})
							lastStatusMsg = statusMsg
							lastStatusTime = time.Now()
						}
					}
					continue
				}

				// ── Check 3: Idle detection (idleKillThreshold = 0 means disabled) ──
				if idleKillThreshold > 0 {
					idleTime := a.sinceActivity()
					if idleTime > 5*time.Minute && idleTime <= 10*time.Minute {
						a.emit(Event{Type: "message", Content: a.opf("⚠️ Watchdog: No activity for %s. No active processes.", "⚠️ 看门狗：%s 无活动，当前无活跃进程。", idleTime.Round(time.Second))})
					}

					if idleTime > 10*time.Minute && idleTime <= idleKillThreshold {
						a.emit(Event{Type: "message", Content: a.opf("⚠️ Watchdog: Idle for %s. Will force-stop at %s.", "⚠️ 看门狗：已空闲 %s，将于 %s 强制停止。", idleTime.Round(time.Second), idleKillThreshold)})
					}

					if idleTime > idleKillThreshold {
						a.emit(Event{Type: "error", Content: fmt.Sprintf("⚠️ Watchdog: Agent truly stuck for %s (no active processes, no LLM response). Force stopping.", idleTime.Round(time.Second))})
						a.stopped.Store(true)
						if a.cancel != nil {
							a.cancel()
						}
						idleCmd, _ := a.scanCtx.Terminal.GetActiveCommand()
						safe.IncWatchdogKill()
						log.Printf("[watchdog] WARN kill command=%q duration=%s scan=%s reason=idle_timeout",
							idleCmd, idleTime.Round(time.Second), a.scanCtx.ID)
						a.scanCtx.Terminal.KillAll()
						return
					}
				}
			}
		}
	})

	return func() {
		close(stopChan)
	}
}

func (a *Agent) Run(targets []string, instruction string) {
	// Stop-before-Run is a valid lifecycle outcome when exact cancellation wins
	// the session admission race. Do not clone source, initialize browsers, or
	// touch the target after that stop has become authoritative.
	if a.stopped.Load() {
		return
	}
	if _, err := a.RestoreExecutionCheckpoint(); err != nil {
		a.stopForHook(HookResult{StopReason: "checkpoint_restore_failed", EmitMessage: a.op("Scan stopped: execution checkpoint could not be restored safely; saved findings are preserved.", "扫描已停止：执行检查点无法安全恢复；已保存的发现不受影响。")})
		return
	}
	if terminal := a.terminalOutcome.Load(); terminal != nil {
		a.emit(Event{Type: "finished", Content: terminal.Content, Aborted: terminal.Aborted, AbortReason: terminal.Reason, TotalTokens: a.syncBudgetTokens()})
		return
	}
	defer func() {
		if err := a.saveExecutionCheckpoint(); err != nil {
			log.Printf("[checkpoint] final save failed: %v", err)
		}
	}()
	// MCP bridge: connect + register BEFORE the prompt schema snapshot so a
	// bridged tool is indistinguishable from any registered tool. Bounded and
	// best-effort: a failed server skips, never blocks the scan.
	a.setupMcpTools()
	if a.mcpManager != nil {
		defer a.mcpManager.Close()
	}
	a.targets = targets // remember the scan targets so probe_hypothesis can resolve a base URL for a bare path
	if a.scanCtx != nil && a.delegatedAgentID == "" {
		a.scanCtx.SetTargets(targets)
	}
	a.ctfMission = isExplicitCTFMission(instruction)
	a.scanStart = time.Now()
	// Native tool calling is the primary channel. Endpoints that cannot carry
	// the tools array use the ChatModel adapter's XML fallback; the prompt must
	// describe the same channel the adapter will actually send.
	a.nativeToolCalling = a.client != nil && a.client.NativeToolCallingSupported()
	if a.scanBudget != nil {
		a.scanBudget.start()
		// Register this agent as a live sharer for the remainder of Run. The
		// shared cap is fair-shared only while two or more agents are live;
		// single-agent paths keep the exact configured cap.
		a.scanBudget.enter()
		defer a.scanBudget.leave()
	}
	stopSemanticWatchdog := a.startSemanticWatchdog()
	defer stopSemanticWatchdog()
	if deadline, ok := a.scanDeadline(); ok {
		ctx, cancel := context.WithDeadline(a.ctx, deadline)
		defer cancel()
		a.ctx = ctx
		a.client.SetContext(ctx)
		stopCleanup := context.AfterFunc(ctx, func() {
			if ctx.Err() == context.DeadlineExceeded && a.scanCtx != nil && a.scanCtx.Cancel != nil {
				a.scanCtx.Cancel()
			}
		})
		defer stopCleanup()
		if !time.Now().Before(deadline) {
			a.stopForHook(HookResult{StopReason: "resource_budget", EmitMessage: a.op("Scan stopped: original duration budget is exhausted; saved findings are preserved.", "扫描已停止：原定时长预算耗尽；已保存的发现不受影响。")})
			return
		}
	}
	// Wire per-scan auth + whitebox source here (in the scan goroutine) so a
	// slow git clone never blocks scan creation or the HTTP handler.
	a.prepareScanEnvironment()
	ratePolicy := EffectiveRequestRatePolicy(a.cfg, instruction)
	if a.scanCtx != nil {
		a.scanCtx.SetRequestRatePolicy(ratePolicy)
	}

	// Start watchdog
	stopWatchdog := a.startWatchdog()
	defer stopWatchdog()

	systemPrompt := a.buildSystemPrompt(targets, instruction, ratePolicy)
	a.messages = []llm.Message{
		{Role: "system", Content: systemPrompt},
	}

	userMsg := a.buildInitialUserMessage(targets, instruction)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: userMsg})

	// Inject the seeded attack surface (from OpenAPI/HAR/Postman context) as a
	// high-priority follow-up so the agent tests the target's REAL endpoints
	// instead of relying solely on crawl-based discovery.
	if a.scanContextBriefing != "" {
		a.messages = append(a.messages, llm.Message{Role: "user", Content: a.scanContextBriefing})
	}
	// Hot-resume transcript sits before the briefing: the replay is history,
	// the briefing is the final "continue from here" instruction.
	if a.replayTranscript != "" {
		a.messages = append(a.messages, llm.Message{Role: "user", Content: a.replayTranscript})
	}
	if a.resumeBriefing != "" {
		a.messages = append(a.messages, llm.Message{Role: "user", Content: a.resumeBriefing})
	}

	// Initialize scan state for hooks (replaces 17+ local tracking variables)
	if a.state == nil {
		a.state = NewScanState()
	}
	if a.forceContinuation.Load() && a.state != nil {
		a.state.ForceContinuation = true
	}

	a.state.Iteration = a.initialIter
	// A scan resumed after a server restart must not repeat the
	// full-detail specialist-wave defer wall: the persisted record already
	// displayed it.
	if a.deferNoteSuppressed {
		a.state.DelegationDeferDetailEmitted = true
	}
	if a.cfg != nil {
		// Thread the configurable no-tool abort threshold (0 = never give up).
		a.state.NoToolAbortLimit = a.cfg.NoToolAbortAt
		a.state.NoToolAbortConfigured = true
		a.state.MaxFinishRejections = a.cfg.MaxFinishRejections
		a.state.MinIterations = a.cfg.MinIterations
	}
	a.state.ScanContextID = a.scanCtx.ID
	a.state.DelegatedAgent = a.delegatedAgentID != ""
	a.state.DelegatedAgentID = a.delegatedAgentID
	a.state.LaneScoped = a.laneScoped
	a.state.AssignedClasses = append([]string(nil), a.assignedClasses...)
	// Delegation vocabulary (builtin + operator-authored profiles) is frozen
	// at Run so hooks and the engine spawn from the same set.
	a.state.SpecialistProfiles = a.mergedSpecialistProfiles()
	a.state.BenchmarkIsolated = a.benchmarkIsolated
	a.state.CTFMission = a.ctfMission
	a.state.ProfessionalAssessment = !a.ctfMission
	a.state.AuthContextKnown = true
	a.state.AuthContextAvailable = len(httpclient.ParseAuthHeaders(a.targetAuth)) > 0
	if a.scanCtx != nil && len(httpclient.SessionAuthForContext(a.scanCtx.ID)) > 0 {
		a.state.AuthContextAvailable = true
	}
	a.state.DiscoveryMode = a.discoveryMode
	a.state.SingleHostScope = a.singleHostScope && !a.discoveryMode
	a.state.AllowedPhases = append([]int(nil), a.allowedPhases...)
	// Derive the explicit depth mode ONCE from the phase selection. Empty
	// AllowedPhases = the full methodology is allowed = deep requirements
	// apply; it must never be misread as "no phases selected = shallow".
	if len(a.allowedPhases) == 0 || len(a.allowedPhases) >= 20 {
		a.state.ScanDepth = "deep"
	} else {
		a.state.ScanDepth = "standard"
	}
	a.state.ReconOnlyMode = isReconReportOnlyPhaseSelection(a.allowedPhases)
	if a.state.ReconOnlyMode {
		a.state.DiscoveryMode = true
	}
	// Configured scope drives applicability: a single explicit host does not
	// owe subdomain enumeration; a bare domain or wildcard does.
	a.state.ScanTargets = append([]string(nil), targets...)
	// Deep-mode recon obligations are a function of depth + intensity ONLY —
	// never of specialist availability. Single-agent scans owe the same
	// deep reconnaissance as wave-enabled scans.
	a.state.DeepReconRequired = a.isDeepMode() && a.scanIntensity == activityModeActive
	// Epoch-based coverage convergence is derived once from config so the
	// coverage predicates read a single ScanState flag (never the global cfg).
	a.state.EpochCoverage = a.cfg != nil && a.cfg.EpochCoverage
	a.state.ConvergeReconcile = a.cfg != nil && a.cfg.ConvergeReconcile
	a.state.EndpointHygiene = a.cfg != nil && a.cfg.EndpointHygiene
	a.state.ConvergeFinish = a.cfg != nil && a.cfg.ConvergeFinish
	// Single-agent mode is an intentional configuration, not a failure
	// state; derived once so every delegation path reads one flag.
	a.state.DelegationEnabled = a.delegationEnabled()
	a.resetPassiveReconGuardForRun()
	if a.executionRestored {
		a.resumePlanContext()
	}
	if !a.checkpointOrStop() {
		return
	}
	if a.ownsAgentGraph {
		a.agentGraph.ResumeCheckpointedWork()
	}

	// Helper to get current token count
	tokenCount := func() int {
		return a.syncBudgetTokens()
	}

	// The Eino ADK is the only scan runtime. Construction or execution
	// failures are terminal and visible; there is no fallback loop.
	a.runLoop(systemPrompt, tokenCount)
}

// DroppedNarration reports narration events dropped because the event channel
// was full or closed. Tool/claim events never use this best-effort path.
func (a *Agent) DroppedNarration() int64 { return a.droppedTelemetry.Load() }

// UncooperativeTimeouts reports hard timeouts on tools without ExecuteContext.
func (a *Agent) UncooperativeTimeouts() int64 { return a.uncooperativeTimeouts.Load() }

// TelemetryStats renders the loss/fallback counters appended to the terminal
// telemetry block. An empty string means nothing was lost.
func (a *Agent) TelemetryStats() string {
	dropped := a.DroppedNarration()
	uncooperative := a.UncooperativeTimeouts()
	if dropped == 0 && uncooperative == 0 {
		return ""
	}
	return fmt.Sprintf("telemetry: dropped_narration=%d uncooperative_timeouts=%d\n", dropped, uncooperative)
}

// completeWithFinish emits the terminal finish event with the honest
// completion assessment and telemetry for the single runtime.
func (a *Agent) completeWithFinish(output string, tokenCount func() int) {
	content := output
	if !a.state.DelegatedAgent {
		count := 0
		if a.scanCtx != nil {
			count = len(reporting.GetVulnerabilitiesForContext(a.scanCtx.ID))
		}
		content = authoritativeFinishSummary(content, count)
		// ── Honest completion assessment ──
		// The gates release a finish after the bounded rejection ceiling; that
		// must never read as "full assessment completed". Compute the explicit
		// terminal state and make it visible in the final summary.
		status, reasons := settleCompletion(a.state)
		if status != CompletionStatusCompleted {
			content += "\n\n" + formatIncompleteSummary(status, reasons)
		}
		if summary := scanTelemetrySummary(a.state, status, reasons); summary != "" {
			if stats := a.TelemetryStats(); stats != "" {
				summary += stats
			}
			a.emit(Event{Type: "message", Content: summary, TotalTokens: tokenCount()})
		}
	}
	a.scanFinished.Store(true)
	// The ADK graph runs ahead of its event consumer: without this cancel it
	// would issue one more model call after a completed finish and discard the
	// result — a paid call the scan can never use.
	if cancel := a.runCancel.Load(); cancel != nil {
		(*cancel)()
	}
	a.emit(Event{Type: "finished", Content: content, TotalTokens: tokenCount()})
}

// Stop signals the agent to stop and kills all running processes.
func (a *Agent) Stop() {
	a.stopped.Store(true)

	if a.cancel != nil {
		a.cancel()
	}
	if a.ownsAgentGraph && a.agentGraph != nil {
		a.agentGraph.Stop()
	}

	// NOTE: Do NOT call terminal.KillAllProcesses() or browser.CleanupBrowser()
	// here — those are GLOBAL operations that kill processes across ALL instances.
	// Per-instance cleanup is handled by sctx.Close() in sess.cleanup().
	// The handleStop handler calls terminal.KillAllProcesses() directly for
	// user-initiated "Stop All" operations.
}

// WaitForDelegatedAgents waits for this root's child runners to observe
// cancellation and unwind. Cleanup uses a bounded wait before deleting shared
// scan-context stores, preventing a late child from writing after persistence.
func (a *Agent) WaitForDelegatedAgents(timeout time.Duration) bool {
	if !a.ownsAgentGraph || a.agentGraph == nil {
		return true
	}
	return a.agentGraph.WaitStopped(timeout)
}

// SendMessage injects an operator message into the running scan so the agent
// sees it on its next model turn. It rides the pending-injection queue that
// BeforeTurn drains into the ADK turn nudge — the ADK owns the conversation
// state, so appending to a.messages (the Run-start seed) would be a dead
// letter that no model call ever reads. Routing through the queue (instead
// of issuing a separate LLM call) also avoids concurrent Chat() calls that
// would corrupt history.
//
// This is fire-and-forget: the returned string is an acknowledgement
// that the message was queued, NOT the agent's reply. The agent's
// response to the message surfaces later as normal scan events on the
// live feed. The error is non-nil only when the agent is already stopped.
func (a *Agent) SendMessage(message string) (string, error) {
	if a.stopped.Load() {
		return "", fmt.Errorf("agent is not running")
	}

	a.queueUserMessage("[USER MESSAGE DURING SCAN]: " + message)

	// Emit as a visible event so it appears in the feed
	a.emit(Event{Type: "message", Content: a.opf("📨 User message received: %s", "📨 已收到用户消息：%s", message)})

	return "Message received and will be processed on the next iteration.", nil
}

func (a *Agent) emit(evt Event) {
	if evt.Type == "finished" && !evt.Resumable && a.checkpointLoaded {
		a.terminalOutcome.Store(&terminalCheckpoint{evt.Content, evt.Aborted, evt.AbortReason})
		if err := a.saveExecutionCheckpoint(); err != nil {
			log.Printf("[checkpoint] terminal save failed: %v", err)
		}
	}
	evt.AgentID = a.ID
	evt.Timestamp = time.Now()
	// Redact operator-supplied auth secrets from telemetry so credentials
	// don't leak into the live event stream, scan logs, or PDF reports.
	if len(a.secretValues) > 0 {
		evt.Content = a.redactSecrets(evt.Content)
		if evt.ToolResult.Output != "" {
			evt.ToolResult.Output = a.redactSecrets(evt.ToolResult.Output)
		}
		if evt.ToolResult.Error != "" {
			evt.ToolResult.Error = a.redactSecrets(evt.ToolResult.Error)
		}
		// Tool ARGUMENTS also carry secrets: the agent is instructed to pass
		// auth headers explicitly to curl/http_request (e.g. the second-account
		// IDOR/BOLA flow), so credentials land in ToolArgs and would otherwise
		// be broadcast/persisted in the clear. Redact a COPY so the agent's own
		// working args (used to actually run the tool) are left intact.
		if len(evt.ToolArgs) > 0 {
			red := make(map[string]string, len(evt.ToolArgs))
			for k, v := range evt.ToolArgs {
				red[k] = a.redactSecrets(v)
			}
			evt.ToolArgs = red
		}
	}
	if a.events != nil {
		// Critical events (finished, error) must never be dropped — use blocking send with timeout.
		// tool_call/tool_result are the durable domain-audit substrate until
		// dedicated audit records exist: they are what carries ledger claims,
		// hypothesis status transitions and report outcomes into the per-scan
		// events.jsonl journal, and a silently dropped claim event makes
		// "who claimed what, when" unreconstructable. They get the same
		// blocking send. Only narration/telemetry stays best-effort.
		if evt.Type == "finished" || evt.Type == "error" || evt.Type == "tool_call" || evt.Type == "tool_result" {
			if !safeSend(a.events, evt, 10*time.Second) {
				log.Printf("⚠️ CRITICAL: Failed sending %s event (channel closed or full for 10s)", evt.Type)
			}
		} else {
			// Non-critical narration stays best-effort, but a full channel is
			// counted and logged so telemetry loss is observable rather than
			// silent. Durable tool/claim events use the blocking branch above.
			if !safeSend(a.events, evt, 0) {
				n := a.droppedTelemetry.Add(1)
				if n == 1 || n%100 == 0 {
					log.Printf("[agent] dropped %d narration telemetry event(s) (channel full/closed)", n)
				}
			}
		}
	}
}

// safeSend sends an event to a channel without panicking if the channel is closed.
// If timeout > 0, it blocks up to that duration. If timeout == 0, it's non-blocking.
// Returns true if sent successfully, false if dropped (closed, full, or timed out).
func safeSend(ch chan Event, evt Event, timeout time.Duration) (sent bool) {
	defer func() {
		if r := recover(); r != nil {
			// "send on closed channel" — channel was closed by parent session
			sent = false
		}
	}()
	if timeout > 0 {
		select {
		case ch <- evt:
			return true
		case <-time.After(timeout):
			return false
		}
	}
	select {
	case ch <- evt:
		return true
	default:
		return false
	}
}

// redactSecrets replaces operator-supplied auth values in s with a marker.
func (a *Agent) redactSecrets(s string) string {
	if s == "" {
		return s
	}
	for _, secret := range a.secretValues {
		if secret != "" && strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, "***REDACTED***")
		}
	}
	return s
}

// SecretValues returns a snapshot of the operator-supplied auth values
// registered for redaction (targetAuth/targetAuthB headers, whitebox source
// URL credentials, HAR-harvested auth — registered during
// prepareScanEnvironment at Run start). The HITL approval gate binds this
// accessor at scan-session wiring and applies the same by-value redaction to
// approval payloads BEFORE they reach the store, the WS stream, and the API:
// targetAuthB is designed for the model to send the second account's
// credentials manually, and exactly that traffic is what approvals display.
// A copy is returned; callers cannot mutate the registry.
func (a *Agent) SecretValues() []string {
	out := make([]string, len(a.secretValues))
	copy(out, a.secretValues)
	return out
}

// credentialsInURL extracts embedded clone credentials from a git/HTTP URL of
// the form scheme://userinfo@host/path — e.g. a GitHub token in
// https://x-access-token:<TOKEN>@github.com/org/repo.git. It returns the
// full userinfo ("user:pass") and, separately, the password/token component,
// so they can be registered as redaction secrets. Returns nil when the URL
// carries no credentials. Values shorter than 4 chars are skipped to avoid
// redacting trivially-common substrings.
func credentialsInURL(raw string) []string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return nil
	}
	rest := raw[i+3:]
	at := strings.Index(rest, "@")
	if at < 0 {
		return nil
	}
	// The host part may itself contain no '@'; take the userinfo up to the
	// FIRST '@' (credentials cannot contain an unescaped '@').
	userinfo := rest[:at]
	if userinfo == "" {
		return nil
	}
	var out []string
	if len(userinfo) >= 4 {
		out = append(out, userinfo)
	}
	if c := strings.Index(userinfo, ":"); c >= 0 {
		if pw := userinfo[c+1:]; len(pw) >= 4 {
			out = append(out, pw)
		}
	}
	return out
}

// SetTargetAuth sets per-scan authenticated-session credentials, overriding
// the global default. Call before Run(). Empty string clears it.
func (a *Agent) SetTargetAuth(s string) { a.targetAuth = strings.TrimSpace(s) }

// SetTargetAuthSecondary sets a per-scan SECOND account's credentials (for
// horizontal IDOR/BOLA proof). Call before Run(). Empty string clears it.
func (a *Agent) SetTargetAuthSecondary(s string) { a.targetAuthB = strings.TrimSpace(s) }

// CodeScanMode selects a code-first scanning strategy where the primary
// subject is the target's source (a repo/path), not a live URL.
type CodeScanMode int

const (
	// CodeScanNone is the default: black-box or whitebox-augmented testing
	// against a live target URL.
	CodeScanNone CodeScanMode = iota
	// CodeScanReview is source review / SAST with NO live target: the agent
	// reads the code, traces source→sink→reachable-route, and reports
	// source-verified findings. Findings are clearly labeled as statically
	// verified (not runtime-exploited), since there is no running target.
	CodeScanReview
	// CodeScanProvision builds and runs the target's source locally (in the
	// agent's sandbox, on a loopback port the orchestrator allowlists), then
	// pentests the running instance for exploit-verified findings.
	CodeScanProvision
)

// SetCodeScanMode sets the code-first scan strategy. Call before Run().
func (a *Agent) SetCodeScanMode(m CodeScanMode) { a.codeScanMode = m }

// SetSourceRepo sets the per-scan whitebox source (git URL or local path),
// overriding the global default. Call before Run().
func (a *Agent) SetSourceRepo(s string) { a.sourceRepo = strings.TrimSpace(s) }

// SetScanContext sets the per-scan context artifact path (OpenAPI/HAR/Postman
// file or directory) used to seed the attack surface. Call before Run().
func (a *Agent) SetScanContext(s string) {
	a.SetScanContexts([]string{s})
}

// SetScanContexts sets every context artifact path for this scan. Task-level
// context_paths and an explicit request scan_context are additive; each path
// may be a file or a directory of artifacts. Call before Run().
func (a *Agent) SetScanContexts(paths []string) {
	cleaned := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		cleaned = append(cleaned, path)
	}
	a.scanContextPaths = cleaned
	a.scanContext = ""
	if len(cleaned) > 0 {
		a.scanContext = cleaned[0]
	}
}

// SetInitialIteration sets the starting iteration index for resumed runs. Call before Run().
func (a *Agent) SetInitialIteration(iter int) {
	if iter > 0 {
		a.initialIter = iter
		if a.scanBudget != nil && a.scanBudget.iterationCount() < iter {
			a.scanBudget.iterations.Store(int64(iter))
		}
	}
}

// SetResumeBriefing sets a briefing summarizing prior state when resuming a scan. Call before Run().
func (a *Agent) SetResumeBriefing(s string) {
	a.resumeBriefing = strings.TrimSpace(s)
}

// SetConversationReplay seeds a hot-resumed session with a bounded projection
// of the previous session's conversation (hot-resume ladder L2; the fallback
// is the L3 resume briefing alone). Call before Run().
func (a *Agent) SetConversationReplay(s string) {
	a.replayTranscript = strings.TrimSpace(s)
}

// SetForceContinuation marks a 1:1 re-open of a finished run (REDESIGN D11):
// the restored execution checkpoint says "plan complete", but the operator
// explicitly re-dispatched, so the plan-complete auto-finalize must not
// re-fire on the first turn. Call before Run().
func (a *Agent) SetForceContinuation() {
	a.forceContinuation.Store(true)
}

// SetDeferNoteDetailEmitted marks that the full-detail specialist-wave
// defer note was already displayed in a previous process lifetime
// (resume path): the current run emits only compact follow-up notes.
func (a *Agent) SetDeferNoteDetailEmitted() {
	a.deferNoteSuppressed = true
}

func (a *Agent) getIterationDelay() time.Duration {
	if a == nil {
		return 0
	}
	if ms := a.iterationDelayMs.Load(); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	if a.cfg != nil && a.cfg.IterationDelaySec > 0 {
		return time.Duration(a.cfg.IterationDelaySec * float64(time.Second))
	}
	return 0
}

// SetIterationDelay dynamically updates the iteration delay in seconds.
// It updates the agent's internal delay, its configuration, and recursively propagates
// to any active child/subagent runners.
func (a *Agent) SetIterationDelay(delaySec float64) {
	if a == nil {
		return
	}
	if delaySec < 0 {
		delaySec = 0
	}
	a.iterationDelayMs.Store(int64(delaySec * 1000))
	if a.cfg != nil {
		a.cfg.IterationDelaySec = delaySec
	}
	a.childrenMu.Lock()
	for child := range a.children {
		child.SetIterationDelay(delaySec)
	}
	a.childrenMu.Unlock()
}

func (a *Agent) registerChildAgent(child *Agent) {
	if a == nil || child == nil {
		return
	}
	a.childrenMu.Lock()
	if a.children == nil {
		a.children = make(map[*Agent]struct{})
	}
	a.children[child] = struct{}{}
	a.childrenMu.Unlock()
}

func (a *Agent) unregisterChildAgent(child *Agent) {
	if a == nil || child == nil {
		return
	}
	a.childrenMu.Lock()
	delete(a.children, child)
	a.childrenMu.Unlock()
}

// prepareScanEnvironment wires per-scan authenticated-session credentials and
// whitebox source into the shared scan context. Runs at the start of Run()
// (in the scan's own goroutine, so a slow git clone never blocks scan
// creation / the HTTP handler). Idempotent and safe for sub-agents: the source
// clone is guarded so only the first agent for a context resolves it.
func (a *Agent) prepareScanEnvironment() {
	if a.scanCtx == nil {
		return
	}
	// Authenticated session → applied to http_request for this scan context.
	if headers := httpclient.ParseAuthHeaders(a.targetAuth); len(headers) > 0 {
		httpclient.SetSessionAuth(a.scanCtx.ID, headers)
		for _, v := range headers {
			if len(strings.TrimSpace(v)) >= 4 { // avoid redacting trivially short values
				a.secretValues = append(a.secretValues, v)
			}
		}
	}
	// Second account (B) is NOT auto-applied — the agent uses it manually to
	// prove horizontal access control. Still redact its values from telemetry.
	for _, v := range httpclient.ParseAuthHeaders(a.targetAuthB) {
		if len(strings.TrimSpace(v)) >= 4 {
			a.secretValues = append(a.secretValues, v)
		}
	}
	// Whitebox source (git clone / local path). Resolve once per scan context.
	if a.sourceRepo != "" && codesearch.GetSourceRoot(a.scanCtx.ID) == "" {
		dest := a.scanCtx.ScanDir
		if dest == "" {
			dest = os.TempDir()
		}
		dest = filepath.Join(dest, "source")
		// A private-repo clone URL may embed a token
		// (https://x-access-token:<TOKEN>@github.com/...). Register those
		// credentials as redaction secrets BEFORE any emit/log so the token
		// never leaks into telemetry, the scan log, PDF reports, or git's
		// own "authentication failed for <url>" error output.
		a.secretValues = append(a.secretValues, credentialsInURL(a.sourceRepo)...)
		a.emit(Event{Type: "message", Content: a.opf("📦 Whitebox: resolving source (%s)…", "📦 白盒：正在解析源码（%s）…", a.redactSecrets(a.sourceRepo))})
		if root, err := codesearch.ResolveSource(a.sourceRepo, dest); err != nil {
			a.emit(Event{Type: "error", Content: "Whitebox source unavailable: " + a.redactSecrets(err.Error()) + " — continuing black-box."})
			log.Printf("[agent] whitebox source unavailable (%s): %v", a.redactSecrets(a.sourceRepo), a.redactSecrets(err.Error()))
		} else if root != "" {
			codesearch.SetSourceRoot(a.scanCtx.ID, root)
			a.emit(Event{Type: "message", Content: a.op("📦 Whitebox source ready — use code_search to hunt sinks.", "📦 白盒源码就绪——可用 code_search 查找污点汇。")})
			log.Printf("[agent] whitebox source ready at %s", root)
			// Auto-seed the ledger from source so the source-to-runtime bridge
			// starts populated (mirrors seedLedgerFromSurface for uploaded
			// artifacts): dangerous sinks + reachable routes become schedulable
			// hypotheses from iteration 1, without waiting for the model to call
			// scan_source_sinks / scan_source_routes.
			if s := a.seedLedgerFromSource(); s.SinkHypotheses+s.RouteHypotheses > 0 {
				a.emit(Event{Type: "message", Content: a.opf("🔎 Seeded %d source→sink and %d route hypotheses from source (%d route↔sink correlated) — use read_ledger and probe_hypothesis to confirm reachability.", "🔎 已从源码播种 %d 条 source→sink 与 %d 条路由假设（%d 条路由↔污点汇已关联）——用 read_ledger 与 probe_hypothesis 确认可达性。", s.SinkHypotheses, s.RouteHypotheses, s.Correlated)})
				log.Printf("[agent] source seeding: %d sink, %d route hypotheses (%d correlated)", s.SinkHypotheses, s.RouteHypotheses, s.Correlated)
			}
		}
	}
	// Attack-surface seeding (OpenAPI / HAR / Postman). Parse the operator's
	// context artifacts into a real endpoint surface + any captured auth, so the
	// scan starts informed instead of blindly crawling.
	contextPaths := a.scanContextPaths
	if len(contextPaths) == 0 && a.scanContext != "" {
		contextPaths = []string{a.scanContext}
	}
	if len(contextPaths) > 0 && a.scanContextBriefing == "" {
		if res, err := attacksurface.LoadFromPaths(contextPaths); err != nil {
			a.emit(Event{Type: "error", Content: "Scan context unavailable: " + err.Error() + " — continuing without seeded surface."})
			log.Printf("[agent] scan context unavailable (%s): %v", strings.Join(contextPaths, ", "), err)
		} else if res != nil {
			a.scanContextBriefing = res.Briefing()
			// Harvest auth captured in real requests (HAR/Postman) when the
			// operator didn't already supply explicit target auth.
			if len(res.AuthHeaders) > 0 && len(httpclient.ParseAuthHeaders(a.targetAuth)) == 0 {
				httpclient.SetSessionAuth(a.scanCtx.ID, res.AuthHeaders)
				for _, v := range res.AuthHeaders {
					if len(strings.TrimSpace(v)) >= 4 {
						a.secretValues = append(a.secretValues, v)
					}
				}
			}
			// Turn the parsed surface into schedulable ledger hypotheses so the
			// uploaded context drives authz_matrix and the specialists, not just
			// the text briefing. Idempotent (the ledger dedups), so sub-agents
			// that also parse the context add nothing new.
			seeded := a.seedLedgerFromSurface(res)
			// Retain the typed endpoint metadata (methods, parameters) for
			// the structured applicability layer — the notes briefing stays
			// the prose mirror, but the planner consumes this typed form.
			for _, e := range res.Endpoints {
				host, epath := splitSurfaceEndpoint(e.Path, res.BaseURLs)
				if epath == "" {
					continue
				}
				record := SeededSurfaceEndpoint{
					Path:   epath,
					Method: strings.ToUpper(strings.TrimSpace(e.Method)),
					Source: "context",
				}
				if e.Source != "" {
					record.Source = "context:" + e.Source
				}
				record.Params = append(record.Params, e.Params...)
				if host != "" {
					recordEndpointMethod(a.state, host+epath, record.Method)
				}
				a.state.SeededSurface = append(a.state.SeededSurface, record)
			}
			if a.scanContextBriefing != "" {
				msg := a.opf("🗺️ Attack surface seeded from context (%d endpoints", "🗺️ 已从上下文播种攻击面（%d 个端点", len(res.Endpoints))
				if seeded > 0 {
					msg += fmt.Sprintf(", %d ledger hypotheses", seeded)
				}
				msg += ")."
				a.emit(Event{Type: "message", Content: msg})
			}
		}
	}
}

// authGuidance returns an authenticated-session briefing when the operator
// supplied target credentials, or "" otherwise. Post-auth surface (IDOR/BOLA,
// privilege escalation, business logic) is where the high-value bugs live, so
// the agent must know it is authenticated and must diff authed vs unauthed.
func (a *Agent) authGuidance() string {
	headers := httpclient.ParseAuthHeaders(a.targetAuth)
	if len(headers) == 0 {
		return ""
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var curlHdrs strings.Builder
	for _, n := range names {
		curlHdrs.WriteString(fmt.Sprintf(" -H %q", n+": "+headers[n]))
	}
	return fmt.Sprintf(`
## AUTHENTICATED SESSION (operator-supplied)
You have VALID authenticated credentials for this target. They are applied AUTOMATICALLY to every http_request call (%s), so your requests are authenticated by default.
- For terminal tools (curl/ffuf/sqlmap/etc.) add these headers explicitly, e.g.: curl%s <url>
- HUNT THE POST-AUTH SURFACE — this is where the money is: IDOR/BOLA (swap ids/uuids to reach OTHER users' data), broken function-level auth (BFLA), privilege escalation (low-priv → admin actions), tenant isolation, and multi-step business-logic flaws.
- ALWAYS DIFF authenticated vs unauthenticated: replay the same request with the auth headers REMOVED (pass an empty value for that header in http_request) — if protected data/actions still work unauthenticated, that is a broken-access-control finding.
%s`, strings.Join(names, ", "), curlHdrs.String(), a.secondAccountGuidance())
}

// secondAccountGuidance returns the horizontal-access-control (IDOR/BOLA)
// playbook when a second account is configured, or "" otherwise. Two valid
// sessions let the agent PROVE cross-user access with concrete evidence.
func (a *Agent) secondAccountGuidance() string {
	headers := httpclient.ParseAuthHeaders(a.targetAuthB)
	if len(headers) == 0 {
		return "- If you can obtain a SECOND account, compare horizontal access (user A's token reaching user B's objects).\n"
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var curlHdrs strings.Builder
	for _, n := range names {
		curlHdrs.WriteString(fmt.Sprintf(" -H %q", n+": "+headers[n]))
	}
	return fmt.Sprintf(`
## SECOND ACCOUNT (operator-supplied) — PROVE IDOR/BOLA
You ALSO have a DISTINCT second account (account B). Its headers (%s) are NOT applied automatically — pass them explicitly to prove horizontal access control:
- With account B in curl: curl%s <url>
- METHODOLOGY: (1) as account A, create/enumerate an object and note its id/uuid (e.g. GET /api/orders → id=1001, owned by A); (2) request that SAME object as account B (curl with B's headers, or http_request with B's headers overriding the session); (3) if B can READ or MODIFY A's object, that is CONFIRMED BOLA/IDOR — capture both responses as proof.
- Do this for every object-scoped endpoint from the attack surface. Also test BFLA: can a low-privilege account reach an admin-only function?
- The strongest evidence is B receiving A's private data (or successfully mutating A's resource). Report with verification_method=data_extracted and paste BOTH requests/responses.
`, strings.Join(names, ", "), curlHdrs.String())
}

func truncStr(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// overBudget reports whether any configured per-scan resource cap has been
// reached (MAPTA §2.7/§3.3). All caps default to 0 = unlimited, so this is a
// no-op unless the operator opts in. Returns a human-readable reason.
func (a *Agent) overBudget(toolCallsTotal int) (bool, string) {
	if a.cfg == nil {
		return false, ""
	}
	if a.cfg.MaxDurationSec > 0 {
		elapsed := time.Duration(0)
		started := false
		if a.scanBudget != nil {
			elapsed, started = a.scanBudget.elapsed()
		} else if !a.scanStart.IsZero() {
			elapsed, started = time.Since(a.scanStart), true
		}
		if started && elapsed >= time.Duration(a.cfg.MaxDurationSec)*time.Second {
			return true, fmt.Sprintf("time %s ≥ cap %ds", elapsed.Round(time.Second), a.cfg.MaxDurationSec)
		}
	}
	usedToolCalls := toolCallsTotal
	if a.scanBudget != nil {
		usedToolCalls = a.scanBudget.toolCallCount()
	}
	if a.cfg.MaxToolCalls > 0 && usedToolCalls >= a.cfg.MaxToolCalls {
		return true, fmt.Sprintf("%d tool calls ≥ cap %d", usedToolCalls, a.cfg.MaxToolCalls)
	}
	if a.cfg.MaxTokens > 0 && a.client != nil {
		total := a.syncBudgetTokens()
		if total >= a.cfg.MaxTokens {
			return true, fmt.Sprintf("%d tokens ≥ cap %d", total, a.cfg.MaxTokens)
		}
	}
	return false, ""
}

var httpxFixOnce sync.Once

// fixHttpxConflict detects and removes Python's httpx if it shadows ProjectDiscovery's httpx.
func fixHttpxConflict() {
	httpxFixOnce.Do(func() {
		// Check if httpx exists
		httpxPath, err := exec.LookPath("httpx")
		if err != nil {
			return // httpx not installed at all, will be installed later
		}

		// Check if it's Python's httpx by running --version
		out, err := exec.Command(httpxPath, "--version").CombinedOutput()
		if err != nil {
			return // Can't determine, skip
		}

		output := strings.ToLower(string(out))
		if strings.Contains(output, "python") || strings.Contains(output, "httpx/0.") {
			log.Println("⚠️  Detected Python httpx interfering with ProjectDiscovery httpx — removing it...")

			// Try removing Python httpx
			for _, pip := range []string{"pip3", "pip", "pipx"} {
				if _, err := exec.LookPath(pip); err == nil {
					cmd := exec.Command(pip, "uninstall", "httpx", "-y")
					if out, err := cmd.CombinedOutput(); err != nil {
						log.Printf("Failed to uninstall Python httpx via %s: %s", pip, string(out))
					}
				}
			}

			// Install ProjectDiscovery httpx
			cmd := exec.Command("go", "install", "-v", "github.com/projectdiscovery/httpx/cmd/httpx@latest")
			if out, err := cmd.CombinedOutput(); err != nil {
				log.Printf("Failed to install ProjectDiscovery httpx: %s", string(out))
			} else {
				log.Println("✅ Replaced Python httpx with ProjectDiscovery httpx")
			}
		}
	})
}

func determineAgentType(agentName string, isDelegated bool) string {
	if !isDelegated {
		return scanctx.AgentTypeRoot
	}
	lower := strings.ToLower(agentName)
	switch {
	case strings.Contains(lower, "authz") || strings.Contains(lower, "logic"):
		return scanctx.AgentTypeAuthzLogic
	case strings.Contains(lower, "inject") || strings.Contains(lower, "server"):
		return scanctx.AgentTypeInjectionServer
	case strings.Contains(lower, "client") || strings.Contains(lower, "source") || strings.Contains(lower, "xss"):
		return scanctx.AgentTypeClientSource
	case strings.Contains(lower, "verifier") || strings.Contains(lower, "verify"):
		return scanctx.AgentTypeVerifier
	default:
		return scanctx.AgentTypeOther
	}
}

func calculateMessageMetrics(msgs []llm.Message) (totalBytes, sysBytes, userBytes, asstBytes, toolCount, toolBytes, skillCount, skillBytes int) {
	for _, m := range msgs {
		b := len(m.Content)
		totalBytes += b
		switch m.Role {
		case "system":
			sysBytes += b
		case "assistant":
			asstBytes += b
		case "user":
			userBytes += b
			isTool := strings.Contains(m.Content, "<function_results>") || strings.Contains(m.Content, "Tool '") || strings.Contains(m.Content, "result:\n")
			if isTool {
				toolCount++
				toolBytes += b
				if strings.Contains(m.Content, "read_skill") || strings.Contains(m.Content, "list_skills") || strings.Contains(m.Content, "Skill already loaded") {
					skillCount++
					skillBytes += b
				}
			}
		}
	}
	return
}

func reasoningRequestCategory(state *ScanState) string {
	if state == nil {
		return scanctx.CategoryNormalReasoning
	}
	if state.PendingFailedReportCalls > 0 || state.MalformedToolOutputCount > 0 {
		return scanctx.CategoryMalformedToolRecovery
	}
	if state.NoToolCount > 0 {
		return scanctx.CategoryNoToolRecovery
	}
	if state.FinishRecoveryPending {
		return scanctx.CategoryFinishRejectionRecovery
	}
	return scanctx.CategoryNormalReasoning
}

func (a *Agent) recordTokenAttribution(usage *llm.TokenUsage, msgs []llm.Message, iter int, category string, retryAttempt int) {
	if a == nil || a.scanCtx == nil || a.scanCtx.Tokens == nil {
		return
	}
	totBytes, sysBytes, usrBytes, asstBytes, tCount, tBytes, sCount, sBytes := calculateMessageMetrics(msgs)
	pTokens, outTokens, _ := a.client.GetTokens()

	model := ""
	provider := ""
	if a.cfg != nil {
		model = a.cfg.LLM
		provider = a.cfg.LLMProvider
	}

	rec := scanctx.TokenAttribution{
		ScanID:                  a.scanCtx.ID,
		AgentID:                 a.ID,
		AgentType:               determineAgentType(a.Name, a.state != nil && a.state.DelegatedAgent),
		Iteration:               iter,
		Model:                   model,
		Provider:                provider,
		PromptTokens:            0,
		CompletionTokens:        0,
		TotalTokens:             0,
		CachedInputTokens:       0,
		UncachedInputTokens:     0,
		MessageCount:            len(msgs),
		SerializedMessageBytes:  totBytes,
		SystemMessageBytes:      sysBytes,
		UserMessageBytes:        usrBytes,
		AssistantMessageBytes:   asstBytes,
		ToolResultCount:         tCount,
		ToolResultBytes:         tBytes,
		SkillResultCount:        sCount,
		SkillResultBytes:        sBytes,
		ConversationBufferBytes: totBytes,
		RetryAttempt:            retryAttempt,
		RequestCategory:         category,
		CompactionCount:         a.compactionCount,
		AgentCumulativePrompt:   pTokens,
		AgentCumulativeOutput:   outTokens,
		Timestamp:               time.Now(),
	}
	if usage != nil {
		rec.PromptTokens = usage.PromptTokens
		rec.CompletionTokens = usage.CompletionTokens
		rec.TotalTokens = usage.TotalTokens
		rec.CachedInputTokens = usage.GetCachedTokens()
		rec.CacheReported = usage.HasCachedTokens
		rec.UncachedInputTokens = usage.PromptTokens - rec.CachedInputTokens
		if rec.UncachedInputTokens < 0 {
			rec.UncachedInputTokens = 0
		}
	}
	a.scanCtx.Tokens.Record(rec)
}
