// Package agentsgraph provides scan-scoped multi-agent delegation tools.
package agentsgraph

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

const (
	DefaultMaxConcurrentAgents = 3
	DefaultWaitTimeout         = 30 * time.Second
)

// AgentRunner runs one delegated agent. agentID is the graph ID exposed to the
// coordinator; event forwarders use it to attach partial results to the right
// delegation. Implementations must stop when ctx is canceled.
type AgentRunner func(ctx context.Context, agentID, name string, targets []string, task string) (string, error)

// AuditSink receives best-effort delegation audit records. op is one of the
// domainjournal invocation operations (OpAgentDelegat); nil sink = no audit
// (tests, minimal setups). A sink failure must never break delegation — the
// implementation logs, it does not propagate.
type AuditSink func(op domainjournal.Operation, summary string)

type subAgentState struct {
	ID          string
	Name        string
	Task        string
	Targets     []string
	Status      string // queued, running, completed, failed
	StartedAt   time.Time
	CompletedAt time.Time
	Result      string
	Error       string
	Observed    bool
	Partial     []string
	done        chan struct{}
	doneOnce    sync.Once
}

// Graph owns all delegation state for exactly one root scan. Nothing in this
// type is package-global: concurrent scans therefore cannot overwrite each
// other's runner, consume each other's semaphore slots, or clear each other's
// results during cleanup.
type Graph struct {
	ctx           context.Context
	cancel        context.CancelFunc
	runner        AgentRunner
	maxAgents     int
	maxConcurrent int

	mu           sync.Mutex
	stopped      bool
	counter      uint64
	delegated    int
	activeAgents int
	queue        []*subAgentState
	agents       map[string]*subAgentState
	wg           sync.WaitGroup
	persistPath  string
	persistErr   error
	audit        AuditSink
	// stateSink receives best-effort lifecycle notifications for panel
	// invalidation (see SetStateSink). Never the authority: Snapshot() is.
	stateSink func(StateEvent)
}

// SetAuditSink installs the per-scan invocation audit sink (wired by the
// agent layer to the scan's domain journal). Call once before Register.
func (g *Graph) SetAuditSink(fn AuditSink) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.audit = fn
}

// auditDelegation emits one delegation lifecycle audit record. Fire-and-forget:
// audit gaps are logged by the sink, never fatal to delegation.
func (g *Graph) auditDelegation(op domainjournal.Operation, summary string) {
	g.mu.Lock()
	fn := g.audit
	g.mu.Unlock()
	if fn != nil {
		fn(op, summary)
	}
}

// EffectiveMaxConcurrentAgents returns the configured maximum number of subagents
// executing simultaneously per scan. When XALGORIX_MAX_CONCURRENT_AGENTS is set to 1,
// specialists run serially one at a time. Defaults to DefaultMaxConcurrentAgents (3).
func EffectiveMaxConcurrentAgents() int {
	if raw := strings.TrimSpace(os.Getenv("XALGORIX_MAX_CONCURRENT_AGENTS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > 10 {
				n = 10
			}
			return n
		}
	}
	return DefaultMaxConcurrentAgents
}

// New creates a scan-scoped graph with the production concurrency limit.
func New(parent context.Context, runner AgentRunner) *Graph {
	return NewWithLimit(parent, DefaultMaxConcurrentAgents, runner)
}

// NewWithLimit bounds both concurrent and total delegated agents. A single
// bounded wave keeps specialists complementary and gives the coordinator time
// to integrate their evidence before the root scan deadline.
func NewWithLimit(parent context.Context, maxAgents int, runner AgentRunner) *Graph {
	return NewWithConfig(parent, maxAgents, EffectiveMaxConcurrentAgents(), runner)
}

