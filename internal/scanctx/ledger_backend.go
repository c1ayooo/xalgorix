// ledger_backend.go — the ledger's persistence backend seam (charter v3.0
// §6.3, REDESIGN D12): the in-memory ledger stays the run-time authority, and
// its durable snapshots live in the PG primary store instead of ledger.json.
//
// The backend is process-global infrastructure, registered once at startup
// (same pattern as the infoitem reader). nil keeps the legacy ledger.json
// file path — production main always registers a backend, so the file path
// effectively serves test harnesses only.
//
// Failure discipline (charter §6.1/§6.4): a persist failure is returned to
// the mutation caller (same semantics as the file backend's
// "persist-failure is not success") and recorded on the run-state watermark
// by the implementation; the in-memory state stays authoritative and the
// next successful mutation re-persists the full snapshot.

package scanctx

import "sync"

// LedgerBackend is the durable home of ledger snapshots.
type LedgerBackend interface {
	// SaveLedgerSnapshot rewrites the durable snapshot for one scan
	// (whole-snapshot semantics; the ledger is bounded to dozens of
	// hypotheses). seq is the store's mutation counter for watermark
	// comparisons.
	SaveLedgerSnapshot(scanID, instanceID string, seq int64, data []byte) error

	// LoadLedgerSnapshot returns the scan's own durable snapshot.
	LoadLedgerSnapshot(scanID string) (data []byte, seq int64, ok bool, err error)

	// LoadLatestLedgerSnapshotForInstance returns the newest snapshot across
	// a run's sibling sessions — the D11 continuity source: a re-dispatched
	// session has no snapshot of its own yet and inherits the previous
	// session's chain instead of starting empty. source is the owning scan id
	// of the returned snapshot (for honest load logs).
	LoadLatestLedgerSnapshotForInstance(instanceID, excludeScanID string) (data []byte, seq int64, source string, ok bool, err error)
}

var (
	ledgerBackendMu sync.RWMutex
	ledgerBackend   LedgerBackend
)

// SetLedgerBackend registers the process-wide ledger persistence backend.
// Pass nil to restore the legacy file behavior (tests only).
func SetLedgerBackend(b LedgerBackend) {
	ledgerBackendMu.Lock()
	defer ledgerBackendMu.Unlock()
	ledgerBackend = b
}

// currentLedgerBackend returns the registered backend or nil.
func currentLedgerBackend() LedgerBackend {
	ledgerBackendMu.RLock()
	defer ledgerBackendMu.RUnlock()
	return ledgerBackend
}

// LedgerSnapshotRaw returns the registered backend's raw snapshot row for one
// scan: (data, ok, err). ok=false when no backend is registered or the scan
// has no durable row — callers keep their legacy file behavior in both cases.
// The R6.2 panel reads (GET /api/scans/{id}/ledger, the exploration view and
// the objective signals) use it for the honest presence/damage flags while
// the hypothesis assembly itself keeps going through the store (LoadFromDisk),
// so panel and engine reads stay on one source.
func LedgerSnapshotRaw(scanID string) ([]byte, bool, error) {
	b := currentLedgerBackend()
	if b == nil {
		return nil, false, nil
	}
	data, _, ok, err := b.LoadLedgerSnapshot(scanID)
	if err != nil {
		return nil, false, err
	}
	return data, ok, nil
}
