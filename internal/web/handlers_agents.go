package web

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
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

type instanceAgentsView struct {
	Available     bool             `json:"available"`
	Concurrency   concurrencyView  `json:"concurrency"`
	Agents        []agentEntryView `json:"agents"`
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
	empty := instanceAgentsView{Agents: []agentEntryView{}}

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
	history := s.workerHistory(instanceID)

	if !live {
		// Terminal / released run. PG rows are the authority when present; the
		// graph checkpoint stays the fallback for pre-0018 runs and for tests
		// that run without a registered backend.
		dir, dirOK := s.resolveScanDirByID(instanceID)
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
	return out
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

// delegationFromWorkerRows rebuilds the worker pool projection from durable
// rows (newest-updated first on input, rendered oldest-first). Limit/MaxAgents
// are kept from the checkpoint when one was readable.
func delegationFromWorkerRows(rows []runstate.WorkerRecord, base agent.DelegationView) agent.DelegationView {
	view := agent.DelegationView{
		Available: true,
		Limit:     base.Limit,
		MaxAgents: base.MaxAgents,
		Agents:    make([]agent.DelegatedAgent, 0, len(rows)),
	}
	for i := len(rows) - 1; i >= 0; i-- {
		worker := workerRowToAgent(rows[i], false)
		switch worker.Status {
		case "running":
			view.Running++
		case "queued":
			view.Queued++
		}
		view.Agents = append(view.Agents, worker)
	}
	return view
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
