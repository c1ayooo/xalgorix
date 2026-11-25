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
	"fmt"
	"strings"
	"sync"
	"time"
)

// ProgressSignal is one typed increment a worker can produce. It is deliberately
// broader than "wrote an InfoItem": a binary-analysis worker that produces an
// artifact or advances coverage is making progress even when no new note exists.
type ProgressSignal string

const (
	SignalInfo        ProgressSignal = "info"
	SignalBelief      ProgressSignal = "belief"
	SignalEvidence    ProgressSignal = "evidence"
	SignalObservation ProgressSignal = "observation"
	SignalAsset       ProgressSignal = "asset"
	SignalArtifact    ProgressSignal = "artifact"
	SignalCoverage    ProgressSignal = "coverage"
	SignalWorkflow    ProgressSignal = "workflow"
	SignalFinding     ProgressSignal = "finding"
	SignalAnalysis    ProgressSignal = "analysis"
)

// ProgressProfile selects the signal weights and stall window for a task shape.
type ProgressProfile string

const (
	ProfileDefault    ProgressProfile = "default"
	ProfileWeb        ProgressProfile = "web"
	ProfileProtocol   ProgressProfile = "protocol"
	ProfileBinary     ProgressProfile = "binary"
	ProfileMultistage ProgressProfile = "multistage"
)

// Progress status values. They describe the observation only; the graph remains
// the lifecycle authority and a typed kill remains the only terminal action.
const (
	ProgressHealthy            = "healthy"
	ProgressAnalysisProgress   = "analysis_progress"
	ProgressActiveTool         = "active_tool"
	ProgressActiveNoProgress   = "active_no_progress"
	ProgressEnvironmentBlocked = "environment_blocked"
	ProgressStalled            = "stalled"
)

// SignalProgress is one signal's bounded projection.
type SignalProgress struct {
	Count  int       `json:"count"`
	Last   time.Time `json:"last"`
	Weight float64   `json:"weight"`
}

// ProgressSnapshot is the deterministic read model the reclaim planner and the
// stall projection consume. It is derived from the in-memory store only.
type ProgressSnapshot struct {
	AgentID             string
	Profile             ProgressProfile
	StartedAt           time.Time
	LastTouch           time.Time
	LastSignal          time.Time
	Signals             map[string]SignalProgress
	ActiveTool          string
	ActiveSince         time.Time
	ToolDeadline        time.Time
	ConsecutiveFailures int
	LastFailureClass    string
	Status              string
	Reason              string
	StallAfter          time.Duration
}

type signalState struct {
	count int
	last  time.Time
}

type failureState struct {
	consecutive int
	last        time.Time
	tool        string
	class       string
}

var profileSignalWeights = map[ProgressProfile]map[ProgressSignal]float64{
	ProfileDefault: {
		SignalInfo: 1, SignalBelief: .8, SignalEvidence: 1, SignalObservation: .5,
		SignalAsset: .6, SignalArtifact: .8, SignalCoverage: .7, SignalWorkflow: .6,
		SignalFinding: 1, SignalAnalysis: .35,
	},
	ProfileWeb: {
		SignalInfo: 1, SignalBelief: .9, SignalEvidence: 1, SignalObservation: .6,
		SignalAsset: .7, SignalArtifact: .7, SignalCoverage: .8, SignalWorkflow: .6,
		SignalFinding: 1, SignalAnalysis: .3,
	},
	ProfileProtocol: {
		SignalInfo: .8, SignalBelief: .8, SignalEvidence: 1, SignalObservation: .9,
		SignalAsset: .7, SignalArtifact: .9, SignalCoverage: .6, SignalWorkflow: .5,
		SignalFinding: 1, SignalAnalysis: .45,
	},
	ProfileBinary: {
		SignalInfo: .4, SignalBelief: .6, SignalEvidence: .8, SignalObservation: .7,
		SignalAsset: .5, SignalArtifact: 1, SignalCoverage: .5, SignalWorkflow: .8,
		SignalFinding: 1, SignalAnalysis: .7,
	},
	ProfileMultistage: {
		SignalInfo: .8, SignalBelief: .8, SignalEvidence: .9, SignalObservation: .5,
		SignalAsset: .7, SignalArtifact: .9, SignalCoverage: .6, SignalWorkflow: 1,
		SignalFinding: 1, SignalAnalysis: .4,
	},
}