// NewWithConfig creates a graph with explicit lifetime delegation and concurrency caps.
func NewWithConfig(parent context.Context, maxAgents, maxConcurrent int, runner AgentRunner) *Graph {
	if parent == nil {
		parent = context.Background()
	}
	if maxAgents <= 0 {
		maxAgents = DefaultMaxConcurrentAgents
	}
	if maxConcurrent <= 0 {
		maxConcurrent = EffectiveMaxConcurrentAgents()
	}
	if maxConcurrent > maxAgents {
		maxConcurrent = maxAgents
	}
	ctx, cancel := context.WithCancel(parent)
	return &Graph{
		ctx:           ctx,
		cancel:        cancel,
		runner:        runner,
		maxAgents:     maxAgents,
		maxConcurrent: maxConcurrent,
		agents:        make(map[string]*subAgentState),
	}
}

// Register installs synchronous and asynchronous delegation tools. Async
// delegation is always available: lifecycle safety comes from scan scoping and
// cancellation, rather than an environment flag that silently contradicts the
// coordinator prompt.
func (g *Graph) Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name:        "create_agent",
		Description: fmt.Sprintf("Create and run a focused sub-agent synchronously. The call returns only after the delegated work completes. At most %d delegated agents may be created during the entire scan.", g.maxAgents),
		Parameters: []tools.Parameter{
			{Name: "name", Description: "Short specialist role name", Required: true},
			{Name: "task", Description: "Bounded task, expected evidence, and stopping condition", Required: true},
			{Name: "target", Description: "Target URL/path for the specialist", Required: false},
		},
		Execute: g.createAgent,
	})

	r.Register(&tools.Tool{
		Name:        "spawn_agent",
		Description: fmt.Sprintf("Spawn a focused sub-agent asynchronously. Returns an agent_id immediately; use check_agent or wait_agent to collect its result. At most %d delegated agents may be created during the entire scan; use one non-overlapping wave.", g.maxAgents),
		Parameters: []tools.Parameter{
			{Name: "name", Description: "Short specialist role name", Required: true},
			{Name: "task", Description: "Bounded task, expected evidence, and stopping condition", Required: true},
			{Name: "target", Description: "Target URL/path for the specialist", Required: false},
		},
		Execute: g.spawnAgent,
	})

	r.Register(&tools.Tool{
		Name:        "check_agent",
		Description: "Check a delegated agent's status and recent partial evidence. A completed result is marked collected.",
		Parameters: []tools.Parameter{
			{Name: "agent_id", Description: "ID returned by spawn_agent", Required: true},
		},
		Execute: g.checkAgent,
	})

	r.Register(&tools.Tool{
		Name:        "wait_agent",
		Description: "Wait for a delegated agent to complete or fail, then collect its result.",
		Parameters: []tools.Parameter{
			{Name: "agent_id", Description: "ID returned by spawn_agent", Required: true},
			{Name: "timeout", Description: "Seconds to wait (default 30, maximum 3600)", Required: false},
		},
		Execute: g.waitAgent,
	})
}

func (g *Graph) validateArgs(args map[string]string) (string, string, []string, error) {
	name := strings.TrimSpace(args["name"])
	task := strings.TrimSpace(args["task"])
	if name == "" || task == "" {
		return "", "", nil, fmt.Errorf("name and task are required")
	}
	if g == nil || g.runner == nil {
		return "", "", nil, fmt.Errorf("agent runner not initialized")
	}
	targets := []string{}
	if target := strings.TrimSpace(args["target"]); target != "" {
		targets = append(targets, target)
	}
	return name, task, targets, nil
}

func (g *Graph) capacityResult(action string) tools.Result {
	if g.ctx.Err() != nil {
		return tools.Result{Output: "Cannot " + action + ": the parent scan is stopping."}
	}
	g.mu.Lock()
	delegated := g.delegated
	stopped := g.stopped
	g.mu.Unlock()
	if stopped {
		return tools.Result{Output: "Cannot " + action + ": the parent scan is stopping."}
	}
	if delegated >= g.maxAgents {
		return tools.Result{Output: fmt.Sprintf("Cannot %s: delegation budget exhausted (%d/%d total agents already created). Integrate the existing specialist evidence and finish the remaining root work directly; do not start another wave.", action, delegated, g.maxAgents)}
	}
	return tools.Result{Output: fmt.Sprintf("Cannot %s: %d/%d delegated agents are already active. Collect or wait for one before delegating more.\nRunning agents:\n%s", action, g.RunningCount(), g.maxAgents, g.listRunningAgents())}
}

