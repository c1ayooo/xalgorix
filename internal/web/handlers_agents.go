package web

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// concurrencyView is the pool header the session page renders as "2/3".
type concurrencyView struct {
	Limit   int `json:"limit"`
	Running int `json:"running"`
	Queued  int `json:"queued"`
}

// claimedHypothesisView is the work a worker currently owns. It is a read-only
// join of the shared ledger onto the agent projection.
type claimedHypothesisView struct {
	ID        string `json:"id"`
	VulnClass string `json:"vuln_class,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Status    string `json:"status,omitempty"`
}

// agentEntryView is one worker row. ClaimedHypotheses answers "what is this
// worker actually doing right now", which the old event-derived rail could not.
type agentEntryView struct {
	agent.DelegatedAgent
	ClaimedHypotheses []claimedHypothesisView `json:"claimed_hypotheses,omitempty"`
}

// workerUnitView is one visible worker: the authoritative intent (work unit)
// with every agent instance that ever ran it. The unit's graph state is the
// row's primary status; agent_status is the live execution detail. This is the
// R7/4zreco identity contract: one work unit = one worker lane, retries append
// to the same lane instead of opening a second one.
type workerUnitView struct {
	UnitID     string `json:"unit_id"`
	Title      string `json:"title,omitempty"`
	State      string `json:"state"`  // graph state: open|running|paused|done|blocked|exhausted|stopped
	Status     string `json:"status"` // UI vocabulary: queued|running|paused|completed|failed
	VulnClass  string `json:"vuln_class,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Target     string `json:"target,omitempty"`
	Phase      int    `json:"phase,omitempty"`
	Priority   int    `json:"priority,omitempty"`
	Owner      string `json:"owner,omitempty"`
	LeaseUntil string `json:"lease_until,omitempty"`
	// AgentIDs lists every dispatch of this unit, oldest first. AgentStatus is
	// the aggregated live execution state (running wins, then queued, then the
	// newest terminal state).
	AgentIDs    []string `json:"agent_ids,omitempty"`
	AgentStatus string   `json:"agent_status,omitempty"`
	StartedAt   string   `json:"started_at,omitempty"`
	CompletedAt string   `json:"completed_at,omitempty"`
	// UpdatedAt is the latest graph transition for the unit (claim, settle,
	// retry). The worker rail sorts by it so the newest work is always first;
	// StartedAt is the first attempt and is kept for duration display.
	UpdatedAt string `json:"updated_at,omitempty"`
	Error     string `json:"error,omitempty"`
	// Claims are the ledger hypotheses currently owned by this unit's agents.
	Claims []claimedHypothesisView `json:"claimed_hypotheses,omitempty"`
}

type instanceAgentsView struct {
	Available   bool             `json:"available"`
	Concurrency concurrencyView  `json:"concurrency"`
	Agents      []agentEntryView `json:"agents"`
	// Units is the unit-keyed worker list (graph intents). Empty when the run
	// never used the R7 pool (legacy spawn mode) or the graph is unavailable;
	// consumers then fall back to Agents, which stay the instance projection.
	Units         []workerUnitView `json:"units"`
	PartialReason string           `json:"partial_reason,omitempty"`
}

// handleInstanceAgents serves GET /api/instances/{id}/agents — the worker pool
// projection. The panel reads state here instead of deduplicating agent ids out
// of the event buffer, which is why a finished worker used to keep rendering as
// an active spinner next to a running one.
func (s *Server) handleInstanceAgents(w http.ResponseWriter, r *http.Request, instanceID string) {
	view := s.instanceAgentsView(instanceID)
	writeJSONStatus(w, http.StatusOK, view)
}

func (s *Server) instanceAgentsView(instanceID string) instanceAgentsView {
	return s.instanceAgentsViewWith(instanceID, nil, nil, false)
}

