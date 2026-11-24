package orchestrator

// planner.go — the R7 planning agent loop (M2c).
//
// One fresh LLM call per round: the model reads a compact graph overview and
// returns 0..N concrete intents plus optional retirements as strict JSON. It
// never executes tools; the worker pool claims what it writes. A failed or
// unconfigured planner is non-fatal: the engine hook planner still maintains
// the deterministic floor.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/gaps"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/methodology"
	"github.com/c1ayooo/xalgorix/v4/internal/promptrender"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

// PlannerCriterion is the neutral projection of one objective criterion for
// the planner prompt (no platform/agent import cycle).
type PlannerCriterion struct {
	ID         string
	Kind       string
	VulnClass  string
	Capability string
	VerifyBy   string
	Required   bool
}

// StalledWorker is one bounded observation of a worker the engine considers
// stalled: no information-layer progress inside the lane's threshold. It is a
// projection for the planner prompt, not a state authority.
type StalledWorker struct {
	UnitID       string
	AgentID      string
	Lane         string
	IdleFor      time.Duration
	ActiveTool   bool
	LastProgress time.Time
}

// PlannerConfig assembles one run's planning loop.
type PlannerConfig struct {
	ScanID    string
	SessionID string
	Target    string
	// Goal is the human-authored task goal (tasks.goal). It is the primary
	// success definition; the objective statement is derived analysis.
	Goal        string
	Instruction string
	// UserInstruction is the operator/task instruction verbatim
	// (tasks.instruction): the how/constraints brief.
	UserInstruction string
	// ObjectiveStatement/Constraints/Criteria are the derived goal analysis.
	// They are auxiliary and never override Goal or UserInstruction.
	ObjectiveStatement   string
	ObjectiveConstraints []string
	ObjectiveCriteria    []PlannerCriterion
	Client               *llm.Client
	Repo                 graph.Repository
	ScanCtx              *scanctx.ScanContext
	// InfoSpace is the optional unified information-space reader (IG2). When
	// nil the planner falls back to graph.Backend() if it implements
	// infospace.Reader, and then to the lexical prior-work block. A missing
	// reader (PG unconfigured, views not migrated) is not an error: the
	// overview stays byte-compatible with the pre-IG2 output.
	InfoSpace infospace.Reader
	// ControlPlaneHosts are never scan targets. The planner is told so
	// explicitly and its intents are filtered by endpoint host.
	ControlPlaneHosts []string
	// ScopeTargets/ScopeDeny/ScopeAllowPorts are the project's enforced
	// engagement scope. Non-empty ScopeTargets enables target_kind=scope and
	// the AUTHORIZED SCOPE overview section; otherwise the planner behaves
	// exactly as before (benchmark prompts stay byte-identical).
	ScopeTargets    []string
	ScopeDeny       []string
	ScopeAllowPorts []int
	// Emit publishes planner activity to the run's event stream (nil-safe).
	Emit func(kind, content string, meta map[string]any)
	// Interval between heartbeat rounds (default 60s).
	Interval time.Duration
	// OnDrained wakes the main agent for a finish check only after a planner
	// round leaves the frontier empty. This prevents a just-settled recon
	// intent from finishing the run before the planner gets a chance to plan.
	OnDrained func()
	// WaitForCapacity blocks a round until a worker slot is free. This is the
	// producer-side backpressure: the planner never stockpiles open intents
	// beyond the configured worker count; a settling worker wakes it.
	WaitForCapacity func(context.Context) error
	// MaxIntents bounds how many intents one round may add (default 4; the
	// server sets it to the configured worker count so the pool can fill).
	MaxIntents int
	// Steer injects a course correction into one running worker. Optional: a
	// nil callback disables the steer_work planner tool.
	Steer func(unitID, message string) error
	// Kill ends one running worker with a validated reason code. Optional: a
	// nil callback disables the kill_work planner tool.
	Kill func(unitID, reasonCode, reason string) error
	// StallReport returns the engine's current stalled-worker observations. When
	// nil, the planner prompt has no STALLED WORKERS section.
	StallReport func() []StalledWorker
}

// Planner is the event-driven LLM intent generator.
type Planner struct {
	cfg    PlannerConfig
	ctx    context.Context
	cancel context.CancelFunc
	notify chan struct{}

	// Cheap-gate state. settlePending/stallPending are real planning triggers;
	// drained means the frontier is empty and only a trigger may wake it.
	initialDone   atomic.Bool
	settlePending atomic.Bool
	stallPending  atomic.Bool
	drained       atomic.Bool
	// backoffIdx walks plannerBackoffLadder after consecutive no-action rounds.
	backoffIdx atomic.Int64
	// modelRounds counts rounds where the model actually answered. An empty
	// frontier before the model has ever produced a plan is not "drained": the
	// first round may have failed (provider timeout), and the planner must
	// retry with backoff instead of sleeping forever.
	modelRounds atomic.Int64
	// roundActions counts steer/kill writes issued in the current round; the
	// final JSON path's added/retired counts are tracked separately.
	roundActions atomic.Int64
	// roundCorrected marks units the model already steered/killed this round.
	// stallMisses counts consecutive rounds that saw the same unit stalled
	// without a corrective action; two misses trigger the engine-assisted
	// stall guard (the planner still owns the decision, but it cannot silently
	// ignore a persistent stall forever).
	roundCorrected map[string]bool
	stallMisses    map[string]int
	// briefMu guards the hot-updatable task brief (goal/instruction/objective).
	// The planner loop reads it every round; UpdateBriefing mutates it from the
	// HTTP path when the operator edits the task mid-run.
	briefMu   sync.RWMutex
	brief     LiveTaskBrief
	briefInit bool
}

// plannerNotifyCoalesce merges a burst of worker settles into one round.
const plannerNotifyCoalesce = 2 * time.Second

// plannerBackoffLadder returns the round interval ladder: the configured base
// (60s by default), then 2m, 5m, 15m for consecutive no-action rounds.
func plannerBackoffLadder(base time.Duration) []time.Duration {
	if base <= 0 {
		base = time.Minute
	}
	return []time.Duration{base, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}
}

// UpdateBriefing replaces the live task brief and wakes the planner so the new
// direction is picked up in the next round (no worker kill/steer).
func (p *Planner) UpdateBriefing(brief LiveTaskBrief) {
	p.briefMu.Lock()
	p.brief = brief.clone()
	p.briefInit = true
	p.briefMu.Unlock()
	p.Notify()
}

func (p *Planner) briefSnapshot() LiveTaskBrief {
	p.briefMu.RLock()
	init := p.briefInit
	brief := p.brief
	p.briefMu.RUnlock()
	if !init {
		// Struct-literal planners (tests/legacy construction) never ran the
		// constructor; their config is the brief.
		return briefFromPlannerConfig(p.cfg)
	}
	return brief.clone()
}

func NewPlanner(parent context.Context, cfg PlannerConfig) *Planner {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.MaxIntents <= 0 {
		cfg.MaxIntents = 4
	}
	if cfg.MaxIntents > 10 {
		cfg.MaxIntents = 10
	}
	ctx, cancel := context.WithCancel(parent)
	return &Planner{
		cfg:       cfg,
		brief:     briefFromPlannerConfig(cfg),
		briefInit: true,
		ctx:       ctx,
		cancel:    cancel,
		notify:    make(chan struct{}, 1),
	}
}

// maxIntents applies the default/cap even for planners built as struct
// literals in tests (NewPlanner normalizes its own copy of the config).
func (p *Planner) maxIntents() int {
	n := p.cfg.MaxIntents
	if n <= 0 {
		n = 4
	}
	if n > 10 {
		n = 10
	}
	return n
}

// SetWaitForCapacity binds producer backpressure after the pool exists.
func (p *Planner) SetWaitForCapacity(fn func(context.Context) error) {
	if p != nil {
		p.cfg.WaitForCapacity = fn
	}
}

// SetStallReport binds the engine's live stall projection. It must be called
// before Start (the planner reads it from each overview round).
func (p *Planner) SetStallReport(fn func() []StalledWorker) {
	if p != nil {
		p.cfg.StallReport = fn
	}
}

// SetMaxIntents aligns the per-round addition cap with the worker count.
func (p *Planner) SetMaxIntents(n int) {
	if p == nil {
		return
	}
	if n < 1 {
		n = 1
	}
	if n > 10 {
		n = 10
	}
	p.cfg.MaxIntents = n
}

// Start runs the first round immediately, then heartbeat/notify rounds.
func (p *Planner) Start() {
	if p == nil || p.cfg.Client == nil || p.cfg.Repo == nil {
		return
	}
	go p.loop()
}

// Notify wakes the planner after a worker settles (or new evidence appears).
// Bursts are coalesced into one round by loop().
func (p *Planner) Notify() {
	if p == nil {
		return
	}
	p.settlePending.Store(true)
	p.wake()
}

// Stall wakes the planner on an explicit stall signal. It is a hard trigger:
// the round runs promptly and the backoff is reset.
func (p *Planner) Stall() {
	if p == nil {
		return
	}
	p.stallPending.Store(true)
	p.wake()
}

func (p *Planner) wake() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

func (p *Planner) Stop() {
	if p != nil {
		p.cancel()
	}
}

// interval is the current wait between rounds: the backoff step selected by
// consecutive no-action rounds.
func (p *Planner) interval() time.Duration {
	ladder := plannerBackoffLadder(p.cfg.Interval)
	idx := int(p.backoffIdx.Load())
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ladder) {
		idx = len(ladder) - 1
	}
	return ladder[idx]
}

// recordRound advances or resets the backoff ladder. Any decision (added /
// retired / steer / kill) or real trigger (settle / stall) resets it.
func (p *Planner) recordRound(acted, settle, stall bool) {
	if acted || settle || stall {
		p.backoffIdx.Store(0)
		return
	}
	idx := p.backoffIdx.Load()
	if maxIdx := int64(len(plannerBackoffLadder(p.cfg.Interval)) - 1); idx < maxIdx {
		idx++
	}
	p.backoffIdx.Store(idx)
}

