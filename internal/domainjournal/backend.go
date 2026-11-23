// backend.go — the domain journal's persistence backend seam (charter v3.0
// §5.2/§6.3, REDESIGN D12, R6.1): the in-memory journal stays the run-time
// sequence authority, and its durable records live in the PG primary store
// (run_journal) instead of domain_journal.jsonl.
//
// The backend is process-global infrastructure, registered once at startup
// (same pattern as scanctx.LedgerBackend). nil keeps the legacy per-scan
// file path — production main always registers a backend, so the file path
// effectively serves test harnesses and pre-R6 directory reads only.
//
// Failure discipline (charter §5.2/§6.1/§6.4): a persist failure is returned
// to the mutation caller (persist-failure is not success) and recorded as a
// failed watermark by the implementation; the failed append leaves its
// sequence reusable, exactly like a failed file append. Read resolution is
// backend first → legacy file fallback: once a backend is registered, the
// file is no longer the durable source, so a backend read error surfaces as
// an error rather than silently reading a stale file (D12 red line).

package domainjournal

import (
	"context"
	"fmt"
	"sync"
)

// JournalSource is one scan's durable journal position in the backend —
// the reconciler's crash catch-up enumeration (charter §6.4): from R6.1
// production journals have no file under the data root, so the durable
// stream must be discoverable through the backend itself.
type JournalSource struct {
	ScanID  string
	LastSeq uint64
}

// Backend is the durable home of journal records. The runstate store
// implements it against run_journal ((scan_id, seq) idempotent, rows
// stamped with the instance id).
type Backend interface {
	// AppendJournalRecord persists ONE record synchronously, idempotent on
	// (scan_id, seq). A failed append leaves the record's sequence reusable
	// — the caller does not advance its watermark.
	AppendJournalRecord(scanID, instanceID string, rec Record) error

	// LoadJournalRecordsByScan returns the scan's own durable records in
	// per-scan seq order. damaged reports rows that failed to decode — the
	// PG alignment of the file journal's truncated/damaged tail contract:
	// one damaged row must never blank the audit history (charter §5.2).
	LoadJournalRecordsByScan(ctx context.Context, scanID string) (records []Record, damaged bool, err error)

	// LoadJournalSources enumerates every scan's durable journal position
	// (max durable seq per scan) for the crash catch-up reconciler.
	LoadJournalSources(ctx context.Context) ([]JournalSource, error)
}

var (
	backendMu sync.RWMutex
	backend   Backend
)

// SetBackend registers the process-wide journal persistence backend.
// Pass nil to restore the legacy file behavior (tests only).
func SetBackend(b Backend) {
	backendMu.Lock()
	defer backendMu.Unlock()
	backend = b
}

// CurrentBackend returns the registered backend or nil.
func CurrentBackend() Backend {
	backendMu.RLock()
	defer backendMu.RUnlock()
	return backend
}

func currentBackend() Backend {
	return CurrentBackend()
}

// ReadRecordsResolved reads one scan's durable journal records in read
// order: the registered backend's stream first, then the legacy per-scan
// file. Callers pass the journal file path they would have read pre-R6
// (becomes the fallback path) and the scan id the journal is keyed by.
//
// The bool mirrors the file reader's truncated flag: with the backend it
// reports rows that failed to decode, aligned with the damaged-tail
// semantics the audit/exploration views already render.
func ReadRecordsResolved(ctx context.Context, path, scanID string) ([]Record, bool, error) {
	if b := currentBackend(); b != nil {
		records, damaged, err := b.LoadJournalRecordsByScan(ctx, scanID)
		if err != nil {
			// PG is the durable authority once registered: an unavailable
			// backend is an honest read failure, never a license to present
			// a stale file as the history (D12 red line).
			return nil, false, fmt.Errorf("domainjournal: backend read %q: %w", scanID, err)
		}
		if len(records) > 0 || damaged {
			return records, damaged, nil
		}
		if path == "" {
			// An empty, undamaged durable stream and no directory anchor at
			// all (a purged session — R6.2 exploration reads key on record
			// ids, not directories): honest "no records", never a synthetic
			// read error.
			return nil, false, nil
		}
		// Empty, undamaged PG stream: fall through to the file — a pre-R6
		// directory whose one-shot import has not produced rows, or a
		// genuinely empty journal. With no file either, both sources agree
		// on "no records".
	}
	return ReadRecords(path)
}