// instanceAgentsViewWith builds the same projection with caller-supplied inputs:
// a task-scoped caller (handleTaskWorkers) passes the one scan-summary walk it
// already paid for plus the batch-loaded worker history, so a task with N runs
// costs ONE walk and ONE worker query instead of N of each.
//
// summaries nil = the per-instance walk (GET /api/instances/{id}/agents).
// historyLoaded false = read this run's rows here.
func (s *Server) instanceAgentsViewWith(
	instanceID string,
	summaries func() []scanEntry,
	history []runstate.WorkerRecord,
	historyLoaded bool,
) instanceAgentsView {
	empty := instanceAgentsView{Agents: []agentEntryView{}, Units: []workerUnitView{}}

	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()

	var (
		delegation agent.DelegationView
		ledger     *scanctx.LedgerStore
		live       bool
	)
	if inst != nil {
		inst.mu.RLock()
		liveAgent := inst.agent
		if inst.sctx != nil {
			ledger = inst.sctx.Ledger // may still be nil; consumers handle it
		}
		inst.mu.RUnlock()
		if liveAgent != nil {
			delegation = liveAgent.DelegationSnapshot()
			live = delegation.Available
		}
	}

	// Durable worker rows (run_workers, migration 0018). Written at every
	// lifecycle boundary by processEvent, so they answer "which workers did
	// this run ever have" without the live graph or the checkpoint — the
	// session page's Workers list is run state and reads PG (charter §6/§7).
	if !historyLoaded {
		history = s.workerHistory(instanceID)
	}

	if !live {
		// Terminal / released run. PG rows are the authority when present; the
		// graph checkpoint stays the fallback for pre-0018 runs and for tests
		// that run without a registered backend.
		if summaries == nil {
			summaries = s.findAllScanSummaries
		}
		dir, dirOK := s.resolveScanDirByIDFrom(instanceID, summaries)
		checkpointOK := false
		if dirOK {
			if view, ok := agent.DelegationProjectionFromCheckpoint(dir); ok {
				delegation = view
				checkpointOK = true
				empty.PartialReason = "checkpoint"
			}
		}
		if len(history) > 0 {
			delegation = delegationFromWorkerRows(history, delegation)
			empty.PartialReason = ""
		} else if !checkpointOK {
			if !dirOK {
				empty.PartialReason = "no_run"
			} else {
				// No delegation ever happened in this run (single-agent scan).
				empty.Available = true
				empty.Concurrency = concurrencyView{Limit: 0, Running: 0, Queued: 0}
			}
			// The graph can still hold intents that were never dispatched (a
			// run stopped with a full frontier). Those units are visible state:
			// show them as queued workers instead of "no workers at all".
			if units := s.workerUnits(instanceID, nil); len(units) > 0 {
				empty.Available = true
				empty.PartialReason = ""
				empty.Units = units
			}
			return empty
		}
	}

	claims := claimsByAgent(ledger)
	out := instanceAgentsView{
		Available: delegation.Available,
		Concurrency: concurrencyView{
			Limit:   delegation.Limit,
			Running: delegation.Running,
			Queued:  delegation.Queued,
		},
		Agents:        make([]agentEntryView, 0, len(delegation.Agents)+len(history)),
		PartialReason: empty.PartialReason,
	}
	seen := make(map[string]bool, len(delegation.Agents))
	for _, worker := range delegation.Agents {
		seen[worker.AgentID] = true
		out.Agents = append(out.Agents, agentEntryView{
			DelegatedAgent:    worker,
			ClaimedHypotheses: claims[worker.AgentID],
		})
	}
	// Sibling sessions of the same run (1:1 re-dispatch) contribute rows the
	// live graph never saw. Oldest first, matching the graph's spawn order.
	for i := len(history) - 1; i >= 0; i-- {
		rec := history[i]
		if seen[rec.AgentID] {
			continue
		}
		seen[rec.AgentID] = true
		out.Agents = append(out.Agents, agentEntryView{DelegatedAgent: workerRowToAgent(rec, live)})
	}
	out.Units = s.workerUnits(instanceID, out.Agents)
	return out
}