// syncDelegationNote is the correctable nudge attached to a synchronously
// delegated result. create_agent blocks the coordinator for the child's whole
// runtime and holds one concurrent slot the entire time, so a scan that only
// ever calls it cannot be parallel no matter how large the pool is. The note is
// stated on the success path where the model is about to decide what to do next.
func (g *Graph) syncDelegationNote() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	limit := g.maxConcurrent
	g.mu.Unlock()
	if limit <= 1 {
		return ""
	}
	return fmt.Sprintf("NOTE: create_agent is synchronous — this call blocked the coordinator for the child's full runtime and held 1 of %d concurrent slots. Use spawn_agent for lanes that should run in parallel.\n\n", limit)
}

// notifyAgentStatus emits the admission notification for a freshly admitted
// delegation: "agent_started" when it took a slot, "agent_queued" otherwise.
func (g *Graph) notifyAgentStatus(agentID, lane, task, target string, started bool) {
	if started {
		g.notify("agent_started", agentID, lane, "running", task, target)
		return
	}
	g.notify("agent_queued", agentID, lane, "queued", task, target)
}

func (g *Graph) nextIDLocked(prefix string) string {
	g.counter++
	return fmt.Sprintf("%s_%d_%d", prefix, g.counter, time.Now().UnixNano())
}

func (g *Graph) runAgent(state *subAgentState) {
	defer g.wg.Done()
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("[PANIC] delegated agent %s panicked: %v\n%s", state.ID, recovered, debug.Stack())
			g.complete(state.ID, "", fmt.Errorf("panic: %v", recovered))
		}
		g.onAgentDone()
	}()

	summary, runErr := g.runner(g.ctx, state.ID, state.Name, state.Targets, state.Task)
	g.complete(state.ID, summary, runErr)
}

func (g *Graph) onAgentDone() {
	g.mu.Lock()
	if g.stopped || g.ctx.Err() != nil {
		g.activeAgents--
		g.mu.Unlock()
		return
	}
	if len(g.queue) > 0 {
		next := g.queue[0]
		g.queue = g.queue[1:]
		next.Status = "running"
		next.StartedAt = time.Now()
		if err := g.persistLocked(); err != nil {
			// Mirror ResumeCheckpointedWork's rollback: the agent did not
			// start, so it must not stay "running" — that ghost never
			// completes and hangs wait_agent plus the finish gate. Requeue it
			// at the front so a later completion retries the start.
			g.activeAgents--
			next.Status = "queued"
			g.queue = append([]*subAgentState{next}, g.queue...)
			g.mu.Unlock()
			return
		}
		g.wg.Add(1)
		agentID, lane := next.ID, next.Name
		task, target := next.Task, strings.Join(next.Targets, ",")
		g.mu.Unlock()
		go g.runAgent(next)
		g.notify("agent_started", agentID, lane, "running", task, target)
		return
	}
	g.activeAgents--
	g.mu.Unlock()
}