func (p *Planner) loop() {
	p.round()
	for {
		if p.ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(p.interval())
		woke := false
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return
		case <-p.notify:
			timer.Stop()
			woke = true
		case <-timer.C:
		}
		if woke && !p.coalesce() {
			return
		}
		p.round()
	}
}

// coalesce merges a burst of trigger signals into the round that is about to
// run. The pending flags are already recorded, so dropping further wakeups in
// the window loses nothing.
func (p *Planner) coalesce() bool {
	timer := time.NewTimer(plannerNotifyCoalesce)
	defer timer.Stop()
	select {
	case <-p.ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-p.notify:
		return true
	}
}

type plannerIntent struct {
	TaskID       string         `json:"task_id"`
	Title        string         `json:"title"`
	Phase        plannerInt     `json:"phase"`
	VulnClass    string         `json:"vuln_class"`
	Endpoint     string         `json:"endpoint"`
	Target       string         `json:"target"`
	TargetKind   string         `json:"target_kind"`
	DependsOn    plannerStrings `json:"depends_on"`
	Notes        string         `json:"notes"`
	ObjectiveRef string         `json:"objective_ref"`
	TimeboxS     plannerInt     `json:"timebox_s"`
}

// plannerInt accepts a JSON number or a string ("6", "phase 6") and extracts
// the first integer. LLM JSON frequently wraps numeric fields in strings; a
// single mismatch must not discard an otherwise valid planning round.
type plannerInt int

func (v *plannerInt) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	raw = strings.Trim(raw, `"`)
	if raw == "" || raw == "null" {
		*v = 0
		return nil
	}
	if n, err := strconv.Atoi(raw); err == nil {
		*v = plannerInt(n)
		return nil
	}
	if m := plannerFirstIntRE.FindString(raw); m != "" {
		if n, err := strconv.Atoi(m); err == nil {
			*v = plannerInt(n)
			return nil
		}
	}
	*v = 0
	return nil
}

var plannerFirstIntRE = regexp.MustCompile(`-?\d+`)

// plannerStrings accepts a JSON array or a string form such as
// "['a','b']" / "a, b" / "a b".
type plannerStrings []string

