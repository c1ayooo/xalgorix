package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
)

// graphNodeView is one exploration node as the panel sees it. Payload stays
// available as raw text for forward compatibility; PayloadObject is the
// decoded structure the UI/planner normally consumes.
type graphNodeView struct {
	graph.Node
	PayloadObject map[string]any `json:"payload_object,omitempty"`
}

// graphRunID maps a request id (instance id or legacy scan id) to the
// run-scoped graph key. The UI normally passes run.instance_id; legacy links
// pass a scan record id, which resolves through the record's instance stamp.
func (s *Server) graphRunID(scanID string) string {
	scanID = strings.TrimSpace(scanID)
	if scanID == "" {
		return ""
	}
	s.instancesMu.RLock()
	_, live := s.instances[scanID]
	s.instancesMu.RUnlock()
	if live {
		return scanID
	}
	if _, rec := s.findScanByInstanceID(scanID); rec != nil {
		return scanID
	}
	if dir, ok := s.resolveScanDirByID(scanID); ok {
		if rec, ok := loadScanRecordFromDir(dir); ok {
			if id := strings.TrimSpace(rec.InstanceID); id != "" {
				return id
			}
		}
	}
	return scanID
}

// handleScanGraph serves GET /api/scans/{id}/graph — the read projection of
// the authoritative dual graph (exploration nodes/edges + counters). It is
// read-only: planner/worker/main never write through this endpoint.
func (s *Server) handleScanGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/graph")
	if scanID == "" || strings.Contains(scanID, "/") {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "scan id is required"})
		return
	}
	repo := graph.Backend()
	runID := s.graphRunID(scanID)
	if repo == nil {
		writeJSONStatus(w, http.StatusOK, map[string]any{
			"scan_id":  scanID,
			"source":   "none",
			"counters": graph.Counts{},
			"nodes":    []graphNodeView{},
			"edges":    []graph.Edge{},
			"partial":  true,
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	nodes := make([]graphNodeView, 0, 64)
	rawNodes := make([]graph.Node, 0, 64)
	for _, kind := range []graph.Kind{
		graph.KindGoal, graph.KindIntent, graph.KindBelief, graph.KindFact,
		graph.KindFinding, graph.KindHint, graph.KindDigest,
	} {
		items, err := repo.ListNodes(ctx, runID, kind)
		if err != nil {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
				"error":   "graph read failed",
				"detail":  err.Error(),
				"scan_id": scanID,
			})
			return
		}
		for _, n := range items {
			rawNodes = append(rawNodes, n)
			nodes = append(nodes, graphNodeView{Node: n, PayloadObject: n.PayloadJSON()})
		}
	}
	edges, err := repo.ListEdges(ctx, runID)
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "graph edge read failed",
			"detail":  err.Error(),
			"scan_id": scanID,
		})
		return
	}
	counters, err := repo.Counts(ctx, runID)
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "graph counter read failed",
			"detail":  err.Error(),
			"scan_id": scanID,
		})
		return
	}
	// Runtime conformance: the same read-only invariants the operator can
	// request in full from /graph/validate. A failing report never hides the
	// graph; it travels with it.
	conformance := graph.ValidateSnapshot(runID, rawNodes, edges, time.Now().UTC())
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"scan_id":     scanID,
		"source":      "graph",
		"counters":    counters,
		"nodes":       nodes,
		"edges":       edges,
		"conformance": conformance.Summary(),
		"partial":     false,
	})
}

// handleScanGraphValidate serves GET /api/scans/{id}/graph/validate — the
// full read-only conformance report (invariants + counts + truncated flag).
// It never repairs state: drift is surfaced for a human, not rewritten.
func (s *Server) handleScanGraphValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/graph/validate")
	if scanID == "" || strings.Contains(scanID, "/") {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "scan id is required"})
		return
	}
	repo := graph.Backend()
	runID := s.graphRunID(scanID)
	if repo == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "graph unavailable",
			"detail":  "PostgreSQL graph authority is not configured",
			"scan_id": scanID,
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	report, err := graph.ValidateStore(ctx, repo, runID)
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "graph validation failed",
			"detail":  err.Error(),
			"scan_id": scanID,
		})
		return
	}
	writeJSONStatus(w, http.StatusOK, report)
}
