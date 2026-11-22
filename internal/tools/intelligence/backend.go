// backend.go — the intelligence snapshot's persistence backend seam
// (charter v3.0 §6.3, REDESIGN D12): with a backend registered, snapshots
// live in the run_state_files row (scan_id, kind=intelligence) instead of
// intelligence.json. The in-memory store stays the run-time authority.
//
// The backend is process-global infrastructure, registered once at startup
// (same pattern as scanctx.LedgerBackend / scanctx.StateFilesBackend). nil
// keeps the legacy intelligence.json file path — production main always
// registers a backend, so the file path effectively serves test harnesses
// only. The R6.1 wiring slice registers it; until then the store stays in
// file mode.
//
// Failure discipline (charter §6.1/§6.4): a persist failure is returned to
// the mutation caller through the existing durable-write acknowledgement
// (RecordForContext rolls the item back and reports the failure —
// persist-failure is not success, §5.2) and recorded on the run-state
// watermark by the implementation.
package intelligence

import "sync"

// Backend is the durable home of intelligence snapshots. Shape mirrors
// scanctx.LedgerBackend; the runstate store implements it by duck typing
// (raw snapshot bytes only — the run_state_files row with
// kind=intelligence).
type Backend interface {
	// SaveIntelSnapshot rewrites the durable snapshot for one scan
	// (whole-snapshot semantics; the store is bounded and deduplicated).
	// seq is the store's item counter for watermark comparisons.
	SaveIntelSnapshot(scanID, instanceID string, seq int64, data []byte) error

	// LoadIntelSnapshot returns the scan's own durable snapshot.
	LoadIntelSnapshot(scanID string) (data []byte, seq int64, ok bool, err error)

	// LoadLatestIntelSnapshotForInstance returns the newest snapshot across
	// a run's sibling sessions — the D11 continuity source: a re-dispatched
	// session has no snapshot of its own yet and inherits the previous
	// session's intel instead of starting empty. source is the owning scan
	// id of the returned snapshot (for honest load logs).
	LoadLatestIntelSnapshotForInstance(instanceID, excludeScanID string) (data []byte, seq int64, source string, ok bool, err error)
}

var (
	backendMu sync.RWMutex
	backend   Backend
)

// SetBackend registers the process-wide intelligence persistence backend.
// Pass nil to restore the legacy file behavior (tests only).
func SetBackend(b Backend) {
	backendMu.Lock()
	defer backendMu.Unlock()
	backend = b
}

// currentBackend returns the registered backend or nil.
func currentBackend() Backend {
	backendMu.RLock()
	defer backendMu.RUnlock()
	return backend
}
