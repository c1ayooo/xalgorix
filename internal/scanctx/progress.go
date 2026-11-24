package scanctx

// progress.go — the in-memory progress ledger projection
// (docs/PLANNER_OBSERVATION_AND_CORRECTION.md §3).
//
// This store answers one question for the planner/main agent: "did this worker
// produce an INFORMATION-LAYER increment recently?" It is an observation
// projection only: nothing here is persisted, nothing is a state authority,
// and losing it on restart cannot change any intent's lifecycle (the graph and
// the ledger remain the authorities). Token counts, assistant output, tool-call
// counts and heartbeats are deliberately NOT progress.

import (
	"strings"
	"sync"
	"time"
)

// AgentProgress is one worker's progress projection. Touch is the last tool
// result observed (any result, including failures); Progress is the last
// information-layer mutation and ProgressCount counts those mutations.
// ActiveTool/ToolStarted track a tool that is currently executing: a legitimate
// long-running tool is not a stall even when no result has arrived yet.
type AgentProgress struct {
	Touch         time.Time
	Progress      time.Time
	ProgressCount int
	ActiveTool    string
	ToolStarted   time.Time
	// ToolDeadline is the tool's own maximum execution time. A tool past its
	// deadline is no longer a legitimate long-running task; the reclaim clock
	// may act on it.
	ToolDeadline time.Time
}

// ProgressStore tracks per-worker touch/progress timestamps for one scan. It is
// safe for concurrent use and nil-safe on every method.
type ProgressStore struct {
	mu    sync.Mutex
	items map[string]*AgentProgress
	// units maps an authoritative intent unit id to the delegated agent id that
	// executes it, so kill validation can translate the main agent's unit
	// address into the key the progress hook writes.
	units map[string]string
	// exits holds each worker's declared lane outcome (INTEL_HANDOFF_SPEC D6).
	// It is consumed by the settlement authority; losing it on restart cannot
	// change an intent's lifecycle (the graph remains the authority).
	exits map[string]ExitDeclaration
}

// ExitDeclaration is the worker's declared outcome for its lane. Exit is
// "none", "access:<fingerprint>" or "finding:<id>"; Reason is required for
// "none" so a lane that yielded nothing always says why.
type ExitDeclaration struct {
	Exit   string
	Reason string
	At     time.Time
}

// NewProgressStore creates an empty progress ledger.
func NewProgressStore() *ProgressStore {
	return &ProgressStore{
		items: make(map[string]*AgentProgress),
		units: make(map[string]string),
		exits: make(map[string]ExitDeclaration),
	}
}

func normalizeAgentID(agentID string) string {
	return strings.TrimSpace(agentID)
}

// Touch records that the worker observed a tool result. It never counts as
// progress. Empty agent ids are ignored rather than stored under a shared key.
func (p *ProgressStore) Touch(agentID string) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.entryLocked(agentID)
	// Monotonic: a stale/racing observation must never move the clock back.
	if now.After(entry.Touch) {
		entry.Touch = now
	}
}

// MarkProgress records an information-layer increment for the worker.
func (p *ProgressStore) MarkProgress(agentID string) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.entryLocked(agentID)
	entry.ProgressCount++
	if now.After(entry.Progress) {
		entry.Progress = now
	}
	if now.After(entry.Touch) {
		entry.Touch = now
	}
}

// AgentTimes returns the worker's last touch and last progress. ok is false for
// an unknown (or blank) agent.
func (p *ProgressStore) AgentTimes(agentID string) (lastTouch, lastProgress time.Time, ok bool) {
	if p == nil {
		return time.Time{}, time.Time{}, false
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return time.Time{}, time.Time{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, found := p.items[agentID]
	if !found {
		return time.Time{}, time.Time{}, false
	}
	return entry.Touch, entry.Progress, true
}

// BeginTool records that a tool started executing for the worker. It is not
// progress by itself; it pauses the stall clock until EndTool.
func (p *ProgressStore) BeginTool(agentID, name string) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.entryLocked(agentID)
	entry.ActiveTool = strings.TrimSpace(name)
	entry.ToolStarted = time.Now()
}

// EndTool clears the active-tool marker. It is called on every tool result.
func (p *ProgressStore) EndTool(agentID string) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.entryLocked(agentID)
	entry.ActiveTool = ""
	entry.ToolStarted = time.Time{}
	entry.ToolDeadline = time.Time{}
}

// SetToolDeadline records the maximum execution time for the active tool.
func (p *ProgressStore) SetToolDeadline(agentID string, deadline time.Time) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entryLocked(agentID).ToolDeadline = deadline
}

// ActiveToolDeadline returns the active tool's declared deadline, if any.
func (p *ProgressStore) ActiveToolDeadline(agentID string) (time.Time, bool) {
	if p == nil {
		return time.Time{}, false
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return time.Time{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, found := p.items[agentID]
	if !found || entry.ActiveTool == "" {
		return time.Time{}, false
	}
	return entry.ToolDeadline, true
}

// ActiveTool reports the tool the worker is currently executing, if any.
func (p *ProgressStore) ActiveTool(agentID string) (name string, started time.Time, ok bool) {
	if p == nil {
		return "", time.Time{}, false
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return "", time.Time{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, found := p.items[agentID]
	if !found || entry.ActiveTool == "" {
		return "", time.Time{}, false
	}
	return entry.ActiveTool, entry.ToolStarted, true
}

// Snapshot returns a copy of every worker's projection.
func (p *ProgressStore) Snapshot() map[string]AgentProgress {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]AgentProgress, len(p.items))
	for id, entry := range p.items {
		out[id] = *entry
	}
	return out
}

// BindUnit records that unitID is executed by agentID. It is called from the
// dispatch path, which is the one place that knows both identifiers.
func (p *ProgressStore) BindUnit(unitID, agentID string) {
	if p == nil {
		return
	}
	unitID = strings.TrimSpace(unitID)
	agentID = normalizeAgentID(agentID)
	if unitID == "" || agentID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.units == nil {
		p.units = make(map[string]string)
	}
	p.units[unitID] = agentID
}

// UnitAgent resolves the delegated agent id currently bound to a unit.
func (p *ProgressStore) UnitAgent(unitID string) (agentID string, ok bool) {
	if p == nil {
		return "", false
	}
	unitID = strings.TrimSpace(unitID)
	if unitID == "" {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	agentID, ok = p.units[unitID]
	return agentID, ok
}

func (p *ProgressStore) entryLocked(agentID string) *AgentProgress {
	if p.items == nil {
		p.items = make(map[string]*AgentProgress)
	}
	entry := p.items[agentID]
	if entry == nil {
		entry = &AgentProgress{}
		p.items[agentID] = entry
	}
	return entry
}

// RecordExit stores the worker's declared lane outcome. The last declaration
// wins (a worker may correct itself before ending); the settlement authority
// reads it once when the lane completes.
func (p *ProgressStore) RecordExit(agentID, exit, reason string) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exits == nil {
		p.exits = make(map[string]ExitDeclaration)
	}
	p.exits[agentID] = ExitDeclaration{
		Exit:   strings.TrimSpace(exit),
		Reason: strings.TrimSpace(reason),
		At:     time.Now(),
	}
}

// ExitFor returns the worker's declared lane outcome.
func (p *ProgressStore) ExitFor(agentID string) (ExitDeclaration, bool) {
	if p == nil {
		return ExitDeclaration{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	declaration, ok := p.exits[normalizeAgentID(agentID)]
	return declaration, ok
}
