package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// Read-only audit surfaces (CORE v2.1 §5.2, §6.4 and the §4.3 "human audit
// view" activation consumer):
//
//	GET /api/scans/{id}/audit     — domain audit projection: claims history,
//	                                operation timeline, duplicate/gap/disorder
//	                                detection with an explicit incomplete flag.
//	GET /api/scans/{id}/coldpath  — cold-path synchronization state: local
//	                                journal position vs the applied watermark,
//	                                reported as pending/applied/gap/failed and
//	                                as "disabled" (never "failed") when no DSN
//	                                is configured.
//
// Both are strict reads. The projection never mutates ledger state and never
// participates in vulnerability judgment (§5.2 item 6). A missing or damaged
// journal is surfaced as incomplete/absent — a fabricated complete history is
// exactly what §5.2 forbids.

// ColdPathPosition is the PostgreSQL-side synchronization position for one
// scan, as reported by the optional cold path service.
type ColdPathPosition struct {
	AppliedSeq uint64    `json:"applied_seq"`
	Status     string    `json:"status"` // pending | applied | gap | failed
	LastError  string    `json:"last_error,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// ColdPathSyncer reports the applied watermark for one scan. The web layer
// owns the state derivation; implementations only supply the durable position.
type ColdPathSyncer interface {
	SyncState(ctx context.Context, scanID string) (ColdPathPosition, error)
}

// ScanAuditView wraps the domain audit projection with journal-file honesty
// flags: a scan predating the journal, or one whose file has a damaged tail,
// must never read as a complete history.
type ScanAuditView struct {
	ScanID         string                    `json:"scan_id"`
	JournalPresent bool                      `json:"journal_present"`
	JournalDamaged bool                      `json:"journal_damaged"`
	Incomplete     bool                      `json:"incomplete"`
	Note           string                    `json:"note,omitempty"`
	Projection     *domainjournal.Projection `json:"projection,omitempty"`
}

// SetColdPathSyncer wires the optional cold path service. Called at startup;
// a nil syncer (the default) means the cold path is disabled.
func (s *Server) SetColdPathSyncer(syncer ColdPathSyncer) {
	s.coldPathSyncerMu.Lock()
	defer s.coldPathSyncerMu.Unlock()
	s.coldPathSyncer = syncer
}

func (s *Server) currentColdPathSyncer() ColdPathSyncer {
	s.coldPathSyncerMu.RLock()
	defer s.coldPathSyncerMu.RUnlock()
	return s.coldPathSyncer
}

// handleScanAudit serves GET /api/scans/{id}/audit.
func (s *Server) handleScanAudit(w http.ResponseWriter, r *http.Request) {
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/audit")
	if scanID == "" {
		http.Error(w, `{"error":"scan id is required"}`, http.StatusBadRequest)
		return
	}
	dir, ok := s.resolveScanDirByID(scanID)
	if !ok {
		http.Error(w, `{"error":"scan not found"}`, http.StatusNotFound)
		return
	}

	view := ScanAuditView{ScanID: filepath.Base(scanID)}
	journalPath := filepath.Join(dir, domainjournal.FileName)
	journalScanID := filepath.Base(dir)
	filePresent := true
	if _, err := os.Stat(journalPath); errors.Is(err, os.ErrNotExist) {
		filePresent = false
	}
	// R6.1 (charter §6.3): the durable source is the PG primary store once
	// the backend is registered; the per-scan file is the legacy fallback
	// (pre-R6 directories). Presence = durable records in either source — a
	// scan with neither must not read as a complete empty history (§5.2).
	records, damaged, err := domainjournal.ReadRecordsResolved(r.Context(), journalPath, journalScanID)
	if err != nil {
		http.Error(w, `{"error":"failed to read domain journal"}`, http.StatusInternalServerError)
		return
	}
	if !filePresent && len(records) == 0 && !damaged {
		view.JournalPresent = false
		view.Incomplete = true
		view.Note = "no domain journal exists for this scan (pre-journal scan or journal disabled); claim history cannot be reconstructed"
		writeJSONStatus(w, http.StatusOK, view)
		return
	}
	view.JournalPresent = true
	view.JournalDamaged = damaged
	proj := domainjournal.Project(view.ScanID, records)
	view.Projection = &proj
	view.Incomplete = proj.Incomplete || damaged
	if damaged {
		view.Note = "journal has a damaged trailing entry; history below the damage is authoritative, the tail is incomplete"
	}
	writeJSONStatus(w, http.StatusOK, view)
}

// handleScanColdPath serves GET /api/scans/{id}/coldpath.
func (s *Server) handleScanColdPath(w http.ResponseWriter, r *http.Request) {
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/coldpath")
	if scanID == "" {
		http.Error(w, `{"error":"scan id is required"}`, http.StatusBadRequest)
		return
	}
	dir, ok := s.resolveScanDirByID(scanID)
	if !ok {
		http.Error(w, `{"error":"scan not found"}`, http.StatusNotFound)
		return
	}

	state := deriveColdPathSyncState(r.Context(), scanID, journalSourceSeq(r.Context(), dir), s.currentColdPathSyncer())
	writeJSONStatus(w, http.StatusOK, state)
}

// deriveColdPathSyncState combines the durable journal's source position
// (resolved through the R6.1 read seam: the PG primary store's stream once
// the backend is registered, the legacy file otherwise — D12) with the
// PostgreSQL watermark. Derivation stays here so the pending/applied/gap/failed
// state machine is contract-testable without a database.
func deriveColdPathSyncState(ctx context.Context, scanID string, sourceSeq uint64, syncer ColdPathSyncer) ColdPathSyncState {
	state := ColdPathSyncState{
		ScanID:    scanID,
		ColdPath:  "disabled",
		SourceSeq: sourceSeq,
		State:     "disabled",
	}
	if syncer == nil {
		return state
	}
	state.ColdPath = "enabled"
	position, err := syncer.SyncState(ctx, scanID)
	if err != nil {
		// A failed watermark READ is an API-level failure, not a sync state:
		// surface it as failed with the reason instead of pretending progress.
		state.State = "failed"
		state.LastError = err.Error()
		return state
	}
	state.AppliedSeq = position.AppliedSeq
	state.LastError = position.LastError
	if !position.UpdatedAt.IsZero() {
		state.UpdatedAt = position.UpdatedAt.UTC().Format(time.RFC3339)
	}
	switch {
	case position.Status == "failed":
		state.State = "failed"
	case position.AppliedSeq >= sourceSeq:
		// Everything durable locally is applied (trivially true for a scan
		// with no journal records).
		state.State = "applied"
	case position.AppliedSeq == 0:
		state.State = "pending"
	default:
		state.State = "gap"
	}
	return state
}

// journalSourceSeq reads the journal's highest durable sequence for the scan
// directory (0 when absent or unreadable — the audit view reports the
// absence honestly; the sync view only needs the position). R6.1: resolved
// through the journal read seam — the PG primary store's stream once the
// backend is registered, the legacy file otherwise.
func journalSourceSeq(ctx context.Context, dir string) uint64 {
	records, _, err := domainjournal.ReadRecordsResolved(ctx, filepath.Join(dir, domainjournal.FileName), filepath.Base(dir))
	if err != nil {
		return 0
	}
	var last uint64
	for _, rec := range records {
		if rec.Seq > last {
			last = rec.Seq
		}
	}
	return last
}

// ColdPathSyncState is the API response for GET /api/scans/{id}/coldpath.
type ColdPathSyncState struct {
	ScanID     string `json:"scan_id"`
	ColdPath   string `json:"cold_path"` // enabled | disabled
	SourceSeq  uint64 `json:"source_seq"`
	AppliedSeq uint64 `json:"applied_seq"`
	State      string `json:"state"` // pending | applied | gap | failed | disabled
	LastError  string `json:"last_error,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}
