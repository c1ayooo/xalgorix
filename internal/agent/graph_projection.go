package agent

// graph_projection.go — M1-5: project the authoritative ledger/findings/
// objective into the exploration graph.
//
// The graph is the authority for node lifecycle and lineage, but the source
// facts keep their own field authorities: the ledger owns evidence detail,
// reporting owns accepted findings, and objective.Evaluate owns goal met.
// This projector only mirrors them (idempotent by unit_key); it never
// decides a verdict or writes a finding directly.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/objective"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

// syncExplorationGraph is a best-effort turn-boundary projection. It runs on
// the root agent only (workers share the ledger; the root's next turn folds
// their results). A PG failure is logged through the agent's error event by
// the caller path and never blocks the loop.
func (a *Agent) syncExplorationGraph() {
	if a == nil || a.scanCtx == nil || a.state == nil || a.delegatedAgentID != "" {
		return
	}
	repo := graph.Backend()
	runID := a.planGraphScanID()
	if repo == nil || runID == "" {
		return
	}
	ctx, cancel := planGraphContext()
	defer cancel()

	sctx := a.scanCtx
	beliefNodes := map[string]int64{}
	factNodes := map[string]int64{}

	// Beliefs + evidence facts.
	if sctx.Ledger != nil {
		all := sctx.Ledger.All()
		for i := range all {
			h := &all[i]
			payload := beliefPayload(h)
			node, err := repo.UpsertNode(ctx, graph.NodeInput{
				ScanID:     runID,
				Kind:       graph.KindBelief,
				UnitKey:    h.ID,
				State:      beliefGraphState(h.Status),
				Priority:   int(h.Confidence * 100),
				Origin:     h.Origin,
				Owner:      h.AssignedTo,
				LeaseUntil: timePtr(h.LeaseUntil),
				Payload:    payload,
			})
			if err != nil || node == nil {
				continue
			}
			beliefNodes[h.ID] = node.ID
			linkPayloadInfoItems(ctx, repo, runID, node.ID, payload)
			for j := range h.Evidence {
				ev := &h.Evidence[j]
				if strings.TrimSpace(ev.ID) == "" {
					continue
				}
				payload := factPayload(h, ev)
				fact, ferr := repo.UpsertNode(ctx, graph.NodeInput{
					ScanID:   runID,
					Kind:     graph.KindFact,
					UnitKey:  ev.ID,
					State:    graph.StateConfirmed,
					Priority: int(ev.Confidence * 100),
					Origin:   ev.AgentID,
					Payload:  payload,
				})
				if ferr != nil || fact == nil {
					continue
				}
				factNodes[ev.ID] = fact.ID
				linkPayloadInfoItems(ctx, repo, runID, fact.ID, payload)
				_ = repo.AddEdge(ctx, graph.EdgeInput{
					ScanID: runID, SrcID: fact.ID, DstID: node.ID, Rel: graph.RelSupports,
				})
			}
		}
	}

	// Goals from the deterministic objective evaluator.
	goalNodes := map[string]int64{}
	findingProofs := map[string][]int64{}
	if a.state.Objective != nil && len(a.state.Objective.Criteria) > 0 {
		report := objective.Evaluate(objectiveSpecFromAgent(a.state.Objective), objectiveSignalForState(a.state))
		for i, c := range a.state.Objective.Criteria {
			id := strings.TrimSpace(c.ID)
			if id == "" {
				id = fmt.Sprintf("criterion-%d", i)
			}
			state := graph.StateOpen
			reason, findingID, evidenceTag := "", "", ""
			if i < len(report.Criteria) {
				if report.Criteria[i].State == objective.StateMet {
					state = graph.StateMet
				}
				reason = report.Criteria[i].Reason
				findingID = report.Criteria[i].EvidenceFindingID
				evidenceTag = report.Criteria[i].EvidenceTag
			}
			payload, _ := json.Marshal(map[string]any{
				"criterion_id":        id,
				"label":               criterionLabel(c),
				"kind":                criterionKindDetail(c),
				"vuln_class":          c.VulnClass,
				"capability":          c.Capability,
				"verify_by":           c.VerifyBy,
				"required":            c.Required,
				"state":               string(state),
				"reason":              reason,
				"evidence_finding_id": findingID,
				"evidence_tag":        evidenceTag,
			})
			node, err := repo.UpsertNode(ctx, graph.NodeInput{
				ScanID: runID, Kind: graph.KindGoal, UnitKey: id,
				State: state, Priority: map[bool]int{true: 100, false: 50}[c.Required],
				Origin: "objective", Payload: string(payload),
			})
			if err != nil || node == nil {
				continue
			}
			goalNodes[id] = node.ID
			if findingID != "" {
				findingProofs[findingID] = append(findingProofs[findingID], node.ID)
			}
		}
	}

	// Accepted findings. Only report_vulnerability can create these rows.
	findingNodes := map[string]int64{}
	for _, v := range reporting.GetVulnerabilitiesForContext(sctx.ID) {
		if strings.TrimSpace(v.ID) == "" {
			continue
		}
		payload, _ := json.Marshal(map[string]any{
			"finding_id": v.ID,
			"title":      v.Title,
			"severity":   v.Severity,
			"endpoint":   v.Endpoint,
			"method":     v.Method,
			"cvss":       v.CVSS,
			"verified":   v.Verified,
			"tags":       v.Tags,
			"agent_name": v.AgentName,
		})
		node, err := repo.UpsertNode(ctx, graph.NodeInput{
			ScanID: runID, Kind: graph.KindFinding, UnitKey: v.ID,
			State: graph.StateConfirmed, Priority: severityPriority(v.Severity),
			Origin: "report", Payload: string(payload),
		})
		if err != nil || node == nil {
			continue
		}
		findingNodes[v.ID] = node.ID
		linkPayloadInfoItems(ctx, repo, runID, node.ID, string(payload))
		for _, goalID := range findingProofs[v.ID] {
			_ = repo.AddEdge(ctx, graph.EdgeInput{
				ScanID: runID, SrcID: node.ID, DstID: goalID, Rel: graph.RelProves,
			})
		}
	}

	// Goal -> intent spawns edges from ObjectiveRef (lineage only).
	if len(goalNodes) > 0 {
		if tasks, err := repo.PlanTasks(ctx, runID); err == nil {
			for _, t := range tasks {
				goalID := strings.TrimSpace(t.ObjectiveRef)
				goalNode, ok := goalNodes[goalID]
				if !ok {
					continue
				}
				_ = repo.AddEdge(ctx, graph.EdgeInput{
					ScanID: runID, SrcID: goalNode, DstID: t.NodeID, Rel: graph.RelSpawns,
				})
			}
		}
	}
	// Asset identities derived from the engine's observed surface.
	a.syncAssets()
	// Conformance after every projection: prove the field authorities' full
	// ID sets are all present as graph nodes. Read-only; drift is logged and
	// surfaced by /api/scans/{id}/graph/validate, never auto-repaired.
	a.checkProjectionConformance(ctx, repo, runID)
}

