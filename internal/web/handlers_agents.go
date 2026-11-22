package web

import (
	"net/http"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
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

	if !live {
		// Terminal / released run: rebuild from the graph checkpoint the scanner
		// already writes. Never replay "running" as live work.
		dir, ok := s.resolveScanDirByID(instanceID)
		if !ok {
			empty.PartialReason = "no_run"
			return empty
		}
		view, ok := agent.DelegationProjectionFromCheckpoint(dir)
		if !ok {
			// No delegation ever happened in this run (single-agent scan).
			empty.Available = true
			empty.Concurrency = concurrencyView{Limit: 0, Running: 0, Queued: 0}
			return empty
		}
		delegation = view
		empty.PartialReason = "checkpoint"
	}

	claims := claimsByAgent(ledger)
	out := instanceAgentsView{
		Available: delegation.Available,
		Concurrency: concurrencyView{
			Limit:   delegation.Limit,
			Running: delegation.Running,
			Queued:  delegation.Queued,
		},
		Agents:        make([]agentEntryView, 0, len(delegation.Agents)),
		PartialReason: empty.PartialReason,
	}
	for _, worker := range delegation.Agents {
		out.Agents = append(out.Agents, agentEntryView{
			DelegatedAgent:    worker,
			ClaimedHypotheses: claims[worker.AgentID],
		})
	}
	return out
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