func (v *plannerStrings) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	if raw == "" || raw == "null" {
		*v = nil
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		*v = arr
		return nil
	}
	s := strings.Trim(raw, `"`)
	s = strings.TrimSpace(strings.Trim(s, "[]"))
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(strings.TrimSpace(p), `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	*v = out
	return nil
}

type plannerOutput struct {
	Intents []plannerIntent `json:"intents"`
	Retire  []string        `json:"retire"`
}

// round runs one planning round and reports whether it changed the plan
// (added/retired intents or steered/killed a worker). Cheap gates run before
// any model call so a saturated or drained frontier never spins.
func (p *Planner) round() (acted bool) {
	if p == nil || p.ctx.Err() != nil {
		return false
	}
	settle := p.settlePending.Swap(false)
	stall := p.stallPending.Swap(false)
	p.roundActions.Store(0)
	p.roundCorrected = make(map[string]bool)
	stalls := p.currentStalls()
	if len(stalls) > 0 {
		log.Printf("[planner] %s: %d stalled worker(s) in this round", p.cfg.ScanID, len(stalls))
	}
	first := !p.initialDone.Swap(true)
	defer func() {
		if p.roundActions.Load() > 0 {
			acted = true
		}
		p.recordRound(acted, settle, stall)
	}()
	// The model owns the corrective decision. If it ignores the same stall for
	// two consecutive rounds, the guard issues one bounded steer so a stalled
	// worker cannot silently keep burning its budget.
	defer p.applyStallGuard(stalls)

	// Cheap pre-check: fetch only the graph counters. The first round always
	// runs (it is what turns an empty frontier into work); later rounds skip
	// when the frontier is already full or drained and no real trigger woke us.
	if !first && !p.shouldPlan(settle, stall) {
		return false
	}
	// Producer backpressure: while every configured worker is executing, wait
	// for one to settle instead of queueing more open intents. This keeps the
	// frontier at the configured worker count instead of growing without bound.
	if p.cfg.WaitForCapacity != nil {
		if err := p.cfg.WaitForCapacity(p.ctx); err != nil {
			return false
		}
	}
	if p.cfg.Client == nil {
		return false
	}
	overview := p.overview()
	msgs := []llm.Message{
		{Role: "system", Content: plannerPromptFor(p.cfg.ControlPlaneHosts, len(p.cfg.ScopeTargets) > 0)},
		{Role: "user", Content: overview},
	}
	// Bounded tool loop (R7 M3 follow-up): the planner may inspect the graph
	// with read tools and may write an intent/retirement through a tool, then
	// finish with the strict-JSON plan. A provider that returns no tool calls
	// keeps the original single-call behavior byte-for-byte.
	added, retired := 0, 0
	const maxRounds = 4
	for attempt := 0; attempt < maxRounds; attempt++ {
		content, calls, err := p.callModel(msgs)
		if err != nil {
			log.Printf("[planner] %s: LLM call failed: %v", p.cfg.ScanID, err)
			return false
		}
		p.modelRounds.Add(1)
		if len(calls) == 0 {
			parsed, perr := parsePlannerOutput(content)
			if perr != nil {
				log.Printf("[planner] %s: invalid planner output: %v", p.cfg.ScanID, perr)
				return false
			}
			a, r := p.apply(parsed)
			added += a
			retired += r
			break
		}
		calls = llm.EnsureToolCallIDs(calls, fmt.Sprintf("plan%d", attempt))
		msgs = append(msgs, plannerAssistantMessage(content, calls))
		for _, call := range calls {
			out, a, r := p.execPlannerTool(call, added)
			added += a
			retired += r
			msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: call.ID, Content: out})
		}
	}
	if added > 0 || retired > 0 {
		if p.cfg.Emit != nil {
			p.cfg.Emit("plan_updated", fmt.Sprintf("planner: +%d intents, -%d retired", added, retired), map[string]any{
				"source": "llm", "added": added, "retired": retired,
			})
		}
	}
	if added == 0 && p.cfg.OnDrained != nil {
		// A successful planning round with no new intent and an empty frontier
		// is the safe moment to wake the main agent for a finish decision.
		ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
		counts, err := p.cfg.Repo.Counts(ctx, p.cfg.ScanID)
		cancel()
		if err == nil && counts.OpenIntents+counts.RunningIntents+counts.PausedIntents == 0 &&
			counts.DoneIntents+counts.BlockedIntents > 0 {
			p.cfg.OnDrained()
		}
	}
	return added > 0 || retired > 0
}

// shouldPlan is the cheap gate that runs before any model call. It only reads
// graph counters: no overview assembly, no LLM. A settle (worker finished) or
// stall signal is a real trigger and wakes a drained/full frontier; a plain
// timer round does not.
func (p *Planner) shouldPlan(settle, stall bool) bool {
	if p.cfg.Repo == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(p.ctx, 3*time.Second)
	defer cancel()
	counts, err := p.cfg.Repo.Counts(ctx, p.cfg.ScanID)
	if err != nil {
		// Cannot judge cheaply; behave like the pre-gate planner.
		return true
	}
	if stall {
		p.drained.Store(false)
		return true
	}
	if settle {
		// A settled worker is new information even when the frontier looks
		// drained; let the model decide the next step.
		p.drained.Store(false)
		return counts.OpenIntents < p.maxIntents()
	}
	if p.drained.Load() {
		return false
	}
	if counts.OpenIntents >= p.maxIntents() {
		// The frontier already fills every worker slot: plan again when one
		// settles instead of stockpiling intents.
		return false
	}
	if counts.OpenIntents+counts.RunningIntents+counts.PausedIntents == 0 {
		// Nothing running and nothing to claim, with no trigger: the frontier is
		// drained — but only after the model has actually produced a round.
		// Before that, a failed first round must retry instead of sleeping.
		if p.modelRounds.Load() == 0 && p.cfg.Client != nil {
			return true
		}
		p.drained.Store(true)
		return false
	}
	return true
}

func (p *Planner) overview() string {
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()
	var b strings.Builder
	fmt.Fprintf(&b, "Target (context only, not necessarily a scannable app): %s\n", p.cfg.Target)

	// Three layers, in authority order: human GOAL, human DESCRIPTION, then
	// derived OBJECTIVE analysis. The last one may not override the first two.
	brief := p.briefSnapshot()
	if goal := strings.TrimSpace(brief.Goal); goal != "" {
		fmt.Fprintf(&b, "\nGOAL (human-authored, highest authority — what success means):\n%s\n", truncateText(goal, 4000))
	}
	instr := strings.TrimSpace(brief.UserInstruction)
	if instr == "" {
		instr = brief.Instruction
	}
	if strings.TrimSpace(instr) != "" {
		fmt.Fprintf(&b, "\nDESCRIPTION (human-authored procedure, constraints and success delivery):\n%s\n", truncateText(instr, 20000))
	}
	if brief.ObjectiveStatement != "" || len(brief.ObjectiveConstraints) > 0 || len(brief.ObjectiveCriteria) > 0 {
		b.WriteString("\nOBJECTIVE (derived analysis — auxiliary, never overrides GOAL/DESCRIPTION):\n")
		if st := strings.TrimSpace(brief.ObjectiveStatement); st != "" {
			fmt.Fprintf(&b, "Statement: %s\n", truncateText(st, 3000))
		}
		if len(brief.ObjectiveConstraints) > 0 {
			b.WriteString("Constraints:\n")
			for _, c := range brief.ObjectiveConstraints {
				fmt.Fprintf(&b, "- %s\n", truncateText(strings.TrimSpace(c), 800))
			}
		}
		if len(brief.ObjectiveCriteria) > 0 {
			b.WriteString("Criteria:\n")
			for _, c := range brief.ObjectiveCriteria {
				req := "optional"
				if c.Required {
					req = "REQUIRED"
				}
				detail := c.Kind
				if c.VulnClass != "" {
					detail += ":" + c.VulnClass
				}
				if c.Capability != "" {
					detail += ":" + c.Capability
				}
				fmt.Fprintf(&b, "- %s [%s, %s, verify=%s]\n", c.ID, detail, req, c.VerifyBy)
			}
		}
	}
	if len(p.cfg.ControlPlaneHosts) > 0 {
		fmt.Fprintf(&b, "\nFORBIDDEN CONTROL-PLANE HOSTS (never scan, crawl, fuzz, or exploit): %s\n", strings.Join(p.cfg.ControlPlaneHosts, ", "))
		b.WriteString("If every provided target is a control plane, do not create reconnaissance/exploitation intents against it. Plan only the API/container orchestration described in the DESCRIPTION (for example: list challenges, start a container, attack the returned container address, submit, close), and use the container address as the intent endpoint. The API base is the one named in the DESCRIPTION.\n")
	}
	if block := p.authorizedScopeBlock(); block != "" {
		b.WriteString(block)
	}
	if counts, err := p.cfg.Repo.Counts(ctx, p.cfg.ScanID); err == nil {
		fmt.Fprintf(&b, "\nCounters: open=%d running=%d done=%d blocked=%d beliefs(queued=%d testing=%d proven=%d) findings=%d\n",
			counts.OpenIntents, counts.RunningIntents, counts.DoneIntents, counts.BlockedIntents,
			counts.QueuedBeliefs, counts.TestingBeliefs, counts.ProvenBeliefs, counts.ConfirmedFinding)
	}
	if tasks, err := p.cfg.Repo.PlanTasks(ctx, p.cfg.ScanID); err == nil && len(tasks) > 0 {
		b.WriteString("\nExisting intents:\n")
		// Claimability matters as much as existence: an open intent whose
		// dependency has not settled is parked, not running work. Without this
		// the planner reads "open" as "already covered" and stops emitting the
		// independent lanes that could actually run in parallel.
		states := make(map[string]string, len(tasks))
		for _, t := range tasks {
			states[t.TaskID] = t.GraphState
		}
		for _, t := range tasks {
			line := fmt.Sprintf("- %s [phase %d, %s, %s] %s", promptrender.Cell(t.TaskID, 80), t.Phase,
				promptrender.Cell(t.Status, 20), promptrender.Cell(t.VulnClass, 40), promptrender.Cell(t.Title, 120))
			if len(t.DependsOn) > 0 {
				var waiting []string
				for _, dep := range t.DependsOn {
					if st, ok := states[dep]; ok && st != "" && !graph.TerminalState(graph.KindIntent, graph.State(st)) {
						waiting = append(waiting, dep+"="+st)
					}
				}
				if len(waiting) > 0 {
					line += " (waiting on: " + strings.Join(waiting, ", ") + ")"
				} else {
					line += " (deps settled: " + strings.Join(t.DependsOn, ", ") + ")"
				}
			}
			b.WriteString(line + "\n")
		}
	}
	if block := p.stalledWorkersBlock(); block != "" {
		b.WriteString(block)
	}
	if hints, err := p.cfg.Repo.ListNodes(ctx, p.cfg.ScanID, graph.KindHint); err == nil && len(hints) > 0 {
		var hb strings.Builder
		for i, n := range hints {
			if i >= 20 {
				break
			}
			if n.State != graph.StateActive {
				continue
			}
			msg := str(n.PayloadJSON()["message"])
			if msg != "" {
				fmt.Fprintf(&hb, "- %s\n", promptrender.Cell(msg, 200))
			}
		}
		if hb.Len() > 0 {
			b.WriteString("\nHuman hints:\n")
			b.WriteString(p.untrusted("human hints", hb.String(), 4000))
			b.WriteString("\n")
		}
	}
	if blob := liveNotesBlob(p.cfg.ScanCtx); strings.TrimSpace(blob) != "" {
		b.WriteString("\nShared notes (discovered challenges/containers/routes):\n")
		b.WriteString(p.untrusted("shared notes", blob, 5000))
		b.WriteString("\n")
	}
	if len(p.cfg.ControlPlaneHosts) > 0 {
		if block := p.readyContainerTargetsBlock(); block != "" {
			b.WriteString(block)
		}
	}
	if p.cfg.ScanCtx != nil && p.cfg.ScanCtx.Ledger != nil {
		all := p.cfg.ScanCtx.Ledger.All()
		if len(all) > 0 {
			b.WriteString("\nBeliefs (ledger):\n")
			for i := range all {
				if i >= 40 {
					break
				}
				h := &all[i]
				fmt.Fprintf(&b, "- %s [%s] %s @ %s conf=%.2f\n", promptrender.Cell(h.ID, 40),
					promptrender.Cell(string(h.Status), 20), promptrender.Cell(h.VulnClass, 40),
					promptrender.Cell(h.Endpoint, 120), h.Confidence)
			}
		}
	}
	if p.cfg.SessionID != "" {
		if findings := reporting.GetVulnerabilitiesForContext(p.cfg.SessionID); len(findings) > 0 {
			b.WriteString("\nAccepted findings:\n")
			for i, f := range findings {
				if i >= 20 {
					break
				}
				fmt.Fprintf(&b, "- %s [%s] %s @ %s\n", promptrender.Cell(f.ID, 40),
					promptrender.Cell(f.Severity, 20), promptrender.Cell(f.Title, 100),
					promptrender.Cell(f.Endpoint, 120))
			}
		}
	}
	if block := p.infoSpaceBlock(); block != "" {
		b.WriteString(block)
	} else if prior := p.priorWorkBlock(); prior != "" {
		// Lexical fallback: unchanged whenever the unified space is
		// unavailable (no reader, PG down, views not migrated).
		b.WriteString(prior)
	}
	snap := p.readCoverageSnapshot()
	if gapsBlock := p.coverageGapsBlock(snap); gapsBlock != "" {
		b.WriteString(gapsBlock)
	}
	if cands := p.gapCandidatesBlock(snap); cands != "" {
		b.WriteString(cands)
	}
	b.WriteString("\nReturn STRICT JSON only: {\"intents\":[...],\"retire\":[task_id,...]}.")
	return b.String()
}

// authorizedScopeLimit bounds the scope section. The project scope is operator
// configuration, but the prompt must stay bounded even for a very large list.
const authorizedScopeLimit = 40

// authorizedScopeBlock renders the project's enforced engagement scope. Empty
// ScopeTargets (every benchmark/control-plane run) produces no section, so the
// overview stays byte-identical there.
func (p *Planner) authorizedScopeBlock() string {
	if len(p.cfg.ScopeTargets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nAUTHORIZED SCOPE (enforced by the project; intents outside it are refused):\n")
	b.WriteString("Allowed targets: ")
	for i, t := range p.cfg.ScopeTargets {
		if i >= authorizedScopeLimit {
			b.WriteString(", …")
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(promptrender.Cell(t, 120))
	}
	b.WriteString("\n")
	if len(p.cfg.ScopeDeny) > 0 {
		b.WriteString("Denied targets (override the allow list): ")
		for i, t := range p.cfg.ScopeDeny {
			if i >= authorizedScopeLimit {
				b.WriteString(", …")
				break
			}
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(promptrender.Cell(t, 120))
		}
		b.WriteString("\n")
	}
	if len(p.cfg.ScopeAllowPorts) > 0 {
		ports := make([]string, 0, len(p.cfg.ScopeAllowPorts))
		for _, port := range p.cfg.ScopeAllowPorts {
			ports = append(ports, strconv.Itoa(port))
		}
		fmt.Fprintf(&b, "Allowed ports: %s\n", strings.Join(ports, ", "))
	}
	b.WriteString("Use target_kind=scope when the next step is authorized by a scope entry (domain, CIDR, or *.domain wildcard); web/host intents still need a concrete target inside this scope.\n")
	return b.String()
}

// stalledWorkerLimit bounds the stalled-worker block. The planner prompt grows
// with the run; this section must stay a scan, not a transcript.
const stalledWorkerLimit = 10

// currentStalls snapshots the bounded stall projection at round start.
func (p *Planner) currentStalls() []StalledWorker {
	if p == nil || p.cfg.StallReport == nil {
		return nil
	}
	workers := p.cfg.StallReport()
	if len(workers) > stalledWorkerLimit {
		workers = workers[:stalledWorkerLimit]
	}
	return append([]StalledWorker(nil), workers...)
}

// noteCorrected records that the model already handled one stalled unit this
// round, so the stall guard does not double-steer it.
func (p *Planner) noteCorrected(unitID string) {
	if p == nil || strings.TrimSpace(unitID) == "" {
		return
	}
	if p.roundCorrected == nil {
		p.roundCorrected = make(map[string]bool)
	}
	p.roundCorrected[strings.TrimSpace(unitID)] = true
	delete(p.stallMisses, strings.TrimSpace(unitID))
}

// applyStallGuard is the bounded fallback for a planner that repeatedly ignores
// an engine-visible stall. It only acts after two consecutive missed rounds,
// issues one steer (never a kill), and records the action in the round budget.
func (p *Planner) applyStallGuard(stalls []StalledWorker) {
	if p == nil || len(stalls) == 0 || p.cfg.Steer == nil {
		return
	}
	if p.stallMisses == nil {
		p.stallMisses = make(map[string]int)
	}
	present := make(map[string]bool, len(stalls))
	for _, worker := range stalls {
		unitID := strings.TrimSpace(worker.UnitID)
		if unitID == "" {
			continue
		}
		present[unitID] = true
		if p.roundCorrected[unitID] {
			delete(p.stallMisses, unitID)
			continue
		}
		p.stallMisses[unitID]++
		if p.stallMisses[unitID] < 2 {
			continue
		}
		message := fmt.Sprintf(
			"Stall guard: no information-layer progress for %s in lane %s. Stop repeating the same calls; save your current notes and evidence, then either change approach or return a short summary so this intent can be settled.",
			worker.IdleFor.Round(time.Second), worker.Lane,
		)
		if err := p.cfg.Steer(unitID, message); err != nil {
			log.Printf("[planner] %s: stall guard steer %s failed: %v", p.cfg.ScanID, unitID, err)
			continue
		}
		log.Printf("[planner] %s: stall guard steered %s after %d round(s) without a corrective action", p.cfg.ScanID, unitID, p.stallMisses[unitID])
		p.roundActions.Add(1)
		delete(p.stallMisses, unitID)
	}
	for unitID := range p.stallMisses {
		if !present[unitID] {
			delete(p.stallMisses, unitID)
		}
	}
}

// stalledWorkersBlock renders the engine's bounded stall observations. It is a
// projection only: the planner still decides whether to steer, kill, or leave
// the worker alone.
func (p *Planner) stalledWorkersBlock() string {
	if p.cfg.StallReport == nil {
		return ""
	}
	workers := p.cfg.StallReport()
	if len(workers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nSTALLED WORKERS (no information-layer progress; prefer steer_work, kill_work only with a valid reason_code):\n")
	for i, w := range workers {
		if i >= stalledWorkerLimit {
			b.WriteString("- … (truncated)\n")
			break
		}
		state := "idle"
		if w.ActiveTool {
			state = "tool-running"
		}
		fmt.Fprintf(&b, "- unit=%s agent=%s lane=%s idle=%s %s",
			promptrender.Cell(w.UnitID, 80), promptrender.Cell(w.AgentID, 60),
			promptrender.Cell(w.Lane, 30), promptrender.Cell(w.IdleFor.Round(time.Second).String(), 24), state)
		if !w.LastProgress.IsZero() {
			fmt.Fprintf(&b, " last_progress=%s ago",
				promptrender.Cell(time.Since(w.LastProgress).Round(time.Second).String(), 24))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// untrusted wraps one untrusted block and records injection markers in the
// scan journal (purpose=injection_suspected). It never executes or propagates
// instructions found inside the text.
// platformFinished reports the engine marker written when the platform task
// ends. It no longer stops the run: apply() only refuses to add new /start or
// /submit control-plane intents, so close/read/state and execution lanes keep
// working until the planner decides to finish.
func (p *Planner) platformFinished() bool {
	// Engine marker, written by a hook into scanctx.NoteStore. Deliberately
	// not part of the tool-notes union: a model-written note must never be
	// able to stop every worker by claiming the platform finished.
	if p.cfg.ScanCtx == nil || p.cfg.ScanCtx.Notes == nil {
		return false
	}
	v, ok := p.cfg.ScanCtx.Notes.Get("platform_finished")
	return ok && strings.TrimSpace(v) != ""
}

func (p *Planner) untrusted(label, text string, max int) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if p.cfg.ScanCtx != nil && p.cfg.ScanCtx.Ledger != nil {
		if hits := promptrender.ScanForInjection(text); len(hits) > 0 {
			if err := p.cfg.ScanCtx.Ledger.AppendInjectionObservation(label, text); err != nil {
				log.Printf("[planner] %s: record injection observation: %v", p.cfg.ScanID, err)
			}
			label += "; injection markers=" + strings.Join(hits, ",")
		}
	}
	return promptrender.Untrusted(label, text, max)
}

// priorWorkBlock surfaces a handful of L0 index entries similar to the task
// intent. Detail is fetched explicitly with read_info_item, not inlined.
func (p *Planner) priorWorkBlock() string {
	if p.cfg.ScanCtx == nil || p.cfg.ScanCtx.Ledger == nil {
		return ""
	}
	all := p.cfg.ScanCtx.Ledger.All()
	docs := make([]infoitem.Doc, 0, len(all)*2)
	for i := range all {
		h := &all[i]
		if strings.TrimSpace(h.ID) == "" {
			continue
		}
		title := strings.TrimSpace(h.VulnClass)
		if h.Endpoint != "" {
			title += " @ " + h.Endpoint
		}
		docs = append(docs, infoitem.Doc{
			ID: h.ID, Kind: "belief", Title: title,
			Summary: promptrender.Cell(h.NextAction, 160),
		})
		for j := range h.Evidence {
			ev := &h.Evidence[j]
			if strings.TrimSpace(ev.ID) == "" {
				continue
			}
			docs = append(docs, infoitem.Doc{
				ID: ev.ID, Kind: "evidence", Title: promptrender.Cell(infoItemFirstLine(ev.Summary), 120),
				Summary: promptrender.Cell(ev.Summary, 160), HasDetail: ev.Summary != "" || ev.Request != "" || ev.Response != "",
			})
		}
	}
	brief := p.briefSnapshot()
	instr := brief.UserInstruction
	if strings.TrimSpace(instr) == "" {
		instr = brief.Instruction
	}
	query := strings.TrimSpace(brief.Goal + " " + instr)
	hits := infoitem.Rank(query, docs, 5)
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nPrior work (index only; fetch detail with read_info_item id):\n")
	for _, hit := range hits {
		fmt.Fprintf(&b, "- [%s] %s (%.2f): %s — %s\n", promptrender.Cell(hit.Kind, 20), promptrender.Cell(hit.ID, 40),
			hit.Score, promptrender.Cell(hit.Title, 120), promptrender.Cell(hit.Summary, 160))
	}
	return b.String()
}

// IG2 — information-space lighting for the planner.

const (
	// infoSpaceRegionLimit / infoSpaceContextLimit bound each half of the
	// block; together they cap the whole block at infoSpaceBlockMaxLines
	// rendered item lines (+2 headers), keeping the prompt growth bounded.
	infoSpaceRegionLimit   = 12
	infoSpaceContextLimit  = 8
	infoSpaceBlockMaxLines = infoSpaceRegionLimit + infoSpaceContextLimit
)

// infoSpaceReader resolves the read-only information space: the configured
// override first, then the graph authority when it implements the reader, and
// nil when neither does (lexical fallback path).
func (p *Planner) infoSpaceReader() infospace.Reader {
	if p.cfg.InfoSpace != nil {
		return p.cfg.InfoSpace
	}
	if backend := graph.Backend(); backend != nil {
		if reader, ok := backend.(infospace.Reader); ok {
			return reader
		}
	}
	return nil
}

// infoSpaceSubject returns the run target only when it names something the
// scanner can actually dispatch to. A placeholder, symbolic task label or
// control-plane host is not an information-space subject.
func (p *Planner) infoSpaceSubject() string {
	target := strings.TrimSpace(p.cfg.Target)
	if target == "" || isPlaceholderTarget(target) || isSymbolicTaskTarget(target) {
		return ""
	}
	if isControlPlaneTarget(target, p.cfg.ControlPlaneHosts) {
		return ""
	}
	return target
}

// infoSpacePurpose returns the one vuln class the objective criteria name
// explicitly. Zero or several classes means "no explicit purpose" — the
// planner must never guess one.
func (p *Planner) infoSpacePurpose() string {
	brief := p.briefSnapshot()
	distinct := make(map[string]bool, len(brief.ObjectiveCriteria))
	for _, c := range brief.ObjectiveCriteria {
		if class := strings.ToLower(strings.TrimSpace(c.VulnClass)); class != "" {
			distinct[class] = true
		}
	}
	if len(distinct) != 1 {
		return ""
	}
	for class := range distinct {
		return class
	}
	return ""
}

// infoSpaceBlock renders the bounded region/context lighting. It returns ""
// when no reader is available, when the read fails (PG down or views not
// migrated) or when the space holds nothing for this run, so the caller keeps
// the lexical prior-work block byte-for-byte.
func (p *Planner) infoSpaceBlock() string {
	reader := p.infoSpaceReader()
	if reader == nil || strings.TrimSpace(p.cfg.ScanID) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()

	subject := p.infoSpaceSubject()
	region, err := reader.Region(ctx, infospace.RegionQuery{
		RunIDs:  []string{p.cfg.ScanID},
		Layer:   infospace.LayerAggregate,
		Kinds:   plannerInfoSpaceKinds,
		Subject: subject,
		Purpose: p.infoSpacePurpose(),
		Limit:   infoSpaceRegionLimit,
	})
	if err != nil {
		return ""
	}
	regionItems := boundedInfoSpaceItems(region.Items, infoSpaceRegionLimit)

	var contextItems []infospace.Item
	if subject != "" {
		res, cerr := reader.ContextSlice(ctx, infospace.ContextQuery{
			RunID:   p.cfg.ScanID,
			Subject: subject,
			Layer:   infospace.LayerAggregate,
			Limit:   infoSpaceContextLimit,
		})
		if cerr == nil {
			contextItems = boundedInfoSpaceItems(res.Items, infoSpaceContextLimit)
		}
	}
	if len(regionItems) == 0 && len(contextItems) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\nInformation space (read-only lighting; coordinates only — fetch detail with read_info_item):\n")
	if len(regionItems) > 0 {
		fmt.Fprintf(&b, "Region (this run, up to %d):\n", infoSpaceRegionLimit)
		for _, item := range regionItems {
			b.WriteString(infoSpaceItemLine(item))
		}
	}
	if region.Incomplete && region.GapReason != "" {
		fmt.Fprintf(&b, "Region incomplete: %s (treat as partial, not empty)\n", promptrender.Cell(region.GapReason, 120))
	}
	if len(contextItems) > 0 {
		fmt.Fprintf(&b, "Context (subject=%s, up to %d):\n", promptrender.Cell(subject, 120), infoSpaceContextLimit)
		for _, item := range contextItems {
			b.WriteString(infoSpaceItemLine(item))
		}
	}
	return b.String()
}

// plannerInfoSpaceKinds keeps the region block on the information layer that
// is not already rendered as "Existing intents": intent lifecycle stays a
// control surface, not duplicated prompt text.
var plannerInfoSpaceKinds = []string{
	string(graph.KindGoal), string(graph.KindBelief), string(graph.KindFact),
	string(graph.KindFinding), string(graph.KindHint), string(graph.KindDigest),
}

// boundedInfoSpaceItems defensively re-sorts and truncates the reader result.
// Ordering is the reader contract (observed_at DESC, info_id ASC); re-applying
// it makes the rendered block deterministic even against a noisy fake or a
// backend drift, and the truncation keeps the block bounded.
func boundedInfoSpaceItems(items []infospace.Item, limit int) []infospace.Item {
	if limit <= 0 {
		return nil
	}
	out := make([]infospace.Item, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ObservedAt.Equal(out[j].ObservedAt) {
			return out[i].ObservedAt.After(out[j].ObservedAt)
		}
		return out[i].InfoID < out[j].InfoID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// infoSpaceItemLine renders one item as IDs and coordinates only. Payload (the
// layer-native body/document) is never inlined.
func infoSpaceItemLine(item infospace.Item) string {
	layer := string(item.Layer)
	if layer == "" {
		layer = string(infospace.LayerAggregate)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s/%s/%s] %s", promptrender.Cell(layer, 12),
		promptrender.Cell(item.Kind, 20), promptrender.Cell(item.State, 20),
		promptrender.Cell(item.InfoID, 60))
	if item.UnitKey != "" {
		fmt.Fprintf(&b, " unit=%s", promptrender.Cell(item.UnitKey, 60))
	}
	if item.Subject != "" {
		fmt.Fprintf(&b, " subject=%s", promptrender.Cell(item.Subject, 120))
	}
	if item.Purpose != "" {
		fmt.Fprintf(&b, " purpose=%s", promptrender.Cell(item.Purpose, 40))
	}
	if item.Nature != "" {
		fmt.Fprintf(&b, " nature=%s", promptrender.Cell(item.Nature, 20))
	}
	b.WriteString("\n")
	return b.String()
}

// coverageSnapshot is the shared deterministic read behind the coverage-gap
// block (IG2 baseline) and the gap-candidate block (IG5). It performs the one
// PlanTasks read per overview so both renderers see the same snapshot.
type coverageSnapshot struct {
	probed      map[string][]string
	verified    map[string][]string
	queued      []scanctx.Hypothesis
	openIntents map[string]bool
}

func (p *Planner) readCoverageSnapshot() coverageSnapshot {
	snap := coverageSnapshot{openIntents: map[string]bool{}}
	if p.cfg.ScanCtx != nil && p.cfg.ScanCtx.Coverage != nil {
		snap.probed, snap.verified = p.cfg.ScanCtx.Coverage.Snapshot()
	}
	if p.cfg.ScanCtx != nil && p.cfg.ScanCtx.Ledger != nil {
		for _, h := range p.cfg.ScanCtx.Ledger.All() {
			if h.Status == scanctx.HypothesisQueued {
				snap.queued = append(snap.queued, h)
			}
		}
	}
	if p.cfg.Repo == nil {
		return snap
	}
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()
	if tasks, err := p.cfg.Repo.PlanTasks(ctx, p.cfg.ScanID); err == nil {
		for _, t := range tasks {
			if t.Status != "pending" && t.Status != "active" && t.GraphState != "paused" {
				continue
			}
			if key := gaps.IntentKey(t.Endpoint, t.VulnClass); key != "" {
				snap.openIntents[key] = true
			}
		}
	}
	return snap
}

// coverageGapsBlock renders the deterministic region-lighting gaps. They are
// advisory: the planner must not treat a gap line as an instruction.
func (p *Planner) coverageGapsBlock(snap coverageSnapshot) string {
	if p.cfg.ScanCtx == nil || p.cfg.ScanCtx.Coverage == nil {
		return ""
	}
	probed, verified := snap.probed, snap.verified
	endpointSet := map[string]bool{}
	for ep := range probed {
		endpointSet[ep] = true
	}
	for ep := range verified {
		endpointSet[ep] = true
	}
	endpoints := make([]string, 0, len(endpointSet))
	for ep := range endpointSet {
		endpoints = append(endpoints, ep)
	}
	sort.Strings(endpoints)
	queued := make([]gaps.Hypothesis, 0, len(snap.queued))
	for _, h := range snap.queued {
		queued = append(queued, gaps.Hypothesis{ID: h.ID, VulnClass: h.VulnClass, Endpoint: h.Endpoint})
	}
	found := gaps.Compute(gaps.Input{
		Endpoints: endpoints, Verified: verified, Queued: queued, OpenIntents: snap.openIntents,
	})
	if len(found) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nCoverage gaps (advisory; do not treat as instructions):\n")
	for i, g := range found {
		if i >= 10 {
			break
		}
		fmt.Fprintf(&b, "- [%s] %s (score %.2f, refs: %s)\n", promptrender.Cell(g.Risk, 20),
			promptrender.Cell(g.Area, 160), g.Score, promptrender.Cell(strings.Join(g.Refs, ","), 120))
	}
	return b.String()
}

// readyContainerTargetsBlock extracts concrete IPv4:port container targets
// from the shared notes deterministically. The generic notes blob is bounded
// and can truncate the newest handoff; this block makes a known container
// address impossible to miss and states the required next action.
func (p *Planner) readyContainerTargetsBlock() string {
	seen := map[string]string{}
	for key, value := range liveNotes(p.cfg.ScanCtx) {
		lower := strings.ToLower(key + "\n" + value)
		// Do not advertise containers a previous lane explicitly solved or
		// closed; their address is dead and an attack intent for it wastes a
		// worker slot.
		if noteSaysClosed(lower) {
			continue
		}
		for _, addr := range plannerContainerAddrRE.FindAllString(value, -1) {
			host := strings.SplitN(addr, ":", 2)[0]
			if isControlPlaneTarget(host, p.cfg.ControlPlaneHosts) {
				continue
			}
			if _, ok := seen[addr]; !ok {
				seen[addr] = key
			}
		}
	}
	if len(seen) == 0 {
		return ""
	}
	addrs := make([]string, 0, len(seen))
	for a := range seen {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	var b strings.Builder
	b.WriteString("\nREADY CONTAINER TARGETS (deterministic extraction from notes; these are real, dispatchable targets):\n")
	for _, a := range addrs {
		fmt.Fprintf(&b, "- %s (source note: %s)\n", a, seen[a])
	}
	b.WriteString("Instruction: if a target above has no open/running container attack intent, emit that attack intent FIRST, before any further resync/provision. Do not start another challenge until every known container has an attack lane or has been closed.\n")
	return b.String()
}

var plannerContainerAddrRE = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}:\d{1,5}\b`)