func (g *Graph) createAgent(args map[string]string) (tools.Result, error) {
	name, task, targets, err := g.validateArgs(args)
	if err != nil {
		return tools.Result{}, err
	}

	g.mu.Lock()
	if g.stopped || g.ctx.Err() != nil {
		g.mu.Unlock()
		return g.capacityResult("create agent"), nil
	}
	if g.delegated >= g.maxAgents {
		g.mu.Unlock()
		return g.capacityResult("create agent"), nil
	}
	g.delegated++

	agentID := g.nextIDLocked("sync")
	state := &subAgentState{
		ID:        agentID,
		Name:      name,
		Task:      task,
		Targets:   append([]string(nil), targets...),
		StartedAt: time.Now(),
		done:      make(chan struct{}),
	}
	g.agents[agentID] = state
	state.Status = "queued"
	if err := g.persistLocked(); err != nil {
		// Roll the admission back out: without a durable checkpoint the
		// delegation never happened. Leaving the state or the budget behind
		// would strand a ghost agent (queued forever) in PendingSummary,
		// UncollectedCount and the finish gate.
		delete(g.agents, agentID)
		g.delegated--
		g.mu.Unlock()
		return tools.Result{Error: "Cannot create agent: delegation checkpoint could not be saved."}, nil
	}

	if g.activeAgents < g.maxConcurrent {
		g.activeAgents++
		state.Status = "running"
		g.wg.Add(1)
		go g.runAgent(state)
	} else {
		state.Status = "queued"
		g.queue = append(g.queue, state)
	}
	started := state.Status == "running"
	g.mu.Unlock()

	g.notifyAgentStatus(agentID, name, task, strings.Join(targets, ","), started)
	g.auditDelegation(domainjournal.OpAgentDelegat, fmt.Sprintf("create_agent id=%s role=%q task=%q", agentID, name, truncTask(task, 200)))

	select {
	case <-state.done:
	case <-g.ctx.Done():
		return tools.Result{Output: "Cannot create agent: the parent scan is stopping."}, nil
	}

	g.mu.Lock()
	result := state.Result
	runErr := state.Error
	elapsed := state.CompletedAt.Sub(state.StartedAt)
	state.Observed = true
	g.mu.Unlock()

	note := g.syncDelegationNote()
	if runErr != "" {
		return tools.Result{Output: note + fmt.Sprintf("Sub-agent %q failed after %s: %s", name, elapsed.Round(time.Second), runErr)}, nil
	}
	return tools.Result{
		Output: note + fmt.Sprintf("Sub-agent %q completed in %s\n%s", name, elapsed.Round(time.Second), result),
		Metadata: map[string]any{
			"agent_id":   agentID,
			"agent_name": name,
			"elapsed":    elapsed.Seconds(),
		},
	}, nil
}

func (g *Graph) spawnAgent(args map[string]string) (tools.Result, error) {
	name, task, targets, err := g.validateArgs(args)
	if err != nil {
		return tools.Result{}, err
	}

	g.mu.Lock()
	if g.stopped || g.ctx.Err() != nil {
		g.mu.Unlock()
		return g.capacityResult("spawn agent"), nil
	}
	if g.delegated >= g.maxAgents {
		g.mu.Unlock()
		return g.capacityResult("spawn agent"), nil
	}
	g.delegated++

	agentID := g.nextIDLocked("sub")
	state := &subAgentState{
		ID:        agentID,
		Name:      name,
		Task:      task,
		Targets:   append([]string(nil), targets...),
		StartedAt: time.Now(),
		done:      make(chan struct{}),
	}
	g.agents[agentID] = state
	state.Status = "queued"
	if err := g.persistLocked(); err != nil {
		// Same rollback as createAgent: no durable checkpoint means no
		// delegation — free the identity and the lifetime budget slot.
		delete(g.agents, agentID)
		g.delegated--
		g.mu.Unlock()
		return tools.Result{Error: "Cannot spawn agent: delegation checkpoint could not be saved."}, nil
	}

	if g.activeAgents < g.maxConcurrent {
		g.activeAgents++
		state.Status = "running"
		g.wg.Add(1)
		go g.runAgent(state)
	} else {
		state.Status = "queued"
		g.queue = append(g.queue, state)
	}
	started := state.Status == "running"
	g.mu.Unlock()

	g.notifyAgentStatus(agentID, name, task, strings.Join(targets, ","), started)
	g.auditDelegation(domainjournal.OpAgentDelegat, fmt.Sprintf("spawn_agent id=%s role=%q task=%q", agentID, name, truncTask(task, 200)))

	target := ""
	if len(targets) > 0 {
		target = targets[0]
	}
	return tools.Result{
		Output: fmt.Sprintf("Sub-agent %q spawned with ID %s\nTask: %s\nTarget: %s\n\nContinue coordinating, then call wait_agent(agent_id=%s) before finishing.", name, agentID, truncTask(task, 200), target, agentID),
		Metadata: map[string]any{
			"agent_id":   agentID,
			"agent_name": name,
			"spawned":    true,
		},
	}, nil
}