func profileWindow(profile ProgressProfile) time.Duration {
	switch profile {
	case ProfileBinary:
		return 30 * time.Minute
	case ProfileMultistage:
		return 25 * time.Minute
	default:
		return 20 * time.Minute
	}
}

func signalWeight(profile ProgressProfile, signal ProgressSignal) float64 {
	weights := profileSignalWeights[profile]
	if weights == nil {
		weights = profileSignalWeights[ProfileDefault]
	}
	return weights[signal]
}

// WindowForProfile is the stall observation window for a profile.
func WindowForProfile(profile ProgressProfile) time.Duration {
	return profileWindow(NormalizeProgressProfile(string(profile)))
}

// NormalizeProgressProfile maps arbitrary task text to a known profile.
func NormalizeProgressProfile(raw string) ProgressProfile {
	switch ProgressProfile(strings.ToLower(strings.TrimSpace(raw))) {
	case ProfileWeb:
		return ProfileWeb
	case ProfileProtocol:
		return ProfileProtocol
	case ProfileBinary:
		return ProfileBinary
	case ProfileMultistage:
		return ProfileMultistage
	default:
		return ProfileDefault
	}
}

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
	// Typed P2 projections.
	signals      map[string]map[ProgressSignal]*signalState
	failures     map[string]*failureState
	profiles     map[string]ProgressProfile
	lastAnalysis map[string]string
	// Phase-1 spin observation (observe-only): tool actions and failure
	// signatures since the last information-layer signal. Nothing in this
	// block may drive steer/kill; it exists to calibrate a future policy.
	spinActionsSince map[string]int
	spinSignals      map[string]int
	spinEpochAt      map[string]time.Time
	spinSigs         map[string]map[string]int
	spinMaxRepeat    map[string]int
	spinLastSig      map[string]string
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
		items:            make(map[string]*AgentProgress),
		units:            make(map[string]string),
		exits:            make(map[string]ExitDeclaration),
		signals:          make(map[string]map[ProgressSignal]*signalState),
		failures:         make(map[string]*failureState),
		profiles:         make(map[string]ProgressProfile),
		spinActionsSince: make(map[string]int),
		spinSignals:      make(map[string]int),
		spinEpochAt:      make(map[string]time.Time),
		spinSigs:         make(map[string]map[string]int),
		spinMaxRepeat:    make(map[string]int),
		spinLastSig:      make(map[string]string),
		lastAnalysis:     make(map[string]string),
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
	p.MarkMutation(agentID, SignalInfo)
}

// MarkMutation records one successful information-layer mutation: exactly one
// legacy progress count plus one typed increment per signal.
func (p *ProgressStore) MarkMutation(agentID string, signals ...ProgressSignal) {
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
	for _, signal := range signals {
		p.markSignalLocked(agentID, signal, now)
	}
	if p.failures != nil {
		p.failures[agentID] = &failureState{}
	}
}

// MarkSignal records one typed increment without advancing the legacy
// information-layer count (used by analysis/artifact observations).
func (p *ProgressStore) MarkSignal(agentID string, signal ProgressSignal) {
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
	p.markSignalLocked(agentID, signal, now)
	if now.After(entry.Touch) {
		entry.Touch = now
	}
	if p.failures != nil {
		p.failures[agentID] = &failureState{}
	}
}