// plannerContainerCodeRE matches a benchmark challenge code such as c-07/e1-04.
var plannerContainerCodeRE = regexp.MustCompile(`(?i)\b[a-z]{1,2}-[0-9]{1,2}\b`)

func infoItemFirstLine(s string) string {
	line := strings.TrimSpace(s)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line
}

func clampTimebox(seconds int) int {
	if seconds <= 0 {
		return 0
	}
	if seconds < 60 {
		return 60
	}
	if seconds > graph.MaxContractTimeboxSeconds {
		return graph.MaxContractTimeboxSeconds
	}
	return seconds
}

// unsettledDependency returns the first dependency that blocks an intent from
// being claimed right now: a dependency created in this same planning round,
// or one whose authoritative state is not terminal. Unknown ids (beliefs or
// tasks outside the plan projection) are not treated as blocking because no
// lineage edge is created for them anyway.
func unsettledDependency(deps []string, byID map[string]graph.PlanTask, created map[string]bool) string {
	for _, raw := range deps {
		dep := strings.TrimSpace(raw)
		if dep == "" {
			continue
		}
		if created[dep] {
			return dep
		}
		prev, ok := byID[dep]
		if !ok {
			continue
		}
		state := graph.State(strings.TrimSpace(prev.GraphState))
		if state == "" || !graph.TerminalState(graph.KindIntent, state) {
			return dep
		}
	}
	return ""
}