func (g *Graph) complete(agentID, summary string, runErr error) {
	g.mu.Lock()
	state, ok := g.agents[agentID]
	if !ok || (state.Status != "running" && state.Status != "queued") {
		g.mu.Unlock()
		return
	}
	state.CompletedAt = time.Now()
	if errors.Is(runErr, context.Canceled) {
		state.Status = "failed"
		state.Error = "parent scan stopped"
		state.Result = summary
	} else if runErr != nil {
		state.Status = "failed"
		state.Error = runErr.Error()
		state.Result = summary
	} else {
		state.Status = "completed"
		state.Result = summary
	}
	state.doneOnce.Do(func() { close(state.done) })
	_ = g.persistLocked()
	// Delegation completion audit: the sink serializes on the journal's own
	// mutex and never calls back into the graph, so calling it under g.mu is
	// deadlock-free (auditDelegation must NOT be used here — it re-locks).
	if g.audit != nil {
		elapsed := state.CompletedAt.Sub(state.StartedAt).Round(time.Second)
		if state.Error != "" {
			g.audit(domainjournal.OpAgentDelegat, fmt.Sprintf("delegate_result id=%s role=%q status=failed after=%s error=%q", state.ID, state.Name, elapsed, truncTask(state.Error, 200)))
		} else {
			g.audit(domainjournal.OpAgentDelegat, fmt.Sprintf("delegate_result id=%s role=%q status=completed in=%s", state.ID, state.Name, elapsed))
		}
	}
	lane, status := state.Name, state.Status
	task, target := state.Task, strings.Join(state.Targets, ",")
	g.mu.Unlock()
	kind := "agent_completed"
	if status == "failed" {
		kind = "agent_failed"
	}
	g.notify(kind, agentID, lane, status, task, target)
}

type agentSnapshot struct {
	ID          string
	Name        string
	Task        string
	Status      string
	StartedAt   time.Time
	CompletedAt time.Time
	Result      string
	Error       string
	Observed    bool
	Partial     []string
	done        <-chan struct{}
}

func (g *Graph) snapshot(agentID string, observeCompleted bool) (agentSnapshot, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state, ok := g.agents[agentID]
	if !ok {
		return agentSnapshot{}, false
	}
	if observeCompleted && state.Status != "running" && state.Status != "queued" {
		state.Observed = true
		_ = g.persistLocked()
	}
	return agentSnapshot{
		ID:          state.ID,
		Name:        state.Name,
		Task:        state.Task,
		Status:      state.Status,
		StartedAt:   state.StartedAt,
		CompletedAt: state.CompletedAt,
		Result:      state.Result,
		Error:       state.Error,
		Observed:    state.Observed,
		Partial:     append([]string(nil), state.Partial...),
		done:        state.done,
	}, true
}

func (g *Graph) checkAgent(args map[string]string) (tools.Result, error) {
	agentID := strings.TrimSpace(args["agent_id"])
	if agentID == "" {
		return tools.Result{}, fmt.Errorf("agent_id is required")
	}
	state, ok := g.snapshot(agentID, true)
	if !ok {
		return tools.Result{Output: fmt.Sprintf("Agent %q not found.\nAvailable agents:\n%s", agentID, g.listAllAgents())}, nil
	}
	return renderSnapshot(state), nil
}