// workerUnits joins the authoritative intent list onto the instance's agent
// rows. One unit = one visible worker: the graph state is the row status and
// every agent that ran the unit is an execution detail. Graph unavailability
// leaves Units empty (consumers fall back to Agents) instead of failing the
// worker projection.
func (s *Server) workerUnits(instanceID string, agents []agentEntryView) []workerUnitView {
	repo := graph.Backend()
	if repo == nil {
		return []workerUnitView{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	tasks, err := repo.PlanTasks(ctx, s.graphRunID(instanceID))
	cancel()
	if err != nil {
		log.Printf("[agents] unit projection read for %s failed: %v", instanceID, err)
		return []workerUnitView{}
	}
	if len(tasks) == 0 {
		return []workerUnitView{}
	}
	byUnit := make(map[string][]agentEntryView, len(agents))
	for _, entry := range agents {
		if unit := strings.TrimSpace(entry.UnitID); unit != "" {
			byUnit[unit] = append(byUnit[unit], entry)
		}
	}
	units := make([]workerUnitView, 0, len(tasks))
	for _, task := range tasks {
		unitID := strings.TrimSpace(task.TaskID)
		if unitID == "" {
			continue
		}
		unit := workerUnitView{
			UnitID:    unitID,
			Title:     task.Title,
			State:     task.GraphState,
			Status:    workerUnitStatus(task.GraphState),
			VulnClass: task.VulnClass,
			Endpoint:  task.Endpoint,
			Target:    task.Target,
			Phase:     task.Phase,
			Priority:  task.Priority,
			Owner:     task.Owner,
		}
		if !task.UpdatedAt.IsZero() {
			unit.UpdatedAt = task.UpdatedAt.UTC().Format(time.RFC3339)
		} else if !task.CreatedAt.IsZero() {
			unit.UpdatedAt = task.CreatedAt.UTC().Format(time.RFC3339)
		}
		if task.LeaseUntil != nil {
			unit.LeaseUntil = task.LeaseUntil.UTC().Format(time.RFC3339)
		}
		rows := byUnit[unitID]
		for _, row := range rows {
			unit.AgentIDs = append(unit.AgentIDs, row.AgentID)
			unit.Claims = append(unit.Claims, row.ClaimedHypotheses...)
			if unit.Target == "" {
				unit.Target = row.Target
			}
			if row.Started != "" && (unit.StartedAt == "" || row.Started < unit.StartedAt) {
				unit.StartedAt = row.Started
			}
			if row.CompletedAt != "" && row.CompletedAt > unit.CompletedAt {
				unit.CompletedAt = row.CompletedAt
			}
			if row.Error != "" && unit.Error == "" {
				unit.Error = row.Error
			}
		}
		unit.AgentStatus = aggregateAgentStatus(rows)
		// A terminal unit with no agent record has no worker history to open
		// (pre-0029 rows carry no unit_id, and some intents were retired before
		// dispatch). The Plan module is the full intent history; the Workers
		// list only shows units that ran or are still dispatchable.
		if len(unit.AgentIDs) == 0 && unitTerminal(task.GraphState) {
			continue
		}
		units = append(units, unit)
	}
	// Newest work first inside each UI group. The rail renders running/queued/
	// finished sections; without this the oldest intent (PlanTasks phase order)
	// stays at the top while a freshly claimed worker sits at the bottom.
	sort.SliceStable(units, func(i, j int) bool {
		ri, rj := workerUnitRank(units[i].Status), workerUnitRank(units[j].Status)
		if ri != rj {
			return ri < rj
		}
		if units[i].UpdatedAt != units[j].UpdatedAt {
			return units[i].UpdatedAt > units[j].UpdatedAt
		}
		return units[i].UnitID < units[j].UnitID
	})
	return units
}

// workerUnitRank orders active work before queued and finished work.
func workerUnitRank(status string) int {
	switch status {
	case "running", "paused":
		return 0
	case "queued":
		return 1
	default:
		return 2
	}
}

// unitTerminal reports whether the intent state is a finished outcome.
func unitTerminal(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "done", "blocked", "exhausted", "stopped", "deleted":
		return true
	default:
		return false
	}
}

// workerUnitStatus maps the graph intent state onto the worker-row status the
// panel renders. Unknown states pass through and the UI shows the raw value.
func workerUnitStatus(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "open":
		return "queued"
	case "running":
		return "running"
	case "paused":
		return "paused"
	case "done":
		return "completed"
	case "blocked", "exhausted", "stopped", "deleted":
		return "failed"
	default:
		return state
	}
}