// IG4 — deterministic intent priority with an optional VOI term.

// intentPhaseBase maps a methodology phase onto the deterministic baseline in
// [0,1]: phase 1 -> 1.0, phase 22 -> 0.0. It preserves the historical
// `1000 - phase*10` ordering (earlier phase ranks first) while normalizing the
// scale so a VOI estimate can be blended in without changing phase order.
func intentPhaseBase(phase int) float64 {
	if phase < 1 {
		phase = 1
	}
	if phase > methodology.PhaseCount {
		phase = methodology.PhaseCount
	}
	return 1 - float64(phase-1)/float64(methodology.PhaseCount-1)
}

// clampIntentVOIWeight bounds the VOI weight to the same [0, 0.30] ceiling the
// hypothesis claim ordering uses (scanctx.MaxVOIWeight). NaN/Inf/negative maps
// to 0, i.e. no VOI term at all.
func clampIntentVOIWeight(weight float64) float64 {
	if math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
		return 0
	}
	if weight > scanctx.MaxVOIWeight {
		return scanctx.MaxVOIWeight
	}
	return weight
}

// intentPriority is the pure, deterministic intent ranking value:
//
//	base  = 1 - (phase-1)/21                      // phase order preserved
//	score = (1-w)*base + w*voi                    // w clamped to [0,0.30]
//	priority = int(score*1000)
//
// Same (phase, voi, weight) always yields the same priority: no clock, no
// randomness. With w = 0 the historical phase ordering is preserved exactly
// (strictly decreasing in phase).
func intentPriority(phase int, voi, weight float64) int {
	w := clampIntentVOIWeight(weight)
	if math.IsNaN(voi) || math.IsInf(voi, 0) || voi < 0 {
		voi = 0
	}
	if voi > 1 {
		voi = 1
	}
	score := (1-w)*intentPhaseBase(phase) + w*voi
	return int(score * 1000)
}