func renderSnapshot(state agentSnapshot) tools.Result {
	var b strings.Builder
	elapsed := time.Since(state.StartedAt).Round(time.Second)
	switch state.Status {
	case "queued":
		fmt.Fprintf(&b, "Agent %q (%s) — QUEUED (waiting for execution slot)\nTask: %s\n", state.Name, state.ID, truncTask(state.Task, 150))
	case "running":
		fmt.Fprintf(&b, "Agent %q (%s) — RUNNING for %s\nTask: %s\n", state.Name, state.ID, elapsed, truncTask(state.Task, 150))
		if len(state.Partial) == 0 {
			b.WriteString("\n(No partial evidence yet.)\n")
		} else {
			b.WriteString("\n--- Recent partial evidence ---\n")
			start := 0
			if len(state.Partial) > 5 {
				start = len(state.Partial) - 5
			}
			for _, partial := range state.Partial[start:] {
				b.WriteString(partial)
				b.WriteByte('\n')
			}
		}
	case "completed":
		elapsed = state.CompletedAt.Sub(state.StartedAt).Round(time.Second)
		fmt.Fprintf(&b, "Agent %q (%s) — COMPLETED in %s\n\n--- Results ---\n%s", state.Name, state.ID, elapsed, truncTask(state.Result, 10000))
	case "failed":
		if !state.CompletedAt.IsZero() {
			elapsed = state.CompletedAt.Sub(state.StartedAt).Round(time.Second)
		}
		fmt.Fprintf(&b, "Agent %q (%s) — FAILED after %s\nError: %s", state.Name, state.ID, elapsed, state.Error)
		if state.Result != "" {
			fmt.Fprintf(&b, "\n\n--- Partial results ---\n%s", truncTask(state.Result, 10000))
		}
	}
	return tools.Result{
		Output: b.String(),
		Metadata: map[string]any{
			"agent_id": state.ID,
			"status":   state.Status,
			"elapsed":  elapsed.Seconds(),
		},
	}
}

func (g *Graph) waitAgent(args map[string]string) (tools.Result, error) {
	agentID := strings.TrimSpace(args["agent_id"])
	if agentID == "" {
		return tools.Result{}, fmt.Errorf("agent_id is required")
	}
	state, ok := g.snapshot(agentID, false)
	if !ok {
		return tools.Result{Output: fmt.Sprintf("Agent %q not found.\nAvailable agents:\n%s", agentID, g.listAllAgents())}, nil
	}
	if state.Status != "running" && state.Status != "queued" {
		state, _ = g.snapshot(agentID, true)
		return renderSnapshot(state), nil
	}

	timeout := DefaultWaitTimeout
	if raw := strings.TrimSpace(args["timeout"]); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 {
			return tools.Result{}, fmt.Errorf("timeout must be a non-negative number of seconds")
		}
		if seconds == 0 {
			timeout = time.Hour
		} else {
			if seconds > 3600 {
				seconds = 3600
			}
			timeout = time.Duration(seconds) * time.Second
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-state.done:
		state, ok = g.snapshot(agentID, true)
		if !ok {
			return tools.Result{Output: fmt.Sprintf("Agent %q was removed before its result could be collected.", agentID)}, nil
		}
		return renderSnapshot(state), nil
	case <-timer.C:
		state, _ = g.snapshot(agentID, false)
		return tools.Result{
			Output:   fmt.Sprintf("Timeout waiting for agent %q (%s) after %s. It is still running; use check_agent or wait_agent again.", state.Name, state.ID, time.Since(state.StartedAt).Round(time.Second)),
			Metadata: map[string]any{"agent_id": agentID, "status": "timeout"},
		}, nil
	case <-g.ctx.Done():
		state, ok = g.snapshot(agentID, true)
		if ok && state.Status != "running" && state.Status != "queued" {
			return renderSnapshot(state), nil
		}
		return tools.Result{Output: fmt.Sprintf("Parent scan stopped while waiting for agent %q.", agentID)}, nil
	}
}

// DelegationCount returns the number of agents created over this graph's
// lifetime, including already-completed synchronous and asynchronous workers.
func (g *Graph) DelegationCount() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.delegated
}

