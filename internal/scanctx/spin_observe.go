package scanctx

// spin_observe.go — Phase-1 observation of worker spin (observe-only).
//
// The existing reclaim clock answers "when did this worker last produce a
// typed information-layer signal?" It cannot see HOW MUCH activity happened in
// between: a worker retrying one dead port 30 times and one that tried 3 things
// look identical. This projection records the missing half:
//
//	actions since the last signal, and repeated (tool,target,outcome) signatures.
//
// It is strictly observational. No kill/steer decision reads these numbers yet;
// they are logged so a future policy can be calibrated against real runs
// instead of an arbitrary token budget.

import "time"

// SpinSnapshot is the observation view for one worker.
type SpinSnapshot struct {
	ActionsSinceProgress int
	Signals              int
	MaxRepeat            int
	LastSignature        string
	IdleSinceSignal      time.Duration
}

// ObserveToolAction records one tool invocation (before it runs).
func (p *ProgressStore) ObserveToolAction(agentID string) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.spinActionsSince == nil {
		p.spinActionsSince = make(map[string]int)
	}
	p.spinActionsSince[agentID]++
}

// ObserveToolOutcome records the canonical result signature of one tool call.
// signature should be stable across retries of the same failing action, e.g.
// "terminal_execute|10.0.177.18:8443|refused".
func (p *ProgressStore) ObserveToolOutcome(agentID, signature string) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" || signature == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.spinSigs == nil {
		p.spinSigs = make(map[string]map[string]int)
	}
	bySig := p.spinSigs[agentID]
	if bySig == nil {
		bySig = make(map[string]int)
		p.spinSigs[agentID] = bySig
	}
	bySig[signature]++
	if bySig[signature] > p.spinMaxRepeat[agentID] {
		p.spinMaxRepeat[agentID] = bySig[signature]
	}
	p.spinLastSig[agentID] = signature
}

// SpinSnapshotFor returns the observation view, measuring time since the last
// information-layer signal (or since start when none exists).
func (p *ProgressStore) SpinSnapshotFor(agentID string, started, now time.Time) SpinSnapshot {
	if p == nil {
		return SpinSnapshot{}
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return SpinSnapshot{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	since := p.spinEpochAt[agentID]
	if since.IsZero() {
		since = started
	}
	snap := SpinSnapshot{
		ActionsSinceProgress: p.spinActionsSince[agentID],
		Signals:              p.spinSignals[agentID],
		MaxRepeat:            p.spinMaxRepeat[agentID],
		LastSignature:        p.spinLastSig[agentID],
	}
	if !since.IsZero() {
		snap.IdleSinceSignal = now.Sub(since)
	}
	return snap
}
