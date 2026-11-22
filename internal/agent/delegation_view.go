package agent

import "github.com/c1ayooo/xalgorix/v4/internal/tools/agentsgraph"

// DelegatedAgent is the stable, JSON-friendly projection of one delegated
// agent. It deliberately mirrors agentsgraph.AgentState (the authority) instead
// of exposing the graph's internal structs to the web layer.
type DelegatedAgent struct {
	AgentID string `json:"agent_id"`
	Lane    string `json:"lane"`
	Status  string `json:"status"` // queued | running | completed | failed
	Task    string `json:"task,omitempty"`
	Target  string `json:"target,omitempty"`
	// Started/CompletedAt are RFC3339 UTC; CompletedAt stays empty while the
	// agent is running or queued.
	Started        string `json:"started_at,omitempty"`
	CompletedAt    string `json:"completed_at,omitempty"`
	Error          string `json:"error,omitempty"`
	QueuedPosition int    `json:"queued_position,omitempty"`
	PartialEvents  int    `json:"partial_events,omitempty"`
	Collected      bool   `json:"collected,omitempty"`
}

// DelegationView is the whole-scan worker pool projection served to panels.
type DelegationView struct {
	// Available is false when this agent owns no delegation graph (the scan is
	// still starting, or the live agent was released). Consumers render a
	// neutral state instead of an error.
	Available bool `json:"available"`
	// Limit is the effective concurrent-agent cap; MaxAgents is the lifetime
	// delegation cap. Limit is always <= MaxAgents.
	Limit     int              `json:"limit"`
	MaxAgents int              `json:"max_agents"`
	Running   int              `json:"running"`
	Queued    int              `json:"queued"`
	Agents    []DelegatedAgent `json:"agents"`
}

// DelegationSnapshot returns the read-only worker pool state for this scan.
// It is safe to call from HTTP handlers (the graph mirrors its state under its
// own lock and hands back copied values).
func (a *Agent) DelegationSnapshot() DelegationView {
	if a == nil || a.agentGraph == nil {
		return DelegationView{Agents: []DelegatedAgent{}}
	}
	state := a.agentGraph.Snapshot()
	view := DelegationView{
		Available: true,
		Limit:     state.Limit,
		MaxAgents: state.MaxAgents,
		Running:   state.Running,
		Queued:    state.Queued,
		Agents:    make([]DelegatedAgent, 0, len(state.Agents)),
	}
	for _, agent := range state.Agents {
		view.Agents = append(view.Agents, DelegatedAgent{
			AgentID:        agent.AgentID,
			Lane:           agent.Lane,
			Status:         agent.Status,
			Task:           agent.Task,
			Target:         agent.Target,
			Started:        agent.Started,
			CompletedAt:    agent.CompletedAt,
			Error:          agent.Error,
			QueuedPosition: agent.QueuedPosition,
			PartialEvents:  agent.PartialEvents,
			Collected:      agent.Collected,
		})
	}
	return view
}

// PlanProjection is re-exported so the web layer can name one type for both
// the live agent and the terminal record.
type PlanProjection = PlanView

// DelegationProjectionFromCheckpoint rebuilds the worker pool view from the
// scan's durable execution-graph checkpoint. Used only after the live agent is
// released (terminal reads); the checkpoint is the file the graph already
// writes, so this introduces no second persistence authority.
func DelegationProjectionFromCheckpoint(scanDir string) (DelegationView, bool) {
	state, ok := agentsgraph.ReadCheckpointState(scanDir)
	if !ok {
		return DelegationView{Agents: []DelegatedAgent{}}, false
	}
	view := DelegationView{
		Available: true,
		Limit:     state.Limit,
		MaxAgents: state.MaxAgents,
		Running:   state.Running,
		Queued:    state.Queued,
		Agents:    make([]DelegatedAgent, 0, len(state.Agents)),
	}
	for _, agent := range state.Agents {
		view.Agents = append(view.Agents, DelegatedAgent{
			AgentID:        agent.AgentID,
			Lane:           agent.Lane,
			Status:         agent.Status,
			Task:           agent.Task,
			Target:         agent.Target,
			Started:        agent.Started,
			CompletedAt:    agent.CompletedAt,
			Error:          agent.Error,
			QueuedPosition: agent.QueuedPosition,
			PartialEvents:  agent.PartialEvents,
			Collected:      agent.Collected,
		})
	}
	return view, true
}
