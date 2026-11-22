package agentsgraph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// AgentState is the read-only projection of one delegated agent. It is the
// authority panels read instead of reconstructing worker state from telemetry
// (charter §5.1/§6: events are an audit projection, not the state authority).
// Every field is copied out of subAgentState under the graph lock — callers
// never receive internal pointers.
type AgentState struct {
	AgentID string `json:"agent_id"`
	// Lane is the delegated role name ("injection-serverside", ...). The UI
	// groups sessions by lane and keys them by AgentID.
	Lane    string `json:"lane"`
	Status  string `json:"status"` // queued | running | completed | failed
	Task    string `json:"task,omitempty"`
	Target  string `json:"target,omitempty"`
	Started string `json:"started_at,omitempty"`
	// CompletedAt is empty while the agent is running or queued.
	CompletedAt string `json:"completed_at,omitempty"`
	Error       string `json:"error,omitempty"`
	// QueuedPosition is 1-based and only set while Status == "queued".
	QueuedPosition int `json:"queued_position,omitempty"`
	// PartialEvents counts the streamed partial evidence rows the agent
	// produced (a liveness hint, never a progress verdict).
	PartialEvents int `json:"partial_events,omitempty"`
	// Collected reports whether the coordinator already read the final result.
	Collected bool `json:"collected,omitempty"`
}

// GraphState is the whole-scan delegation view: the concurrency limit and every
// delegated agent with its current lifecycle state.
type GraphState struct {
	// Limit is the effective concurrent-agent cap for this scan
	// (min(XALGORIX_MAX_CONCURRENT_AGENTS, lifetime delegation cap)).
	Limit int `json:"limit"`
	// MaxAgents is the lifetime delegation cap (one wave + manual agents).
	MaxAgents int          `json:"max_agents"`
	Running   int          `json:"running"`
	Queued    int          `json:"queued"`
	Agents    []AgentState `json:"agents"`
}

// StateEvent is a best-effort lifecycle notification. Consumers use it ONLY to
// invalidate a cached projection; the projection itself stays authoritative.
type StateEvent struct {
	Kind    string // agent_queued | agent_started | agent_completed | agent_failed
	AgentID string
	Lane    string
	Status  string
	// Limit/Running/Queued snapshot the pool at emit time so a listener can
	// render "2/3" without an extra fetch.
	Limit   int
	Running int
	Queued  int
}

// SetStateSink installs the per-scan lifecycle listener (wired by the agent
// layer to the scan's event emitter). The sink MUST NOT call back into the
// graph: notifications are delivered after the graph lock is released, but the
// contract keeps the sink cheap and side-effect free.
func (g *Graph) SetStateSink(fn func(StateEvent)) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stateSink = fn
}

// notify delivers one lifecycle event to the configured sink. Callers must not
// hold g.mu (notify re-locks it to read the sink).
func (g *Graph) notify(kind, agentID, lane, status string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	fn := g.stateSink
	if fn == nil {
		g.mu.Unlock()
		return
	}
	ev := StateEvent{
		Kind:    kind,
		AgentID: agentID,
		Lane:    lane,
		Status:  status,
		Limit:   g.maxConcurrent,
		Running: g.countStatusLocked("running"),
		Queued:  len(g.queue),
	}
	g.mu.Unlock()
	fn(ev)
}

func (g *Graph) countStatusLocked(status string) int {
	count := 0
	for _, state := range g.agents {
		if state.Status == status {
			count++
		}
	}
	return count
}

// Limit returns the effective concurrent-agent cap for this scan.
func (g *Graph) Limit() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxConcurrent
}

// MaxAgents returns the lifetime delegation cap for this scan.
func (g *Graph) MaxAgents() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxAgents
}