// checkProjectionConformance reconciles ledger/reporting/objective ID sets
// against the graph projection. Empty expected sets are skipped by the
// checker, so an early turn that has not produced a finding is not drift.
func (a *Agent) checkProjectionConformance(ctx context.Context, repo graph.Repository, runID string) {
	sctx := a.scanCtx
	if sctx == nil || repo == nil || runID == "" {
		return
	}
	// The reconciliation lists every node of every kind; throttle it so a
	// fast turn loop does not turn the audit into load. Drift is structural
	// and does not need sub-second detection.
	const minInterval = 30 * time.Second
	a.conformanceMu.Lock()
	if !a.lastConformanceCheck.IsZero() && time.Since(a.lastConformanceCheck) < minInterval {
		a.conformanceMu.Unlock()
		return
	}
	a.lastConformanceCheck = time.Now()
	a.conformanceMu.Unlock()

	expected := map[graph.Kind][]string{}
	if sctx.Ledger != nil {
		all := sctx.Ledger.All()
		for i := range all {
			h := &all[i]
			if strings.TrimSpace(h.ID) == "" {
				continue
			}
			expected[graph.KindBelief] = append(expected[graph.KindBelief], h.ID)
			for j := range h.Evidence {
				if id := strings.TrimSpace(h.Evidence[j].ID); id != "" {
					expected[graph.KindFact] = append(expected[graph.KindFact], id)
				}
			}
		}
	}
	for _, v := range reporting.GetVulnerabilitiesForContext(sctx.ID) {
		if id := strings.TrimSpace(v.ID); id != "" {
			expected[graph.KindFinding] = append(expected[graph.KindFinding], id)
		}
	}
	if a.state.Objective != nil {
		for i, c := range a.state.Objective.Criteria {
			id := strings.TrimSpace(c.ID)
			if id == "" {
				id = fmt.Sprintf("criterion-%d", i)
			}
			expected[graph.KindGoal] = append(expected[graph.KindGoal], id)
		}
	}
	counts, violations, err := graph.CheckProjection(ctx, repo, runID, expected)
	if err != nil {
		log.Printf("[graph-conformance] projection check failed: %v", err)
		return
	}
	errors, warnings := 0, 0
	for _, v := range violations {
		if v.Severity == graph.SeverityError {
			errors++
		} else {
			warnings++
		}
	}
	// Persist the snapshot even when clean: "checked and clean" is an audit
	// fact, and the next batch (or an operator) can tell a missing check from
	// a passing one. Recording is read-only and best-effort.
	if rec, ok := repo.(graph.ConformanceRecorder); ok {
		rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := rec.RecordConformance(rctx, graph.Report{
			ScanID:     runID,
			CheckedAt:  time.Now().UTC(),
			OK:         errors == 0,
			Errors:     errors,
			Warnings:   warnings,
			Violations: violations,
			Projection: &counts,
		}); err != nil {
			log.Printf("[graph-conformance] record snapshot failed: %v", err)
		}
		rcancel()
	}
	if len(violations) == 0 {
		return
	}
	log.Printf("[graph-conformance] run=%s expected=%d observed=%d missing=%d stale=%d errors=%d warnings=%d",
		runID, counts.Expected, counts.Observed, len(counts.Missing), len(counts.Stale), errors, warnings)
}