// aggregateAgentStatus reports the live execution state across a unit's
// attempts: running wins, then queued, then the newest terminal status.
func aggregateAgentStatus(rows []agentEntryView) string {
	status := ""
	for _, row := range rows {
		switch row.Status {
		case "running":
			return "running"
		case "queued":
			status = "queued"
		case "completed", "failed":
			// Attempts are ordered oldest-first: the newest terminal state is
			// the one that actually settled the unit's last execution.
			status = row.Status
		}
	}
	return status
}

// workerHistory reads this run's durable worker rows; an unavailable backend
// or a failed read degrades to the live/checkpoint projection.
func (s *Server) workerHistory(instanceID string) []runstate.WorkerRecord {
	backend := currentWorkerStateBackend()
	if backend == nil {
		return nil
	}
	ctx, cancel := eventStateCtx()
	defer cancel()
	rows, err := backend.ListWorkersByInstance(ctx, instanceID)
	if err != nil {
		log.Printf("[agents] worker history read for %s failed: %v", instanceID, err)
		return nil
	}
	return rows
}

// workerHistoryBatch is the optional batch seam on top of WorkerStateBackend.
// The task-scoped rail lists every run of a task; reading one run at a time is
// N round trips to PG for a request the operator waits on before first paint.
type workerHistoryBatch interface {
	ListWorkersByInstances(ctx context.Context, instanceIDs []string) (map[string][]runstate.WorkerRecord, error)
}

// workerHistories loads durable worker rows for several runs in one query when
// the backend supports it, falling back to per-run reads otherwise (tests and
// pre-batch backends). The bool reports whether the caller may treat a missing
// map entry as "this run has no rows" (loaded) versus "not loaded".
func (s *Server) workerHistories(instanceIDs []string) (map[string][]runstate.WorkerRecord, bool) {
	backend := currentWorkerStateBackend()
	if backend == nil || len(instanceIDs) == 0 {
		return nil, false
	}
	ctx, cancel := eventStateCtx()
	defer cancel()
	if batch, ok := backend.(workerHistoryBatch); ok {
		out, err := batch.ListWorkersByInstances(ctx, instanceIDs)
		if err == nil {
			return out, true
		}
		log.Printf("[agents] batch worker history read failed, falling back per run: %v", err)
	}
	out := make(map[string][]runstate.WorkerRecord, len(instanceIDs))
	for _, id := range instanceIDs {
		rows, err := backend.ListWorkersByInstance(ctx, id)
		if err != nil {
			log.Printf("[agents] worker history read for %s failed: %v", id, err)
			continue
		}
		out[id] = rows
	}
	return out, true
}