// intentVOI aggregates the ledger VOI behind an intent's coordinate
// (endpoint/target + vuln_class).
//
// Aggregation rule (decided for IG4, see docs/INFO_AS_GRAPH_SPEC.md §11.5):
// the MAX over matching hypotheses that carry an *estimated* VOI. Rationale:
// the VOI of one decision is the value of the single most informative matching
// belief — a mean would dilute a high-value hypothesis with unrelated
// siblings, an evidence-weighted sum would need confidence×count normalization
// that the ledger does not define. Max is order-independent and cannot exceed
// the heaviest belief. Missing/unestimated VOI is 0 (a real zero is
// `VOIEstimated` with VOI 0, which aggregates to 0 as well).
func intentVOI(hypotheses []scanctx.Hypothesis, target, endpoint, vulnClass string) float64 {
	ep := strings.ToLower(strings.TrimSpace(endpoint))
	tgt := strings.ToLower(strings.TrimSpace(target))
	class := strings.ToLower(strings.TrimSpace(vulnClass))
	if ep == "" && tgt == "" {
		return 0
	}
	best := 0.0
	for i := range hypotheses {
		h := &hypotheses[i]
		if !h.VOIEstimated || h.VOI <= 0 {
			continue
		}
		if class != "" && strings.ToLower(strings.TrimSpace(h.VulnClass)) != class {
			continue
		}
		matched := ep != "" && strings.EqualFold(strings.TrimSpace(h.Endpoint), ep)
		if !matched && tgt != "" {
			matched = strings.EqualFold(strings.TrimSpace(h.Target), tgt)
		}
		if !matched {
			continue
		}
		voi := h.VOI
		if voi > 1 {
			voi = 1
		}
		if voi > best {
			best = voi
		}
	}
	return best
}

func (p *Planner) apply(out plannerOutput) (added, retired int) {
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
	defer cancel()
	existing, err := p.cfg.Repo.PlanTasks(ctx, p.cfg.ScanID)
	if err != nil {
		return 0, 0
	}
	byID := make(map[string]graph.PlanTask, len(existing))
	seen := make(map[string]bool, len(existing))
	activeEndpoints := make(map[string]bool, len(existing))
	recentEndpoints := make(map[string]time.Time, len(existing))
	for _, t := range existing {
		byID[t.TaskID] = t
		dedupeTarget := strings.TrimSpace(t.Target)
		if dedupeTarget == "" {
			dedupeTarget = t.Endpoint
		}
		key := intentKey(dedupeTarget, t.VulnClass, t.Title)
		if key != "" {
			seen[key] = true
		}
		if ep := strings.ToLower(strings.TrimSpace(t.Endpoint)); ep != "" && !t.CreatedAt.IsZero() {
			if prev, ok := recentEndpoints[ep]; !ok || t.CreatedAt.After(prev) {
				recentEndpoints[ep] = t.CreatedAt
			}
		}
		if t.Status == "pending" || t.Status == "active" {
			if ep := strings.ToLower(strings.TrimSpace(t.Endpoint)); ep != "" {
				activeEndpoints[ep] = true
			}
		}
	}
	newIDs := make(map[string]int64, len(out.Intents))
	generated := make([]string, len(out.Intents))
	createdThisBatch := make(map[string]bool, len(out.Intents))
	// platform_finished no longer ends the scan: it only closes the submit/start
	// half of the control-plane lane, so read/state/close orchestration (and
	// every execution lane) keeps working.
	platformDone := p.platformFinished()
	// IG4: the VOI term is read once per round from the ledger snapshot. When
	// the weight is 0 (the default and the only value legacy scans carry) no
	// hypothesis is read and the priority is the pure phase baseline.
	voiWeight := 0.0
	var voiHyps []scanctx.Hypothesis
	if p.cfg.ScanCtx != nil && p.cfg.ScanCtx.Ledger != nil {
		voiWeight = clampIntentVOIWeight(p.cfg.ScanCtx.Ledger.VOIWeight())
		if voiWeight > 0 {
			voiHyps = p.cfg.ScanCtx.Ledger.All()
		}
	}
	for i, in := range out.Intents {
		if added >= p.maxIntents() {
			break
		}
		title := strings.TrimSpace(in.Title)
		if title == "" {
			continue
		}
		target := strings.TrimSpace(in.Target)
		if target == "" {
			target = strings.TrimSpace(in.Endpoint)
		}
		if target == "" {
			continue // no target = not dispatchable; the task intent must name one
		}
		if isPlaceholderTarget(target) {
			continue // "CONTAINER_ADDR"/"pending-..." is not a dispatchable target
		}
		kind := strings.ToLower(strings.TrimSpace(in.TargetKind))
		if kind == "" {
			if isControlPlaneTarget(target, p.cfg.ControlPlaneHosts) {
				continue // never infer a scan intent against the control plane
			}
			kind = "web"
		}
		// A task intent that names a real host is an execution lane wearing the
		// wrong label; relabel it so it gets the worker surface and per-target
		// accounting. A symbolic "task"/"task:queue" stays analysis-only.
		if kind == "task" && !isSymbolicTaskTarget(target) &&
			(strings.Contains(target, ".") || strings.Contains(target, ":")) {
			kind = "web"
		}
		// A scope intent is only meaningful when the target really is one of the
		// authorized project scope entries (or inside one). Without an enforced
		// scope this kind is never accepted, so benchmark behavior is unchanged.
		if kind == "scope" &&
			!scopeTargetAuthorized(target, p.cfg.ScopeTargets, p.cfg.ScopeDeny, p.cfg.ScopeAllowPorts) {
			continue
		}
		controlLaneKey := ""
		if kind == "control_plane_api" {
			if platformDone && controlPlaneIntentStartsOrSubmits(in) {
				// The platform task already ended by the engine's marker: no new
				// start/submit work. Close/read/state intents stay allowed so the
				// run can wind down cleanly.
				continue
			}
			if controlPlaneIntentAttacks(in) {
				// Lane separation: the API lane orchestrates; the engine
				// materializes a container attack intent from the published
				// container_addr. Never merge the attack into this lane.
				continue
			}
			if !controlPlaneAPITargetAllowed(target, p.cfg.ControlPlaneHosts) {
				continue // API lane must name the configured platform host, not a health IP
			}
			controlLaneKey = strings.ToLower(strings.TrimSpace(in.Endpoint))
			if controlLaneKey == "" {
				controlLaneKey = strings.ToLower(strings.TrimSpace(target))
			}
			if activeEndpoints[controlLaneKey] {
				continue // an equivalent orchestration intent is already in flight
			}
			if last, ok := recentEndpoints[controlLaneKey]; ok && time.Since(last) < 5*time.Minute {
				// Throttle only read/state endpoints; /start, /submit and
				// /close must stay available for parallel challenge lanes.
				if !strings.Contains(controlLaneKey, "/start") &&
					!strings.Contains(controlLaneKey, "/submit") &&
					!strings.Contains(controlLaneKey, "/close") {
					continue
				}
			}
		} else if isControlPlaneTarget(target, p.cfg.ControlPlaneHosts) {
			continue
		}
		if intentNamesControlPlaneForScan(in, p.cfg.ControlPlaneHosts) {
			continue
		}
		// Do not emit work that cannot be claimed yet. An intent whose
		// dependency is still running (or was created earlier in this same
		// round) would sit open-but-parked: it occupies a worker row, produces
		// no stream, and hides the fact that the pool has idle capacity. Wait
		// for the parent to settle; OnSettled wakes the planner for the next
		// step. This is the code-level enforcement of the concurrency doctrine
		// in the prompt (4zreco only ever dispatches ready steps).
		if dep := unsettledDependency(in.DependsOn, byID, createdThisBatch); dep != "" {
			log.Printf("[planner] %s: deferring %q — dependency %s has not settled", p.cfg.ScanID, title, dep)
			continue
		}
		key := intentKey(target, in.VulnClass, title)
		if key != "" && seen[key] {
			continue
		}
		taskID := strings.TrimSpace(in.TaskID)
		if taskID == "" {
			taskID = fmt.Sprintf("llm-%d-%d", time.Now().UnixNano(), added)
		}
		if _, ok := byID[taskID]; ok {
			continue
		}
		generated[i] = taskID
		phase := int(in.Phase)
		if phase < 1 {
			phase = 1
		}
		if phase > 22 {
			phase = 22
		}
		payload, _ := json.Marshal(map[string]any{
			"task_id":       taskID,
			"title":         title,
			"phase":         phase,
			"vuln_class":    strings.ToLower(strings.TrimSpace(in.VulnClass)),
			"endpoint":      strings.TrimSpace(in.Endpoint),
			"target":        target,
			"target_kind":   kind,
			"origin":        "llm",
			"depends_on":    in.DependsOn,
			"notes":         strings.TrimSpace(in.Notes),
			"objective_ref": strings.TrimSpace(in.ObjectiveRef),
			"contract": map[string]any{
				"version":   1,
				"timebox_s": clampTimebox(int(in.TimeboxS)),
				// A planner-created intent is not operator-confirmed work; the
				// confirmation gate stays for the main-chat proposal path.
				"requires_confirmation": false,
			},
		})
		voi := 0.0
		if voiWeight > 0 {
			voi = intentVOI(voiHyps, target, strings.TrimSpace(in.Endpoint), in.VulnClass)
		}
		node, err := p.cfg.Repo.UpsertNode(ctx, graph.NodeInput{
			ScanID: p.cfg.ScanID, Kind: graph.KindIntent, UnitKey: taskID,
			State: graph.StateOpen, Priority: intentPriority(phase, voi, voiWeight), Origin: "llm",
			Payload: string(payload),
		})
		if err != nil || node == nil {
			continue
		}
		newIDs[taskID] = node.ID
		createdThisBatch[taskID] = true
		if key != "" {
			seen[key] = true
		}
		if controlLaneKey != "" {
			activeEndpoints[controlLaneKey] = true
		}
		added++
	}
	// Dependency edges need every node id resolved first.
	for i, in := range out.Intents {
		taskID := generated[i]
		if taskID == "" {
			taskID = strings.TrimSpace(in.TaskID)
		}
		dst, ok := newIDs[taskID]
		if !ok {
			continue
		}
		deps := make([]int64, 0, len(in.DependsOn))
		for _, dep := range in.DependsOn {
			if prev, ok := byID[dep]; ok {
				deps = append(deps, prev.NodeID)
			} else if id, ok := newIDs[dep]; ok {
				deps = append(deps, id)
			}
		}
		_ = p.cfg.Repo.ReplaceDependencyEdges(ctx, p.cfg.ScanID, dst, deps)
	}
	// Retire only open intents; running work is settled by its worker.
	for _, taskID := range out.Retire {
		t, ok := byID[strings.TrimSpace(taskID)]
		if !ok || t.GraphState != string(graph.StateOpen) {
			continue
		}
		if ok, _ := p.cfg.Repo.Transition(ctx, p.cfg.ScanID, t.NodeID, graph.KindIntent,
			graph.StateOpen, graph.StateDeleted); ok {
			retired++
		}
	}
	return added, retired
}

