package web

// graph_guidance.go — project human guidance as exploration hint nodes (R7/M1).
//
// Hints are the charter's human-input ring (§2): only source=human guidance
// becomes a hint node; engine obligations are never written here. The node
// carries the guidance id as its unit key so a retry/status flip is idempotent.

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// projectGuidanceInfoItem writes delivered operator guidance into the
// information space as an observed, human-sourced InfoItem (charter §2/§8:
// human input is a source=human observation). It is idempotent per guidance
// row, audit-only, and never blocks delivery: a projection failure is logged
// and the run continues.
func (s *Server) projectGuidanceInfoItem(sctx *scanctx.ScanContext, g platform.Guidance) {
	if sctx == nil || sctx.Ledger == nil {
		return
	}
	if err := sctx.Ledger.AppendGuidanceObservation(scanctx.GuidanceObservation{
		ID:         g.ID,
		TaskID:     g.TaskID,
		RunID:      g.RunID,
		InstanceID: g.InstanceID,
		CreatedBy:  g.CreatedBy,
		Message:    g.Message,
	}); err != nil {
		log.Printf("[platform] guidance %s InfoItem projection failed: %v", g.ID, err)
	}
}

func (s *Server) projectGuidanceHint(ctx context.Context, g *platform.Guidance) {
	if g == nil || strings.TrimSpace(g.ID) == "" || strings.TrimSpace(g.InstanceID) == "" {
		return
	}
	repo := graph.Backend()
	if repo == nil {
		return
	}
	state := graph.StateActive
	if g.Status == platform.GuidanceConsumed {
		state = graph.StateConsumed
	}
	payload, _ := json.Marshal(map[string]any{
		"guidance_id": g.ID,
		"task_id":     g.TaskID,
		"run_id":      g.RunID,
		"instance_id": g.InstanceID,
		"message":     g.Message,
		"status":      g.Status,
		"created_by":  g.CreatedBy,
		"created_at":  g.CreatedAt,
	})
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = repo.UpsertNode(cctx, graph.NodeInput{
		ScanID:   s.graphRunID(g.InstanceID),
		Kind:     graph.KindHint,
		UnitKey:  g.ID,
		State:    state,
		Priority: 50,
		Origin:   "human",
		Payload:  string(payload),
	})
}