// snapshotLocked builds the immutable projection. Ordering is deterministic
// (queue position first, then start time, then id) so identical state renders
// identically for every caller.
func (g *Graph) snapshotLocked() GraphState {
	position := make(map[string]int, len(g.queue))
	for i, queued := range g.queue {
		position[queued.ID] = i + 1
	}
	view := GraphState{
		Limit:     g.maxConcurrent,
		MaxAgents: g.maxAgents,
		Agents:    make([]AgentState, 0, len(g.agents)),
	}
	for _, state := range g.agents {
		agent := AgentState{
			AgentID:        state.ID,
			Lane:           state.Name,
			Status:         state.Status,
			Task:           state.Task,
			Error:          state.Error,
			QueuedPosition: position[state.ID],
			PartialEvents:  len(state.Partial),
			Collected:      state.Observed,
		}
		if len(state.Targets) > 0 {
			agent.Target = state.Targets[0]
		}
		if !state.StartedAt.IsZero() {
			agent.Started = state.StartedAt.UTC().Format(time.RFC3339)
		}
		if !state.CompletedAt.IsZero() {
			agent.CompletedAt = state.CompletedAt.UTC().Format(time.RFC3339)
		}
		switch state.Status {
		case "running":
			view.Running++
		case "queued":
			view.Queued++
		}
		view.Agents = append(view.Agents, agent)
	}
	sort.Slice(view.Agents, func(i, j int) bool {
		a, b := view.Agents[i], view.Agents[j]
		// Queue order is the most actionable ordering; running work follows.
		if a.QueuedPosition != b.QueuedPosition {
			if a.QueuedPosition == 0 {
				return false
			}
			if b.QueuedPosition == 0 {
				return true
			}
			return a.QueuedPosition < b.QueuedPosition
		}
		if a.Started != b.Started {
			if a.Started == "" {
				return false
			}
			if b.Started == "" {
				return true
			}
			return a.Started < b.Started
		}
		return a.AgentID < b.AgentID
	})
	return view
}

// Snapshot returns the read-only delegation projection for this scan.
func (g *Graph) Snapshot() GraphState {
	if g == nil {
		return GraphState{Agents: []AgentState{}}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshotLocked()
}

// ReadCheckpointState loads a persisted execution-graph checkpoint without
// constructing a live graph. It is the terminal fallback for panels that read
// worker state after the run's agent has been released: the checkpoint is the
// same durable file the graph itself writes (no second persistence authority).
func ReadCheckpointState(dir string) (GraphState, bool) {
	if dir == "" {
		return GraphState{}, false
	}
	data, err := os.ReadFile(filepath.Join(dir, "execution-graph.json"))
	if err != nil {
		return GraphState{}, false
	}
	var saved graphCheckpoint
	if err := json.Unmarshal(data, &saved); err != nil {
		return GraphState{}, false
	}
	view := GraphState{Limit: DefaultMaxConcurrentAgents, Agents: make([]AgentState, 0, len(saved.Agents))}
	// The checkpoint does not store the caps; the lifetime cap is bounded by
	// the agent's merged profile count and is not needed for a terminal read.
	view.MaxAgents = len(saved.Agents)
	if view.MaxAgents < 1 {
		view.MaxAgents = 1
	}
	view.Limit = EffectiveMaxConcurrentAgents()
	for _, state := range saved.Agents {
		agent := AgentState{
			AgentID:       state.ID,
			Lane:          state.Name,
			Status:        state.Status,
			Task:          state.Task,
			Error:         state.Error,
			Collected:     state.Observed,
			PartialEvents: len(state.Partial),
		}
		if len(state.Targets) > 0 {
			agent.Target = state.Targets[0]
		}
		if !state.StartedAt.IsZero() {
			agent.Started = state.StartedAt.UTC().Format(time.RFC3339)
		}
		if !state.CompletedAt.IsZero() {
			agent.CompletedAt = state.CompletedAt.UTC().Format(time.RFC3339)
		}
		switch state.Status {
		case "running":
			// A persisted "running" row has no live runner after a restart.
			agent.Status = "failed"
			if agent.Error == "" {
				agent.Error = "run ended before the agent completed"
			}
			view.Running = 0
		case "queued":
			agent.Status = "failed"
			if agent.Error == "" {
				agent.Error = "queued agent never started"
			}
		case "completed":
			// keep
		case "failed":
			// keep
		default:
			agent.Status = "failed"
		}
		view.Agents = append(view.Agents, agent)
	}
	sort.Slice(view.Agents, func(i, j int) bool { return view.Agents[i].Started < view.Agents[j].Started })
	return view, true
}
