// scan_state_backend.go — R6.1 state-authority seam for the scan record
// (REDESIGN D12, charter v3.0 §5.1: the runs table takes over scan.json).
//
// The backend is process-global infrastructure registered once at startup
// (web.SetScanStateBackend in cmd/xalgorix, the same pattern as
// scanctx.SetLedgerBackend and SetEventStateBackend); nil keeps the legacy
// scan.json file path byte-for-byte — every existing web test depends on that
// file mode. Production always registers, so with a backend the record safe
// point (saveScanRecordTo) never writes scan.json again: the durable
// run_scan_records row becomes the record authority and the runs mount row's
// takeover columns (migration 0012) carry the instance's newest top-level
// state.
//
// Read resolution (charter §6): the durable row first, the legacy scan.json
// as the fallback for directories whose row the R6.0 importer never landed
// ("未导入残留"), and the file walk as the set-level fallback under the
// platform sweep (findAllScanSummaries).
//
// Failure discipline (charter §5.2/§6.4): a PutScanRecord failure returns
// from the backend (persist-failure is not success), is logged and broadcast
// by the save point, and is recorded on the run_state_watermarks
// "scan_record" stream by the implementation. The in-memory record stays
// authoritative — a failed persist never blocks the running scan.

package web

import (
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
)

// ScanStateBackend is the durable home of one scan record — run_scan_records
// in the PG primary store plus the runs mount row's takeover columns, one
// record keyed by scan_id (the workspace directory basename) with the run
// (instance_id) stamped at the write boundary.
type ScanStateBackend interface {
	// PutScanRecord persists one record save synchronously (idempotent per
	// scan_id). The error returns to the caller — persist-failure is not
	// success; the implementation records the failed watermark.
	PutScanRecord(rec runstate.ScanRecordState) error
	// LoadScanRecord reads one record's durable row. Absent → ok=false.
	LoadScanRecord(ctx context.Context, scanID string) (runstate.ScanRecordState, bool, error)
	// DeleteScanRecord drops one record's durable row (run deletion /
	// retention — PG rows follow the run lifecycle).
	DeleteScanRecord(ctx context.Context, scanID string) error
}

var (
	scanStateBackendMu sync.RWMutex
	scanStateBackend   ScanStateBackend
)

// SetScanStateBackend registers the process-wide scan record backend.
// Pass nil to restore the legacy scan.json file behavior (tests only).
func SetScanStateBackend(b ScanStateBackend) {
	scanStateBackendMu.Lock()
	defer scanStateBackendMu.Unlock()
	scanStateBackend = b
}

func currentScanStateBackend() ScanStateBackend {
	scanStateBackendMu.RLock()
	defer scanStateBackendMu.RUnlock()
	return scanStateBackend
}

// scanStateCtx bounds one backend round trip off the request paths.
func scanStateCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// scanStateIDForDir maps a workspace directory to its durable key: the
// directory basename (the same convention the events journal rows use).
func scanStateIDForDir(dir string) string {
	return filepath.Base(filepath.Clean(dir))
}

// scanStateDirRel maps a workspace directory onto its dataDir-relative
// anchor for the durable row (portable across data-dir relocation). Falls
// back to the absolute path when the data dir is unknown or the path escapes
// it — the reader accepts both forms.
func scanStateDirRel(dataDir, dir string) string {
	dir = filepath.Clean(dir)
	if strings.TrimSpace(dataDir) == "" {
		return dir
	}
	if rel, err := filepath.Rel(filepath.Clean(dataDir), dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return dir
}

// scanStateDirAbs rehydrates a stored anchor against the current data dir.
// An absolute stored path passes through unchanged.
func scanStateDirAbs(dataDir, stored string) string {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return ""
	}
	if filepath.IsAbs(stored) {
		return filepath.Clean(stored)
	}
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Clean(filepath.Join(filepath.Clean(dataDir), stored))
}

// decodeScanRecordSummary decodes a durable record WITHOUT its event log
// (the scanRecordLite discipline: the shadow Events field routes the record's
// event array to a discarded RawMessage instead of decoding thousands of
// WSEvent structs — summaries never read events; EventsJournal stays
// preserved — consumers use the flag to know the journal is the event
// source).
func decodeScanRecordSummary(detail []byte) (*ScanRecord, bool) {
	var lite scanRecordLite
	if err := json.Unmarshal(detail, &lite); err != nil {
		return nil, false
	}
	rec := lite.ScanRecord
	rec.Events = nil
	return &rec, true
}

// decodeScanRecordFull decodes a durable record INCLUDING any embedded event
// log (pre-journal legacy records carry their whole event log inline). The
// journal branch re-seeds Events from the durable journal afterwards — the
// same shape loadScanRecordFromDir's file path produces.
func decodeScanRecordFull(detail []byte) (*ScanRecord, bool) {
	var rec ScanRecord
	if err := json.Unmarshal(detail, &rec); err != nil {
		return nil, false
	}
	return &rec, true
}