// delegationFromWorkerRows merges the checkpoint projection with the run's
// durable worker rows for a terminal run. The checkpoint covers the current
// session's spawn order (and the fields rows may not carry); PG rows cover
// every session of the run and are the lifecycle authority (charter §7: run
// state is PG), so a worker present in both keeps its checkpoint position but
// takes the row's terminal status/error/timestamps. Rows the checkpoint never
// saw (earlier sibling sessions) are appended oldest-first.
//
// Before this merge the function rebuilt the view from rows alone, dropping
// every checkpoint-only worker and its spawn order.
func delegationFromWorkerRows(rows []runstate.WorkerRecord, base agent.DelegationView) agent.DelegationView {
	view := agent.DelegationView{
		Available: true,
		Limit:     base.Limit,
		MaxAgents: base.MaxAgents,
		Agents:    make([]agent.DelegatedAgent, 0, len(base.Agents)+len(rows)),
	}
	rowsByID := make(map[string]runstate.WorkerRecord, len(rows))
	for _, rec := range rows {
		rowsByID[rec.AgentID] = rec
	}
	seen := make(map[string]bool, len(base.Agents)+len(rows))
	for _, worker := range base.Agents {
		seen[worker.AgentID] = true
		if rec, ok := rowsByID[worker.AgentID]; ok {
			row := workerRowToAgent(rec, false)
			if row.Status != "" {
				worker.Status = row.Status
			}
			worker.Error = row.Error
			if row.UnitID != "" {
				worker.UnitID = row.UnitID
			}
			if row.Target != "" {
				worker.Target = row.Target
			}
			if row.Started != "" {
				worker.Started = row.Started
			}
			if row.CompletedAt != "" {
				worker.CompletedAt = row.CompletedAt
			}
			if worker.Task == "" {
				worker.Task = row.Task
			}
		} else {
			// Terminal run and no row at all: a checkpoint still claiming
			// running/queued died before its completion boundary. Render it
			// failed instead of an eternal spinner (same rule as a stale row).
			switch worker.Status {
			case "running", "queued":
				worker.Status = "failed"
				if worker.Error == "" {
					worker.Error = "run ended before this worker finished"
				}
			}
		}
		countWorkerConcurrency(&view, worker.Status)
		view.Agents = append(view.Agents, worker)
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if seen[rows[i].AgentID] {
			continue
		}
		seen[rows[i].AgentID] = true
		worker := workerRowToAgent(rows[i], false)
		countWorkerConcurrency(&view, worker.Status)
		view.Agents = append(view.Agents, worker)
	}
	return view
}

// countWorkerConcurrency tallies the merged (post-normalisation) status, so
// the counters agree with the rows the panel renders.
func countWorkerConcurrency(view *agent.DelegationView, status string) {
	switch status {
	case "running":
		view.Running++
	case "queued":
		view.Queued++
	}
}

// workerRowToAgent projects one durable row onto the panel's worker shape.
// In a run that is no longer live, a row still marked running/queued is stale
// (the process died before the completion boundary) and renders as failed
// instead of an eternal spinner.
func workerRowToAgent(rec runstate.WorkerRecord, live bool) agent.DelegatedAgent {
	status := rec.Status
	errText := rec.Error
	if !live && (status == "running" || status == "queued") {
		status = "failed"
		if errText == "" {
			errText = "run ended before this worker finished"
		}
	}
	worker := agent.DelegatedAgent{
		AgentID: rec.AgentID,
		Lane:    rec.Lane,
		UnitID:  rec.UnitID,
		Status:  status,
		Task:    rec.Task,
		Target:  rec.Target,
		Error:   errText,
	}
	if !rec.StartedAt.IsZero() {
		worker.Started = rec.StartedAt.UTC().Format(time.RFC3339)
	}
	if !rec.FinishedAt.IsZero() {
		worker.CompletedAt = rec.FinishedAt.UTC().Format(time.RFC3339)
	}
	return worker
}

// claimsByAgent groups the ledger's currently-owned hypotheses by agent id.
// Only in-flight ownership (testing) is attached: proven/rejected rows are
// history and belong to the ledger pane, not the worker row.
func claimsByAgent(ledger *scanctx.LedgerStore) map[string][]claimedHypothesisView {
	if ledger == nil {
		return nil
	}
	out := make(map[string][]claimedHypothesisView)
	for _, hyp := range ledger.All() {
		owner := strings.TrimSpace(hyp.AssignedTo)
		if owner == "" || hyp.Status != scanctx.HypothesisTesting {
			continue
		}
		out[owner] = append(out[owner], claimedHypothesisView{
			ID:        hyp.ID,
			VulnClass: hyp.VulnClass,
			Endpoint:  hyp.Endpoint,
			Status:    string(hyp.Status),
		})
	}
	return out
}
