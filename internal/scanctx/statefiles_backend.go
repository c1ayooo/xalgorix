// statefiles_backend.go — the bounded state documents' persistence backend
// seam (charter v3.0 §6.3, REDESIGN D12): the run_state_files rows
// (kind ∈ {notes, coverage}) are the durable home of the NoteStore map and
// the CoverageStore matrix instead of notes.json / coverage.json. The
// in-memory stores stay the run-time authority.
//
// The backend is process-global infrastructure, registered once at startup
// (same pattern as LedgerBackend). nil keeps the legacy file path —
// production main always registers a backend, so the file path effectively
// serves test harnesses only. The R6.1 wiring slice registers it; until
// then every store here stays in file mode.
//
// Failure discipline (charter §6.1/§6.4): a persist failure is returned to
// the mutation caller (persist-failure is not success, §5.2) and recorded on
// the run-state watermark by the implementation; the in-memory state stays
// authoritative and the next successful mutation re-persists the full
// snapshot.
package scanctx

import "sync"

// The run_state_files row kinds this seam persists. Must stay in lockstep
// with the engine-owned vocabulary in runstate (KindNotes / KindCoverage) —
// kept as local constants so scanctx does not import runstate (the PG store
// implements this interface by duck typing, raw snapshot bytes only).
const (
	StateFileKindNotes    = "notes"
	StateFileKindCoverage = "coverage"
)

// StateFilesBackend is the durable home of the bounded state documents.
// One interface serves both families: kind selects the run_state_files row.
type StateFilesBackend interface {
	// SaveStateFileSnapshot rewrites the durable snapshot for one scan
	// (whole-snapshot semantics; the documents are bounded). kind is the
	// row discriminator (StateFileKindNotes / StateFileKindCoverage). seq
	// is the store's write counter for watermark comparisons.
	SaveStateFileSnapshot(scanID, kind, instanceID string, seq int64, data []byte) error

	// LoadStateFileSnapshot returns the scan's own durable document.
	LoadStateFileSnapshot(scanID, kind string) (data []byte, seq int64, ok bool, err error)

	// LoadLatestStateFileSnapshotForInstance returns the newest document of
	// one kind across a run's sibling sessions — the D11 continuity source:
	// a re-dispatched session has no document of its own yet and inherits
	// the previous session's state instead of starting empty. source is the
	// owning scan id of the returned document (for honest load logs).
	LoadLatestStateFileSnapshotForInstance(instanceID, excludeScanID, kind string) (data []byte, seq int64, source string, ok bool, err error)
}

var (
	stateFilesBackendMu sync.RWMutex
	stateFilesBackend   StateFilesBackend
)

// SetStateFilesBackend registers the process-wide state-files persistence
// backend. Pass nil to restore the legacy file behavior (tests only).
func SetStateFilesBackend(b StateFilesBackend) {
	stateFilesBackendMu.Lock()
	defer stateFilesBackendMu.Unlock()
	stateFilesBackend = b
}

// CurrentStateFilesBackend returns the registered backend or nil.
func CurrentStateFilesBackend() StateFilesBackend {
	stateFilesBackendMu.RLock()
	defer stateFilesBackendMu.RUnlock()
	return stateFilesBackend
}