// loadScanRecordStateFromBackend reads one workspace directory's record from
// the durable row: metadata decode, then — for journal-backed records — the
// full event journal reseeded over it (byte-identical post-processing to the
// file path in loadScanRecordFromDir). The walkEventJournal call is itself
// backend-aware: it reads run_events (with the legacy file as the pre-import
// fallback), so dir only anchors the fallback.
func loadScanRecordStateFromBackend(b ScanStateBackend, dir string) (*ScanRecord, bool) {
	ctx, cancel := scanStateCtx()
	defer cancel()
	row, ok, err := b.LoadScanRecord(ctx, scanStateIDForDir(dir))
	if err != nil {
		log.Printf("[scan] durable record read for %s failed, trying legacy file: %v", filepath.Base(dir), err)
		return nil, false
	}
	if !ok {
		return nil, false
	}
	rec, ok := decodeScanRecordFull(row.Detail)
	if !ok {
		return nil, false // damaged row: the legacy file fallback decides
	}
	if rec.ID == "" {
		rec.ID = row.ScanID
	}
	if rec.EventsJournal {
		// Non-nil seed: a zero-event journal must marshal as [] not null.
		rec.Events = []WSEvent{}
		total, _, jerr := walkEventJournal(dir, func(_ int, event WSEvent) { rec.Events = append(rec.Events, event) })
		if jerr != nil {
			return nil, false
		}
		rec.EventsTotal = total
		rec.EventsTruncated = false
	}
	return rec, true
}

// scanRecordMetaForDir reads a workspace directory's record WITHOUT its
// event log: the durable row first (R6.1), then the legacy scan.json
// (pre-import directories). nil-backend mode reads the file exactly as
// before — every existing caller's file behavior is unchanged.
func scanRecordMetaForDir(dir string) (*ScanRecord, bool) {
	dir = filepath.Clean(dir)
	if dir == "" {
		return nil, false
	}
	if b := currentScanStateBackend(); b != nil {
		ctx, cancel := scanStateCtx()
		defer cancel()
		if row, ok, err := b.LoadScanRecord(ctx, scanStateIDForDir(dir)); err != nil {
			log.Printf("[scan] durable record meta read for %s failed, trying legacy file: %v", filepath.Base(dir), err)
		} else if ok {
			if rec, ok := decodeScanRecordSummary(row.Detail); ok {
				if rec.ID == "" {
					rec.ID = row.ScanID
				}
				return rec, true
			}
		}
	}
	return readScanSummary(filepath.Join(dir, "scan.json"))
}

// RunStatePurgeBackend is the durable run-state store's purge surface: the
// row families that follow a scan's lifecycle when its directory (or its
// run) is deleted — the domain journal records, the ledger snapshot, the
// bounded state documents and the watermark protocol rows (REDESIGN D12, PG
// spec user story 9: "删除 run 时其全部面板状态行随之清理"). The conversation
// event rows go through EventStateBackend's DeleteEventsByScan; the dispatch
// tombstone is keyed by the instance, so it is purged separately where the
// instance id is known (deleteDispatchSnapshotState). Satisfied by the
// runstate store by duck typing; the web layer type-asserts so tests can
// register narrow fakes.
type RunStatePurgeBackend interface {
	PurgeScanState(ctx context.Context, scanID string) error
	DeleteDispatchSnapshot(ctx context.Context, instanceID string) error
}

// Compile-time pin: the runstate store is the production purge surface.
var _ RunStatePurgeBackend = (*runstate.Store)(nil)

// runStatePurgeBackend resolves the registered purge surface: the scan-state
// seam first, the events seam second (production registers the same runstate
// store on both; tests may register either).
func runStatePurgeBackend() RunStatePurgeBackend {
	for _, b := range []any{currentScanStateBackend(), currentEventStateBackend()} {
		if p, ok := b.(RunStatePurgeBackend); ok {
			return p
		}
	}
	return nil
}

// deleteScanRecordState drops one record's durable row AND the run-state
// rows that follow its lifecycle, best-effort — the run deletion/retention
// counterpart of PutScanRecord so a deleted scan disappears from the sweep
// the way its directory disappearance used to, and leaves no orphan panel
// rows behind (R6.2: journal records, ledger snapshot, state documents,
// watermark rows; the event rows go through the events seam). dirID is the
// workspace basename and recID the record id; both are tried because legacy
// directories may name them differently.
func (s *Server) deleteScanRecordState(dirID, recID string) {
	b := currentScanStateBackend()
	ctx, cancel := scanStateCtx()
	defer cancel()
	ids := make([]string, 0, 2)
	if id := strings.TrimSpace(dirID); id != "" {
		ids = append(ids, id)
	}
	if id := strings.TrimSpace(recID); id != "" && id != dirID {
		ids = append(ids, id)
	}
	for _, id := range ids {
		if b != nil {
			if err := b.DeleteScanRecord(ctx, id); err != nil {
				log.Printf("[scan] durable record delete for %s failed: %v", id, err)
			}
		}
		if eb := currentEventStateBackend(); eb != nil {
			if err := eb.DeleteEventsByScan(ctx, id); err != nil {
				log.Printf("[scan] durable event rows delete for %s failed: %v", id, err)
			}
		}
		if p := runStatePurgeBackend(); p != nil {
			if err := p.PurgeScanState(ctx, id); err != nil {
				log.Printf("[scan] durable run-state purge for %s failed: %v", id, err)
			}
		}
	}
}

// deleteDispatchSnapshotState drops one run's dispatch tombstone row — the
// run's mount row is deleted with the runs table; the tombstone must not
// survive the run and keep answering re-dispatch reservations. Best-effort.
func (s *Server) deleteDispatchSnapshotState(instanceID string) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return
	}
	if p := runStatePurgeBackend(); p != nil {
		ctx, cancel := scanStateCtx()
		defer cancel()
		if err := p.DeleteDispatchSnapshot(ctx, instanceID); err != nil {
			log.Printf("[scan] durable dispatch snapshot delete for %s failed: %v", instanceID, err)
		}
	}
}
