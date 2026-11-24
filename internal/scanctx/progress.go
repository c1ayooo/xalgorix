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
type AgentProgress struct {
	Touch         time.Time
	Progress      time.Time
	ProgressCount int
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
}

// NewProgressStore creates an empty progress ledger.
func NewProgressStore() *ProgressStore {
	return &ProgressStore{
		items: make(map[string]*AgentProgress),
		units: make(map[string]string),
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
