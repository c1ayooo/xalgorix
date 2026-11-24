package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/assets"
)

// handleScanAssets serves GET /api/scans/{id}/assets — the assets this run
// linked, read from the authoritative asset graph. Read-only.
func (s *Server) handleScanAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/assets")
	if scanID == "" || strings.Contains(scanID, "/") {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "scan id is required"})
		return
	}
	repo := assets.Backend()
	if repo == nil {
		writeJSONStatus(w, http.StatusOK, map[string]any{
			"scan_id": scanID, "source": "none", "items": []assets.Asset{}, "total": 0,
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, err := repo.ListTaskAssets(ctx, s.graphRunID(scanID))
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error": "asset read failed", "detail": err.Error(), "scan_id": scanID,
		})
		return
	}
	if items == nil {
		items = []assets.Asset{}
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"scan_id": scanID, "source": "assets", "items": items, "total": len(items),
	})
}