func (p *ProgressStore) markSignalLocked(agentID string, signal ProgressSignal, now time.Time) {
	if strings.TrimSpace(string(signal)) == "" {
		return
	}
	if p.signals == nil {
		p.signals = make(map[string]map[ProgressSignal]*signalState)
	}
	bySignal := p.signals[agentID]
	if bySignal == nil {
		bySignal = make(map[ProgressSignal]*signalState)
		p.signals[agentID] = bySignal
	}
	state := bySignal[signal]
	if state == nil {
		state = &signalState{}
		bySignal[signal] = state
	}
	state.count++
	if now.After(state.last) {
		state.last = now
	}
	// Observe-only epoch: a real information-layer signal resets the action
	// counter and advances the epoch clock.
	p.spinSignals[agentID]++
	p.spinActionsSince[agentID] = 0
	p.spinEpochAt[agentID] = now
}

// MarkAnalysis records a distinct analysis read. Re-reading the same reference
// consecutively is touch, not new analysis progress.
func (p *ProgressStore) MarkAnalysis(agentID, key string) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.entryLocked(agentID)
	if p.lastAnalysis == nil {
		p.lastAnalysis = make(map[string]string)
	}
	if p.lastAnalysis[agentID] == key {
		if now.After(entry.Touch) {
			entry.Touch = now
		}
		return
	}
	p.lastAnalysis[agentID] = key
	p.markSignalLocked(agentID, SignalAnalysis, now)
	if now.After(entry.Touch) {
		entry.Touch = now
	}
	if p.failures != nil {
		p.failures[agentID] = &failureState{}
	}
}

// MarkFailure records one failed tool result and classifies its environment
// symptom. A later successful result clears the consecutive count.
func (p *ProgressStore) MarkFailure(agentID, tool, errorText string) {
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
	if p.failures == nil {
		p.failures = make(map[string]*failureState)
	}
	state := p.failures[agentID]
	if state == nil {
		state = &failureState{}
		p.failures[agentID] = state
	}
	state.consecutive++
	state.last = now
	state.tool = strings.TrimSpace(tool)
	state.class = classifyEnvironmentFailure(errorText)
	if entry := p.items[agentID]; entry != nil && now.After(entry.Touch) {
		entry.Touch = now
	}
}

// ClearFailures resets the consecutive-failure counter after a success.
func (p *ProgressStore) ClearFailures(agentID string) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return
	}
	p.mu.Lock()
	if p.failures != nil {
		p.failures[agentID] = &failureState{}
	}
	p.mu.Unlock()
}

func classifyEnvironmentFailure(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	switch {
	case t == "":
		return ""
	case strings.Contains(t, "timed out") || strings.Contains(t, "timeout"):
		return "timeout"
	case strings.Contains(t, "permission denied") || strings.Contains(t, "forbidden") || strings.Contains(t, "403"):
		return "denied"
	case strings.Contains(t, "connection refused") || strings.Contains(t, "no route") || strings.Contains(t, "unreachable") || strings.Contains(t, "network is down"):
		return "unreachable"
	case strings.Contains(t, "not found") || strings.Contains(t, "no such file") || strings.Contains(t, "command not found"):
		return "missing"
	default:
		return "error"
	}
}

// SetProfile binds the task profile used to weight this worker's signals.
func (p *ProgressStore) SetProfile(agentID string, profile ProgressProfile) {
	if p == nil {
		return
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return
	}
	p.mu.Lock()
	if p.profiles == nil {
		p.profiles = make(map[string]ProgressProfile)
	}
	p.profiles[agentID] = NormalizeProgressProfile(string(profile))
	p.mu.Unlock()
}