func beliefPayload(h *scanctx.Hypothesis) string {
	payload, _ := json.Marshal(map[string]any{
		"hypothesis_id":      h.ID,
		"subject":            beliefSubject(h),
		"title":              h.Title,
		"vuln_class":         h.VulnClass,
		"endpoint":           h.Endpoint,
		"parameter":          h.Parameter,
		"role":               h.Role,
		"status":             string(h.Status),
		"confidence":         h.Confidence,
		"voi":                h.VOI,
		"voi_estimated":      h.VOIEstimated,
		"assigned_to":        h.AssignedTo,
		"next_action":        h.NextAction,
		"attempts":           h.Attempts,
		"origin":             h.Origin,
		"preconditions":      h.Preconditions,
		"required_privilege": h.RequiredPrivilege,
		"baseline":           h.Baseline,
		"evidence_count":     len(h.Evidence),
	})
	return string(payload)
}

func factPayload(h *scanctx.Hypothesis, ev *scanctx.Evidence) string {
	payload, _ := json.Marshal(map[string]any{
		"evidence_id":   ev.ID,
		"hypothesis_id": h.ID,
		"kind":          ev.Kind,
		"summary":       graphSummary(ev.Summary),
		"nature":        string(ev.Nature),
		"supports":      ev.Supports,
		"confidence":    ev.Confidence,
		"info_item_id":  ev.InfoItemID,
		"finding_id":    ev.FindingID,
		"source":        ev.Source,
		"agent_id":      ev.AgentID,
	})
	return string(payload)
}

// linkPayloadInfoItems records aggregate→atom links (IG3) for the InfoItem
// references a projected payload already carries: the scalar `info_item_id`
// and the `info_item_ids` array. Links are a read model, not state authority,
// so a write failure is logged and the projection continues. Only atom IDs
// cross the boundary; bodies stay in the InfoItem cold path.
func linkPayloadInfoItems(ctx context.Context, repo graph.Repository, runID string, nodeID int64, payload string) {
	writer, ok := repo.(graph.InfoLinkWriter)
	if !ok || nodeID <= 0 {
		return
	}
	for _, id := range payloadInfoItemIDs(payload) {
		if err := writer.UpsertInfoLink(ctx, runID, nodeID, id, graph.InfoLinkRoleProvenance); err != nil {
			log.Printf("[graph-projection] info link node=%d atom=%s failed: %v", nodeID, id, err)
		}
	}
}

// payloadInfoItemIDs extracts the scalar and array atom references from a
// payload, preserving order and dropping blanks/duplicates.
func payloadInfoItemIDs(payload string) []string {
	if strings.TrimSpace(payload) == "" {
		return nil
	}
	var raw struct {
		InfoItemID  string   `json:"info_item_id"`
		InfoItemIDs []string `json:"info_item_ids"`
	}
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	add(raw.InfoItemID)
	for _, id := range raw.InfoItemIDs {
		add(id)
	}
	return out
}

func beliefGraphState(status scanctx.HypothesisStatus) graph.State {
	switch status {
	case scanctx.HypothesisTesting:
		return graph.StateTesting
	case scanctx.HypothesisProven:
		return graph.StateProven
	case scanctx.HypothesisRejected:
		return graph.StateRejected
	case scanctx.HypothesisBlocked:
		return graph.StateBlocked
	case scanctx.HypothesisExhausted:
		return graph.StateExhausted
	default:
		return graph.StateQueued
	}
}

// graphSummary bounds the L0 summary stored in a fact node. Full evidence
// detail stays in the ledger; the graph is an index, not a copy.
func graphSummary(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= 240 {
		return string(r)
	}
	return string(r[:240]) + "…"
}

func severityPriority(sev string) int {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical":
		return 100
	case "high":
		return 80
	case "medium":
		return 60
	case "low":
		return 40
	default:
		return 20
	}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func beliefSubject(h *scanctx.Hypothesis) string {
	if h == nil {
		return ""
	}
	if ep := strings.TrimSpace(h.Endpoint); ep != "" {
		return "endpoint:" + strings.ToLower(ep)
	}
	return strings.ToLower(strings.TrimSpace(h.VulnClass))
}
