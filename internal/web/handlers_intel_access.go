package web

// handlers_intel_access.go — the read projection of the intel reachability
// layer (INTEL_HANDOFF_SPEC M2/M3/M4):
//
//	GET /api/scans/{id}/intel-access — access rows (state + grants), issued
//	                                  capabilities and the newest audit rows.
//
// Read-only. The projection's authorities are the tables themselves; this
// endpoint exists so the panel and the report can show "what doors the run
// holds, who approved them, and what was used" without re-deriving anything.

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/intelaccess"
)

const intelAccessReadTimeout = 5 * time.Second

func (s *Server) handleScanIntelAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/intel-access")
	scanID = strings.TrimSpace(scanID)
	if scanID == "" || strings.Contains(scanID, "/") {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "scan id is required"})
		return
	}
	store := intelaccess.StoreOrNil()
	if store == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{
			"error": "intel projection store is not available",
		})
		return
	}
	runID := s.graphRunID(scanID)
	ctx, cancel := context.WithTimeout(r.Context(), intelAccessReadTimeout)
	defer cancel()

	access, err := store.ListAccess(ctx, runID)
	if err != nil {
		log.Printf("[intel] scan %s: access projection read failed: %v", scanID, err)
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error": "intel access read failed", "detail": err.Error(),
		})
		return
	}
	capabilities, err := store.ListScanCapabilities(ctx, runID, 200)
	if err != nil {
		log.Printf("[intel] scan %s: capability read failed: %v", scanID, err)
		capabilities = nil
	}
	uses, err := store.ListUses(ctx, runID, 200)
	if err != nil {
		log.Printf("[intel] scan %s: audit read failed: %v", scanID, err)
		uses = nil
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"scan_id":      scanID,
		"run_id":       runID,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"access":       access,
		"capabilities": capabilities,
		"uses":         uses,
	})
}