// ProfileFor returns the worker's profile, defaulting to ProfileDefault.
func (p *ProgressStore) ProfileFor(agentID string) ProgressProfile {
	if p == nil {
		return ProfileDefault
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if profile := p.profiles[normalizeAgentID(agentID)]; profile != "" {
		return profile
	}
	return ProfileDefault
}

// LastSignalAt returns the newest typed or legacy progress timestamp.
func (p *ProgressStore) LastSignalAt(agentID string) time.Time {
	if p == nil {
		return time.Time{}
	}
	agentID = normalizeAgentID(agentID)
	if agentID == "" {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var last time.Time
	for _, state := range p.signals[agentID] {
		if state.last.After(last) {
			last = state.last
		}
	}
	if entry := p.items[agentID]; entry != nil && entry.Progress.After(last) {
		last = entry.Progress
	}
	return last
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

// SnapshotFor builds the typed progress read model for one worker. It is a pure
// projection: no state changes and no decisions.
func (p *ProgressStore) SnapshotFor(agentID string, started, now time.Time) ProgressSnapshot {
	agentID = normalizeAgentID(agentID)
	snap := ProgressSnapshot{
		AgentID: agentID, Profile: ProfileDefault, StartedAt: started,
		Signals: map[string]SignalProgress{}, Status: ProgressStalled,
		StallAfter: profileWindow(ProfileDefault),
	}
	if p == nil || agentID == "" {
		return snap
	}
	if now.IsZero() {
		now = time.Now()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if profile := p.profiles[agentID]; profile != "" {
		snap.Profile = profile
	}
	snap.StallAfter = profileWindow(snap.Profile)
	entry := p.items[agentID]
	if entry != nil {
		snap.LastTouch = entry.Touch
		snap.ActiveTool = entry.ActiveTool
		snap.ActiveSince = entry.ToolStarted
		snap.ToolDeadline = entry.ToolDeadline
	}
	for signal, state := range p.signals[agentID] {
		weight := signalWeight(snap.Profile, signal)
		snap.Signals[string(signal)] = SignalProgress{Count: state.count, Last: state.last, Weight: weight}
		if state.last.After(snap.LastSignal) {
			snap.LastSignal = state.last
		}
	}
	if entry != nil && entry.Progress.After(snap.LastSignal) {
		snap.LastSignal = entry.Progress
	}
	failure := p.failures[agentID]
	if failure != nil {
		snap.ConsecutiveFailures = failure.consecutive
		snap.LastFailureClass = failure.class
	}

	lastInfo := time.Time{}
	if state := p.signals[agentID][SignalInfo]; state != nil {
		lastInfo = state.last
	}
	base := snap.LastSignal
	if base.IsZero() {
		base = started
	}
	idle := now.Sub(base)
	hasSignal := !snap.LastSignal.IsZero()
	active := entry != nil && strings.TrimSpace(entry.ActiveTool) != ""
	activeBlocking := active && (entry.ToolDeadline.IsZero() || now.Before(entry.ToolDeadline))

	switch {
	case activeBlocking:
		snap.Status = ProgressActiveTool
		snap.Reason = "tool " + entry.ActiveTool + " is still running"
	case failure != nil && failure.consecutive >= 3 && now.Sub(failure.last) < snap.StallAfter/2:
		snap.Status = ProgressEnvironmentBlocked
		snap.Reason = fmt.Sprintf("%d consecutive tool failures (%s) on %s; environment may be blocking",
			failure.consecutive, failure.class, failure.tool)
	case now.Sub(base) >= snap.StallAfter:
		snap.Status = ProgressStalled
		snap.Reason = fmt.Sprintf("no typed progress for %s (window %s)", idle.Round(time.Second), snap.StallAfter)
	case hasSignal && !lastInfo.IsZero() && now.Sub(lastInfo) < snap.StallAfter:
		snap.Status = ProgressHealthy
		snap.Reason = "information-layer progress inside the window"
	case hasSignal:
		snap.Status = ProgressAnalysisProgress
		snap.Reason = "analysis/artifact/belief progress without a new InfoItem"
	case active:
		snap.Status = ProgressActiveNoProgress
		snap.Reason = "tool activity without a typed increment"
	default:
		snap.Status = ProgressActiveNoProgress
		snap.Reason = "touch only; no typed increment"
	}
	if snap.LastSignal.After(snap.LastTouch) {
		snap.LastTouch = snap.LastSignal
	}
	return snap
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
