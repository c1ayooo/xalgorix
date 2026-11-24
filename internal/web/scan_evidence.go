package web

// scan_evidence.go serves the immutable evidence snapshots pinned by the
// report gate (internal/evidence). Every byte is verified against its sha256
// before it is returned: a snapshot that was tampered with is an error, never a
// silently different body.
//
//	GET /api/scans/{scanID}/findings/{findingID}/evidence
//	GET /api/scans/{scanID}/findings/{findingID}/evidence/{snapshotID}/body?side=request|response

import (
	"net/http"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/evidence"
)

// evidencePathParts extracts {scanID}, {findingID}, {snapshotID} from
// /api/scans/{id}/findings/{fid}/evidence[/{sid}/body].
func evidencePathParts(path string) (scanID, findingID, snapshotID string, ok bool) {
	rest := strings.TrimPrefix(path, "/api/scans/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 4 || parts[1] != "findings" || parts[3] != "evidence" {
		return "", "", "", false
	}
	scanID, findingID = parts[0], parts[2]
	if len(parts) >= 5 {
		snapshotID = parts[4]
	}
	return scanID, findingID, snapshotID, scanID != "" && findingID != ""
}

func (s *Server) handleScanEvidence(w http.ResponseWriter, r *http.Request) {
	scanID, findingID, snapshotID, ok := evidencePathParts(r.URL.Path)
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	store := evidence.Default()
	if store == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "evidence store is not configured"})
		return
	}
	scanDir, found := s.resolveScanDirByID(scanID)
	if !found {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "scan not found"})
		return
	}
	if snapshotID != "" {
		s.writeEvidenceBody(w, r, store, scanID, scanDir, findingID, snapshotID)
		return
	}
	snaps, version, err := store.List(r.Context(), scanDir, findingID)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"scan_id":    scanID,
		"finding_id": findingID,
		"version":    version,
		"snapshots":  snaps,
	})
}

func (s *Server) writeEvidenceBody(w http.ResponseWriter, r *http.Request, store *evidence.Store, scanID, scanDir, findingID, snapshotID string) {
	side := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("side")))
	if side != "request" && side != "response" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "side must be request or response"})
		return
	}
	snaps, _, err := store.List(r.Context(), scanDir, findingID)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var snap *evidence.Snapshot
	for i := range snaps {
		if snaps[i].ID == snapshotID {
			snap = &snaps[i]
			break
		}
	}
	if snap == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "evidence snapshot is not bound to this finding"})
		return
	}
	body, err := store.OpenBody(*snap, side)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "evidence verification failed: " + err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Evidence-SHA256", snap.ReqSHA256)
	if side == "response" {
		w.Header().Set("X-Evidence-SHA256", snap.RespSHA256)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
