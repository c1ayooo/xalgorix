package web

// Read-only ledger projection for one scan (the "Run 账本" surface):
//
//	GET /api/scans/{id}/ledger — hypothesis/evidence snapshot with status
//	                            counts and honest presence/damage flags.
//
// Strict read, mirroring scan_audit.go discipline: the projection never
// mutates ledger state and never participates in vulnerability judgment.
// The on-disk ledger.json IS the state authority for hypothesis lifecycle
// (charter §5 / §6.3: JSON is the write source), so this endpoint serves the
// authority itself rather than a replayed event view. A missing or damaged
// snapshot is surfaced as absent/damaged — a fabricated "empty ledger" for a
// scan that actually has one is exactly what the charter forbids.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// ledgerFileName is the on-disk snapshot the LedgerStore persists to. Kept in
// step with scanctx (LedgerStore.SetPersistPath joins this name).
const ledgerFileName = "ledger.json"

// scanLedgerHypothesisStatusOrder sorts the operator view: active work first
// (queued/testing/blocked), settled outcomes last (proven/rejected/exhausted),
// most recently updated first inside each group. Pure display ordering — the
// scheduler's VOI ordering is a different concern and is not re-implemented
// here.
var scanLedgerHypothesisStatusOrder = func(st scanctx.HypothesisStatus) int {
	switch st {
	case scanctx.HypothesisTesting:
		return 0
	case scanctx.HypothesisQueued:
		return 1
	case scanctx.HypothesisBlocked:
		return 2
	case scanctx.HypothesisProven:
		return 3
	case scanctx.HypothesisRejected:
		return 4
	case scanctx.HypothesisExhausted:
		return 5
	}
	return 6
}

// scanLedgerView is the API response for GET /api/scans/{id}/ledger.
type scanLedgerView struct {
	ScanID string `json:"scan_id"`
	// LedgerPresent=false means no snapshot exists on disk: either a
	// pre-ledger scan or a run that has not claimed anything yet. The note
	// says so explicitly instead of answering an empty-but-present ledger.
	LedgerPresent bool `json:"ledger_present"`
	// LedgerDamaged means the snapshot exists but does not parse — the tail
	// cannot be trusted as "no hypotheses". Never folded into a clean empty.
	LedgerDamaged bool   `json:"ledger_damaged"`
	Incomplete    bool   `json:"incomplete"`
	Note          string `json:"note,omitempty"`
	// Counts zero-fills every known status so the UI can render all six
	// buckets without inferring absent keys.
	Counts     map[string]int       `json:"counts"`
	Hypotheses []scanctx.Hypothesis `json:"hypotheses"`
}

// handleScanLedger serves GET /api/scans/{id}/ledger.
func (s *Server) handleScanLedger(w http.ResponseWriter, r *http.Request) {
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/ledger")
	if scanID == "" {
		http.Error(w, `{"error":"scan id is required"}`, http.StatusBadRequest)
		return
	}
	dir, ok := s.resolveScanDirByID(scanID)
	if !ok {
		http.Error(w, `{"error":"scan not found"}`, http.StatusNotFound)
		return
	}

	view := scanLedgerView{
		ScanID:     filepath.Base(scanID),
		Counts:     map[string]int{},
		Hypotheses: []scanctx.Hypothesis{},
	}
	// Zero-fill all six statuses so the UI renders every bucket without
	// inferring absent keys. Kept in step with scanctx's status constants.
	for _, st := range []scanctx.HypothesisStatus{
		scanctx.HypothesisQueued,
		scanctx.HypothesisTesting,
		scanctx.HypothesisProven,
		scanctx.HypothesisRejected,
		scanctx.HypothesisBlocked,
		scanctx.HypothesisExhausted,
	} {
		view.Counts[string(st)] = 0
	}

	// R6.2 (REDESIGN D12): the durable snapshot row is the ledger authority
	// once the backend is registered — presence/damage read from the row, and
	// the store anchors on the scan id so its backend read resolves. The
	// legacy ledger.json file below serves pre-import directories and the
	// nil-backend (test) file mode, byte-identical to before.
	ledgerScanID := filepath.Base(filepath.Clean(dir))
	row, fromBackend, rowErr := scanctx.LedgerSnapshotRaw(ledgerScanID)
	if rowErr != nil {
		log.Printf("[ledger] durable snapshot read for %s failed, trying legacy file: %v", ledgerScanID, rowErr)
		fromBackend = false
	}
	if fromBackend {
		view.LedgerPresent = true
		var probe struct {
			Hypotheses []json.RawMessage `json:"hypotheses"`
		}
		if json.Unmarshal(row, &probe) != nil {
			view.LedgerDamaged = true
			view.Incomplete = true
			view.Note = "ledger snapshot exists but does not parse; hypotheses below the damage point are unknown"
			writeJSONStatus(w, http.StatusOK, view)
			return
		}
	} else if _, err := os.Stat(filepath.Join(dir, ledgerFileName)); errors.Is(err, os.ErrNotExist) {
		view.LedgerPresent = false
		view.Incomplete = true
		view.Note = "no ledger snapshot exists for this scan (pre-ledger scan, or the run has not claimed any hypothesis yet)"
		writeJSONStatus(w, http.StatusOK, view)
		return
	}

	store := scanctx.NewLedgerStore()
	store.SetPersistPath(dir)
	if fromBackend {
		store.SetScanID(ledgerScanID)
	}
	loaded := store.LoadFromDisk()
	if loaded == 0 && !fromBackend {
		// LoadFromDisk folds both "empty snapshot" and "unparsable snapshot"
		// into zero. Distinguish them honestly: parse the snapshot shape here
		// so a corrupt file is reported as damaged, never as a clean ledger.
		raw, readErr := os.ReadFile(filepath.Join(dir, ledgerFileName))
		if readErr == nil {
			var probe struct {
				Hypotheses []json.RawMessage `json:"hypotheses"`
			}
			if json.Unmarshal(raw, &probe) != nil {
				view.LedgerPresent = true
				view.LedgerDamaged = true
				view.Incomplete = true
				view.Note = "ledger snapshot exists but does not parse; hypotheses below the damage point are unknown"
				writeJSONStatus(w, http.StatusOK, view)
				return
			}
		}
	}

	hyps := store.All()
	sort.SliceStable(hyps, func(i, j int) bool {
		ri, rj := scanLedgerHypothesisStatusOrder(hyps[i].Status), scanLedgerHypothesisStatusOrder(hyps[j].Status)
		if ri != rj {
			return ri < rj
		}
		return hyps[i].UpdatedAt.After(hyps[j].UpdatedAt)
	})
	for st, n := range store.Counts() {
		view.Counts[string(st)] = n
	}
	view.LedgerPresent = true
	if len(hyps) == 0 {
		view.Hypotheses = []scanctx.Hypothesis{}
	} else {
		view.Hypotheses = hyps
	}
	writeJSONStatus(w, http.StatusOK, view)
}