// callModel makes one bounded planner LLM call with the planner tool surface.
func (p *Planner) callModel(msgs []llm.Message) (string, []llm.ToolCall, error) {
	callCtx, cancel := context.WithTimeout(p.ctx, 3*time.Minute)
	defer cancel()
	type result struct {
		content string
		calls   []llm.ToolCall
		err     error
	}
	done := make(chan result, 1)
	go func() {
		content, calls, _, err := p.cfg.Client.ChatWithUsageToolCalls(msgs, plannerToolDefinitions())
		done <- result{content, calls, err}
	}()
	select {
	case r := <-done:
		return r.content, r.calls, r.err
	case <-callCtx.Done():
		return "", nil, fmt.Errorf("round timed out")
	}
}

func plannerAssistantMessage(content string, calls []llm.ToolCall) llm.Message {
	msg := llm.Message{Role: "assistant", Content: content}
	for _, call := range calls {
		args := call.RawArguments
		if strings.TrimSpace(args) == "" {
			b, _ := json.Marshal(call.Args)
			args = string(b)
		}
		msg.ToolCalls = append(msg.ToolCalls, llm.MessageToolCall{
			ID:   call.ID,
			Type: "function",
			Function: llm.MessageToolFunction{
				Name:      call.Name,
				Arguments: args,
			},
		})
	}
	return msg
}

// plannerToolDefinitions is the read/write surface offered to the planner. All
// writes still pass through apply(), so caps, dedupe, control-plane filtering
// and graph CAS semantics are unchanged.
func plannerToolDefinitions() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{Name: "graph_overview", Description: "Read the current counters and existing intents."},
		{Name: "read_plan", Description: "List the existing plan intents."},
		{Name: "read_info_item", Description: "Read one belief/evidence summary by ID.", Parameters: []llm.ToolParameter{
			{Name: "id", Description: "Hypothesis or evidence ID", Required: true},
		}},
		{Name: "add_intent", Description: "Add one concrete intent (subject to the same caps/dedupe as the final JSON).", Parameters: []llm.ToolParameter{
			{Name: "title", Required: true}, {Name: "target", Required: true},
			{Name: "target_kind", Description: "control_plane_api|container|web|task"},
			{Name: "vuln_class"}, {Name: "phase"}, {Name: "endpoint"},
			{Name: "task_id"}, {Name: "depends_on", Description: "Comma-separated task ids"},
			{Name: "notes"}, {Name: "objective_ref"}, {Name: "timebox_s"},
		}},
		{Name: "retire_intent", Description: "Retire one OPEN intent by task id.", Parameters: []llm.ToolParameter{
			{Name: "task_id", Required: true},
		}},
		{Name: "steer_work", Description: "Send a course correction to one running worker (unit_id). Prefer steer before kill.", Parameters: []llm.ToolParameter{
			{Name: "unit_id", Required: true}, {Name: "message", Description: "What to change and why", Required: true},
		}},
		{Name: "kill_work", Description: "End one running worker (unit_id) with a required reason_code: stagnant|repeated_failure|lane_violation|scope_violation|duplicate|superseded.", Parameters: []llm.ToolParameter{
			{Name: "unit_id", Required: true}, {Name: "reason_code", Required: true},
			{Name: "reason", Description: "Evidence / short explanation"},
		}},
	}
}

// execPlannerTool runs one planner tool call. addedSoFar bounds mid-loop writes
// to the same per-round budget apply() enforces.
func (p *Planner) execPlannerTool(call llm.ToolCall, addedSoFar int) (string, int, int) {
	switch call.Name {
	case "graph_overview":
		return p.overview(), 0, 0
	case "read_plan":
		return p.planList(), 0, 0
	case "read_info_item":
		return p.readInfoItem(call.Args["id"]), 0, 0
	case "add_intent":
		if addedSoFar >= p.maxIntents() {
			return fmt.Sprintf("add_intent rejected: this round already added the maximum of %d intents", p.maxIntents()), 0, 0
		}
		in := plannerIntent{
			TaskID:       strings.TrimSpace(call.Args["task_id"]),
			Title:        strings.TrimSpace(call.Args["title"]),
			VulnClass:    strings.TrimSpace(call.Args["vuln_class"]),
			Endpoint:     strings.TrimSpace(call.Args["endpoint"]),
			Target:       strings.TrimSpace(call.Args["target"]),
			TargetKind:   strings.TrimSpace(call.Args["target_kind"]),
			Notes:        strings.TrimSpace(call.Args["notes"]),
			ObjectiveRef: strings.TrimSpace(call.Args["objective_ref"]),
		}
		if n, err := strconv.Atoi(strings.TrimSpace(call.Args["phase"])); err == nil {
			in.Phase = plannerInt(n)
		}
		if n, err := strconv.Atoi(strings.TrimSpace(call.Args["timebox_s"])); err == nil {
			in.TimeboxS = plannerInt(n)
		}
		for _, dep := range strings.Split(call.Args["depends_on"], ",") {
			if dep = strings.TrimSpace(dep); dep != "" {
				in.DependsOn = append(in.DependsOn, dep)
			}
		}
		added, _ := p.apply(plannerOutput{Intents: []plannerIntent{in}})
		if added == 0 {
			return "add_intent rejected (duplicate, capped, or invalid target/control-plane)", 0, 0
		}
		return "intent added", added, 0
	case "retire_intent":
		_, retired := p.apply(plannerOutput{Retire: []string{strings.TrimSpace(call.Args["task_id"])}})
		if retired == 0 {
			return "retire_intent rejected (unknown or not open)", 0, 0
		}
		return "intent retired", 0, retired
	case "steer_work":
		unitID := strings.TrimSpace(call.Args["unit_id"])
		message := strings.TrimSpace(call.Args["message"])
		if unitID == "" || message == "" {
			return "steer_work rejected: unit_id and message are required", 0, 0
		}
		if p.cfg.Steer == nil {
			return "steer_work unavailable: no steer callback configured", 0, 0
		}
		if err := p.cfg.Steer(unitID, message); err != nil {
			return "steer_work failed: " + err.Error(), 0, 0
		}
		p.noteCorrected(unitID)
		p.roundActions.Add(1)
		return "worker steered", 0, 0
	case "kill_work":
		unitID := strings.TrimSpace(call.Args["unit_id"])
		reasonCode := strings.ToLower(strings.TrimSpace(call.Args["reason_code"]))
		reason := strings.TrimSpace(call.Args["reason"])
		if unitID == "" {
			return "kill_work rejected: unit_id is required", 0, 0
		}
		if !validKillReasonCode(reasonCode) {
			return "kill_work rejected: reason_code must be one of stagnant|repeated_failure|lane_violation|scope_violation|duplicate|superseded", 0, 0
		}
		if p.cfg.Kill == nil {
			return "kill_work unavailable: no kill callback configured", 0, 0
		}
		if err := p.cfg.Kill(unitID, reasonCode, reason); err != nil {
			return "kill_work failed: " + err.Error(), 0, 0
		}
		p.noteCorrected(unitID)
		p.roundActions.Add(1)
		return "worker killed", 0, 0
	default:
		return "unknown planner tool: " + call.Name, 0, 0
	}
}

// validKillReasonCode is the closed set the engine accepts for kill_work.
func validKillReasonCode(code string) bool {
	switch code {
	case "stagnant", "repeated_failure", "lane_violation", "scope_violation", "duplicate", "superseded":
		return true
	}
	return false
}

func (p *Planner) planList() string {
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()
	tasks, err := p.cfg.Repo.PlanTasks(ctx, p.cfg.ScanID)
	if err != nil {
		return "read_plan failed: " + err.Error()
	}
	var b strings.Builder
	for i, t := range tasks {
		if i >= 60 {
			b.WriteString("- … (truncated)\n")
			break
		}
		fmt.Fprintf(&b, "- %s [phase %d, %s, %s] %s\n", promptrender.Cell(t.TaskID, 80),
			t.Phase, promptrender.Cell(t.Status, 20), promptrender.Cell(t.VulnClass, 40),
			promptrender.Cell(t.Title, 120))
	}
	if b.Len() == 0 {
		return "read_plan: no intents"
	}
	return b.String()
}

func (p *Planner) readInfoItem(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "read_info_item: id is required"
	}
	if p.cfg.ScanCtx == nil || p.cfg.ScanCtx.Ledger == nil {
		return "read_info_item: ledger unavailable"
	}
	for _, h := range p.cfg.ScanCtx.Ledger.All() {
		if h.ID == id {
			return fmt.Sprintf("belief %s [%s] %s @ %s conf=%.2f\nnext: %s", h.ID, h.Status,
				h.VulnClass, h.Endpoint, h.Confidence, truncateText(h.NextAction, 600))
		}
		for _, ev := range h.Evidence {
			if ev.ID != id {
				continue
			}
			return fmt.Sprintf("evidence %s (hypothesis %s, kind=%s, nature=%s)\n%s\nrequest: %s\nresponse: %s",
				ev.ID, h.ID, ev.Kind, ev.Nature, truncateText(ev.Summary, 1200),
				truncateText(ev.Request, 1200), truncateText(ev.Response, 1200))
		}
	}
	return "read_info_item: not found in this run's ledger"
}