// AddPartialResult attaches streaming evidence to the coordinator-visible ID.
func (g *Graph) AddPartialResult(agentID, result string) {
	if g == nil || strings.TrimSpace(result) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state, ok := g.agents[agentID]
	if !ok || state.Status != "running" {
		return
	}
	state.Partial = append(state.Partial, result)
	if len(state.Partial) > 50 {
		state.Partial = append([]string(nil), state.Partial[len(state.Partial)-50:]...)
	}
}

// RunningCount returns the number of active (running or queued) delegated agents in this scan.
func (g *Graph) RunningCount() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	count := 0
	for _, state := range g.agents {
		if state.Status == "running" || state.Status == "queued" {
			count++
		}
	}
	return count
}

// UncollectedCount returns completed/failed delegations whose final result has
// not yet been read by check_agent or wait_agent.
func (g *Graph) UncollectedCount() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	count := 0
	for _, state := range g.agents {
		if state.Status != "running" && state.Status != "queued" && !state.Observed {
			count++
		}
	}
	return count
}

// PendingSummary gives the coordinator actionable IDs for finish gating.
func (g *Graph) PendingSummary() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var lines []string
	for _, state := range g.agents {
		switch {
		case state.Status == "running" || state.Status == "queued":
			lines = append(lines, fmt.Sprintf("- %s (%s): %s — call wait_agent", state.Name, state.ID, state.Status))
		case !state.Observed:
			lines = append(lines, fmt.Sprintf("- %s (%s): %s, result not collected — call check_agent", state.Name, state.ID, state.Status))
		}
	}
	return strings.Join(lines, "\n")
}

// Stop cancels this scan's delegated agents and unblocks all waiters. It never
// touches any other scan graph. Runner implementations receive the cancellation
// through their context and are expected to unwind promptly.
func (g *Graph) Stop() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return
	}
	g.stopped = true
	g.cancel()
	now := time.Now()
	g.queue = nil
	var stopped []AgentState
	for _, state := range g.agents {
		if state.Status == "running" || state.Status == "queued" {
			state.Status = "failed"
			state.Error = "parent scan stopped"
			state.CompletedAt = now
			state.doneOnce.Do(func() { close(state.done) })
			stopped = append(stopped, AgentState{AgentID: state.ID, Lane: state.Name, Task: state.Task, Target: strings.Join(state.Targets, ",")})
		}
	}
	_ = g.persistLocked()
	g.mu.Unlock()
	for _, agent := range stopped {
		g.notify("agent_failed", agent.AgentID, agent.Lane, "failed", agent.Task, agent.Target)
	}
}

// WaitStopped waits for delegated runner goroutines to unwind. It is mainly
// useful for shutdown paths and race tests; normal scan cleanup may remain
// non-blocking after calling Stop.
func (g *Graph) WaitStopped(timeout time.Duration) bool {
	if g == nil {
		return true
	}
	if timeout <= 0 {
		g.wg.Wait()
		return true
	}
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (g *Graph) listRunningAgents() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var b strings.Builder
	for _, state := range g.agents {
		if state.Status == "running" || state.Status == "queued" {
			fmt.Fprintf(&b, "  - %s (%s, status: %s): %s — %s\n", state.Name, state.ID, state.Status, truncTask(state.Task, 80), time.Since(state.StartedAt).Round(time.Second))
		}
	}
	if b.Len() == 0 {
		return "  (none)"
	}
	return b.String()
}

func (g *Graph) listAllAgents() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var b strings.Builder
	for _, state := range g.agents {
		fmt.Fprintf(&b, "  - %s (%s): %s [%s]\n", state.Name, state.ID, truncTask(state.Task, 80), state.Status)
	}
	if b.Len() == 0 {
		return "  (none)"
	}
	return b.String()
}

func truncTask(value string, max int) string {
	if len(value) > max {
		return value[:max] + "..."
	}
	return value
}