const plannerSystemPrompt = `You are the planning agent for an authorized task. Read the situation's three layers in this authority order:
1. GOAL (human-authored): what success means. It is the primary objective.
2. DESCRIPTION (human-authored): the procedure, hard constraints, priorities, limits, delivery and stop conditions. It outranks generic pentest methodology.
3. OBJECTIVE (derived analysis): auxiliary criteria/constraints. It never overrides GOAL or DESCRIPTION.
The target is context only and is NOT necessarily a scannable application.

You do NOT execute attack tools and you do NOT test anything yourself. Your job is to turn GOAL + DESCRIPTION into concrete intents the worker pool can execute. Before emitting an intent, ask: does it serve the GOAL, and does it respect every hard rule in the DESCRIPTION? If not, do not emit it.

Rules:
- Emit a small number of NEW intents (never more than the worker-pool limit). Zero is valid when the frontier already covers the next step or is waiting for an external step.
- Every intent MUST include a concrete target and target_kind:
  * web: a real web target from the task context.
  * task: an analysis/research step with NO network or shell access. It may read notes/ledger and write notes, nothing more. Never use it to attack, probe, or submit; those need an execution intent with a concrete target.
- NEVER invent placeholder targets such as "pending-container-address". If an address is not known yet, emit only the step that obtains it; plan the follow-up once the value exists in notes.
- Respect the DESCRIPTION's hard constraints: concurrency cap, per-intent timebox/cut-loss, priority order, delivery and the legal finish/stop conditions. Preserve them in how many intents you emit and what they do.
- Concurrency doctrine: intents are claimable work, not a backlog. Emit independent dimensions together (different targets, entry surfaces, or vulnerability classes) up to the pool limit. Emit a serial chain step by step: only the step whose inputs already exist. Never front-load a downstream step with depends_on to park it — a pending intent occupies a worker row, produces no stream, and makes the run look idle. Wait for the parent's fact/address, then emit the next step in a later round. depends_on is lineage for a dependency that already settled, never a promise to wait.
- Never submit, report, or fabricate evidence you did not obtain from the target.
- Do not duplicate an existing intent (same target + class + title) or an in-flight orchestration endpoint; do not re-create a retired/completed direction without material new evidence.
- For every intent set objective_ref to the GOAL or an OBJECTIVE criterion id it serves (for example "GOAL" or "C1"). If no criterion exists, use "GOAL".
- timebox_s is an optional per-intent execution budget in seconds (60-86400). Use a short box only for cheap checks; for exploitation lanes omit it or use a generous value — execution lanes get a floor regardless.
- steer_work(unit_id,message) corrects one running worker; kill_work(unit_id,reason_code,reason) ends one. reason_code must be one of stagnant|repeated_failure|lane_violation|scope_violation|duplicate|superseded. Prefer steer over kill, and never kill a worker that is still producing information-layer progress.
- Never claim a goal is met; never report a finding.
- Return STRICT JSON only, no markdown fences:
{"intents":[{"task_id":"optional-stable-id","title":"...","phase":6,"vuln_class":"sqli","endpoint":"/api/users","target":"https://app.example","target_kind":"web","depends_on":[],"notes":"why","objective_ref":"GOAL","timebox_s":1800}],"retire":["task-id"]}`

// plannerBenchmarkRules is appended only when the task configures a
// control-plane API (the benchmark/CTF harness). Engagement planning must not
// see platform/challenge vocabulary it cannot use.
const plannerBenchmarkRules = `

Benchmark control-plane rules (this task configures CONTROL-PLANE HOSTS):
- control_plane_api: an API call to the platform base named in the DESCRIPTION. Only the orchestrator lane executes it (HTTP + notes), never scan/exploit.
- container: a real challenge container target (an IP:port returned by the platform API).
- Every container attack MUST be its own intent with target_kind=container and the concrete container_addr (IP:port) as target. A symbolic target such as "task" or "task:queue" is an analysis lane and can never reach a container.
- NEVER invent placeholder targets such as CONTAINER_ADDR or pending-container-address. If the container address is not known yet, emit only the API step that obtains it; plan the container attack once the address exists in notes.
- If the situation lists READY CONTAINER TARGETS, emit a container attack intent for every target that has no open/running attack intent FIRST, before any resync/provision. Do not start another challenge while a known container has no attack lane.
- NEVER use a control-plane host as the target of a web/container/scan intent. NEVER use a health-check/VPN-probe address as the API base. Reconnaissance/exploitation of the control plane is forbidden.
- Bookkeeping (reconcile, score, close, submission) must not gate starting or attacking a container that is already live. Start/attack lanes and bookkeeping lanes are independent dimensions; keep them so.
- Never submit a probe, dummy or fabricated flag. Only a flag actually read from the target may be submitted; submit-close intents must depend on a real flag-producing attack intent.
- The control_plane_api lane orchestrates only (list/state/start/submit/close). Never put attack/exploit/flag-recovery work in that lane; the engine creates a container attack intent from the published container_addr.
- For control-plane API intents, use a short timebox (300-900); execution lane budgets follow the shared rule above.`

// plannerEngagementScopeRules is appended only for an engagement run with an
// enforced project scope. It introduces the scope target kind without exposing
// any platform/challenge vocabulary (those stay in the benchmark suffix).
const plannerEngagementScopeRules = `

Engagement scope rules (this task has an ENFORCED project scope):
- target_kind=scope: an execution intent whose target names a project scope entry (a domain, a CIDR such as 10.0.0.0/24, or a *.domain wildcard). Use it for work authorized by the scope itself; an out-of-scope target is refused and never dispatched.
- web/host intents still require a concrete target (a host or URL) that falls inside the authorized scope.
- Never emit an intent whose target is outside the authorized scope: it cannot be dispatched.`

func plannerPromptFor(controlPlane []string, hasScope ...bool) string {
	if len(controlPlane) > 0 {
		return plannerSystemPrompt + plannerBenchmarkRules
	}
	if len(hasScope) > 0 && hasScope[0] {
		return plannerSystemPrompt + plannerEngagementScopeRules
	}
	return plannerSystemPrompt
}

func parsePlannerOutput(raw string) (plannerOutput, error) {
	var out plannerOutput
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out, fmt.Errorf("empty output")
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, err
	}
	return out, nil
}

func intentKey(target, class, title string) string {
	ep := strings.ToLower(strings.TrimSpace(target))
	cl := strings.ToLower(strings.TrimSpace(class))
	ti := strings.ToLower(strings.TrimSpace(title))
	if ep == "" && cl == "" && ti == "" {
		return ""
	}
	return ep + "|" + cl + "|" + ti
}

func truncateText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// isControlPlaneTarget reports whether a single target/endpoint host is a
// control-plane host. API orchestration intents are the only allowed use.
func isControlPlaneTarget(target string, controlPlane []string) bool {
	host := normalizeHost(target)
	if host == "" {
		return false
	}
	for _, raw := range controlPlane {
		cp := normalizeHost(raw)
		if cp != "" && (host == cp || strings.HasSuffix(host, "."+cp)) {
			return true
		}
	}
	return false
}

// isPlaceholderTarget rejects model-invented placeholders such as
// "CONTAINER_ADDR" or "pending-container-address-from-start-intent". They are
// not dispatchable and would otherwise occupy workers/blocked rows forever.
func isPlaceholderTarget(raw string) bool {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return true
	}
	for _, bad := range []string{
		"CONTAINER_ADDR", "PENDING-CONTAINER", "PENDING-", "PLACEHOLDER",
		"TBD", "<", ">", "{{", "}}", "EXAMPLE.COM",
	} {
		if strings.Contains(s, bad) {
			return true
		}
	}
	return false
}

// controlPlaneIntentStartsOrSubmits reports whether a control-plane intent
// opens (start) or submits (submit/flag) work. After the platform task has
// finished these are the only control-plane actions the planner may not add.
func controlPlaneIntentStartsOrSubmits(in plannerIntent) bool {
	text := strings.ToLower(strings.TrimSpace(in.Endpoint) + " " + strings.TrimSpace(in.Target))
	return strings.Contains(text, "/submit") || strings.Contains(text, "/start")
}

// controlPlaneIntentAttacks reports whether a control-plane intent carries an
// attack action. The API lane must only list/start/submit/close; attacks are
// engine-materialized container intents with the execution surface.
func controlPlaneIntentAttacks(in plannerIntent) bool {
	text := strings.ToLower(strings.Join([]string{in.Title, in.Notes, in.VulnClass}, " "))
	for _, marker := range []string{
		"attack", "exploit", "recover the flag", "提取 flag", "拿 flag", "拿到 flag",
		"利用", "rce", "webshell", "getshell", "反弹 shell",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// controlPlaneAPITargetAllowed requires the API lane to name a configured
// control-plane host. When a domain control-plane host is configured, a bare
// IP (for example the VPN health-check address) is refused: the health probe
// is not the platform API base.
func controlPlaneAPITargetAllowed(target string, controlPlane []string) bool {
	if !isControlPlaneTarget(target, controlPlane) {
		return false
	}
	host := normalizeHost(target)
	if net.ParseIP(host) == nil {
		return true
	}
	for _, raw := range controlPlane {
		cp := normalizeHost(raw)
		if cp != "" && net.ParseIP(cp) == nil {
			return false
		}
	}
	return true
}

// intentNamesControlPlaneForScan rejects an intent whose text points at a
// control-plane host unless it is explicitly the API orchestration lane.
func intentNamesControlPlaneForScan(in plannerIntent, controlPlane []string) bool {
	if strings.EqualFold(strings.TrimSpace(in.TargetKind), "control_plane_api") {
		return false
	}
	text := strings.ToLower(in.Target + " " + in.Endpoint + " " + in.Title + " " + in.Notes + " " + in.TaskID)
	for _, raw := range controlPlane {
		host := normalizeHost(raw)
		if host != "" && strings.Contains(text, host) {
			return true
		}
	}
	return false
}

func normalizeHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "//" + raw
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return strings.TrimSuffix(u.Hostname(), ".")
	}
	return strings.TrimSuffix(raw, ".")
}
