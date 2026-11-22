// run_scan_records.go — the "full runs state sweep" query API (R6.1, REDESIGN
// D12: the runs table takes over scan.json, files stop being written).
//
// The web layer's findAllScanSummaries needs the FULL scan set at per-record
// granularity (multi-target queue siblings, wildcard children, D11 ancestor
// sessions), which the unique-instance runs mount row cannot carry. The
// per-record durable home is run_scan_records (migration 0016): one row per
// scan record, verbatim record JSON (TEXT — migration 0013's byte-fidelity
// rule) plus the dataDir-relative workspace anchor. This file is that
// table's repository seam: ScanRunRecords sweeps it with keyset-cursor
// pagination sized for the web walk's scale; PutRunScanRecord is the file
// store's write counterpart so tests exercise the same surface.
//
// Both repository implementations serve the same signature: the PostgreSQL
// store reads the table the live safe point and the R6.0 importer write; the
// file store mirrors rows into its platform.json snapshot (the no-DSN/test
// mode — file-mode web tests that never write records see an empty sweep and
// fall back to their legacy file walk, byte-identical to pre-R6.1).

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RunScanRecord is one per-scan-record state row of the full runs scan set.
type RunScanRecord struct {
	// ScanID is the record id (= the workspace directory basename) — the
	// same durable key the events/journal rows use.
	ScanID string
	// InstanceID is the D11 run identity stamped at the write boundary.
	InstanceID string
	// ScanDir is the dataDir-relative workspace anchor (byte artifacts stay
	// on disk). Empty means "unknown" — the reader falls back to the file
	// walk's directory for the same record id.
	ScanDir string
	// Detail is the verbatim scan record JSON (the scan.json long tail).
	Detail []byte
	// UpdatedAt is the row's last write (the web layer's freshness signal).
	UpdatedAt time.Time
}

// ScanRunRecordPageBounds bounds one sweep page. The web layer loops until
// the cursor is exhausted; the cap keeps a single page from materializing an
// unbounded slice even when a caller passes a huge limit.
const (
	ScanRunRecordDefaultPage = 500
	ScanRunRecordMaxPage     = 2000
)

// normalizeScanRunRecordPage clamps the page size into [1, MaxPage].
func normalizeScanRunRecordPage(limit int) int {
	if limit <= 0 {
		return ScanRunRecordDefaultPage
	}
	if limit > ScanRunRecordMaxPage {
		return ScanRunRecordMaxPage
	}
	return limit
}

// ── PostgreSQL implementation ────────────────────────────────────────────────

// ScanRunRecords implements Repository.ScanRunRecords (keyset cursor on
// scan_id — the primary key, so every page is an index-only range scan).
func (s *Store) ScanRunRecords(ctx context.Context, cursor string, limit int) ([]RunScanRecord, string, error) {
	rows, err := s.query(ctx,
		`SELECT scan_id, instance_id, scan_dir, scan_detail, updated_at
		 FROM run_scan_records
		 WHERE scan_id > $1
		 ORDER BY scan_id ASC
		 LIMIT $2`,
		strings.TrimSpace(cursor), normalizeScanRunRecordPage(limit),
	)
	if err != nil {
		return nil, "", fmt.Errorf("platform/postgres: sweep scan records: %w", err)
	}
	defer rows.Close()
	out := []RunScanRecord{}
	var next string
	for rows.Next() {
		var rec RunScanRecord
		var detail []byte
		if err := rows.Scan(&rec.ScanID, &rec.InstanceID, &rec.ScanDir, &detail, &rec.UpdatedAt); err != nil {
			return nil, "", fmt.Errorf("platform/postgres: scan scan record row: %w", err)
		}
		rec.Detail = detail
		rec.UpdatedAt = rec.UpdatedAt.UTC()
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("platform/postgres: iterate scan record rows: %w", err)
	}
	if len(out) == normalizeScanRunRecordPage(limit) {
		next = out[len(out)-1].ScanID
	}
	return out, next, nil
}

// PutRunScanRecord implements Repository.PutRunScanRecord.
func (s *Store) PutRunScanRecord(ctx context.Context, rec RunScanRecord) error {
	rec.ScanID = strings.TrimSpace(rec.ScanID)
	if rec.ScanID == "" {
		return fmt.Errorf("%w: scan record scan_id is required", ErrValidation)
	}
	updatedAt := rec.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	if _, err := s.exec(ctx,
		`INSERT INTO run_scan_records (scan_id, instance_id, scan_dir, scan_detail, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (scan_id) DO UPDATE
		   SET instance_id = EXCLUDED.instance_id,
		       scan_dir = EXCLUDED.scan_dir,
		       scan_detail = EXCLUDED.scan_detail,
		       updated_at = EXCLUDED.updated_at`,
		rec.ScanID, rec.InstanceID, rec.ScanDir, string(rec.Detail), updatedAt,
	); err != nil {
		return fmt.Errorf("platform/postgres: put scan record %s: %w", rec.ScanID, err)
	}
	return nil
}

// DeleteRunScanRecord implements Repository.DeleteRunScanRecord.
func (s *Store) DeleteRunScanRecord(ctx context.Context, scanID string) error {
	if _, err := s.exec(ctx, `DELETE FROM run_scan_records WHERE scan_id = $1`, strings.TrimSpace(scanID)); err != nil {
		return fmt.Errorf("platform/postgres: delete scan record %s: %w", scanID, err)
	}
	return nil
}

// ── file store implementation (no-DSN / test mode) ───────────────────────────

// runScanRecordWire persists one per-record row inside the platform.json
// snapshot. Detail stays a raw embedded JSON value so the verbatim record
// round-trips byte-stable inside the snapshot.
type runScanRecordWire struct {
	ScanID     string          `json:"scan_id"`
	InstanceID string          `json:"instance_id,omitempty"`
	ScanDir    string          `json:"scan_dir,omitempty"`
	Detail     json.RawMessage `json:"detail"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// ScanRunRecords implements Repository.ScanRunRecords over the snapshot
// (sorted by scan_id, same keyset contract as the PG implementation).
func (s *FileStore) ScanRunRecords(_ context.Context, cursor string, limit int) ([]RunScanRecord, string, error) {
	s.mu.RLock()
	wire := append([]runScanRecordWire(nil), s.data.RunScanRecords...)
	s.mu.RUnlock()
	sort.Slice(wire, func(i, j int) bool { return wire[i].ScanID < wire[j].ScanID })

	size := normalizeScanRunRecordPage(limit)
	out := []RunScanRecord{}
	cursor = strings.TrimSpace(cursor)
	for _, w := range wire {
		if cursor != "" && w.ScanID <= cursor {
			continue
		}
		out = append(out, RunScanRecord{
			ScanID:     w.ScanID,
			InstanceID: w.InstanceID,
			ScanDir:    w.ScanDir,
			Detail:     append([]byte(nil), w.Detail...),
			UpdatedAt:  w.UpdatedAt.UTC(),
		})
		if len(out) == size {
			break
		}
	}
	var next string
	if len(out) == size && len(out) > 0 {
		next = out[len(out)-1].ScanID
	}
	return out, next, nil
}

// PutRunScanRecord implements Repository.PutRunScanRecord over the snapshot.
func (s *FileStore) PutRunScanRecord(_ context.Context, rec RunScanRecord) error {
	rec.ScanID = strings.TrimSpace(rec.ScanID)
	if rec.ScanID == "" {
		return fmt.Errorf("%w: scan record scan_id is required", ErrValidation)
	}
	updatedAt := rec.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wire := runScanRecordWire{
		ScanID:     rec.ScanID,
		InstanceID: rec.InstanceID,
		ScanDir:    rec.ScanDir,
		Detail:     append(json.RawMessage(nil), rec.Detail...),
		UpdatedAt:  updatedAt.UTC(),
	}
	replaced := false
	for i := range s.data.RunScanRecords {
		if s.data.RunScanRecords[i].ScanID == rec.ScanID {
			s.data.RunScanRecords[i] = wire
			replaced = true
			break
		}
	}
	if !replaced {
		s.data.RunScanRecords = append(s.data.RunScanRecords, wire)
	}
	return s.save()
}

// DeleteRunScanRecord implements Repository.DeleteRunScanRecord over the
// snapshot. An absent row is a no-op (nothing to persist).
func (s *FileStore) DeleteRunScanRecord(_ context.Context, scanID string) error {
	scanID = strings.TrimSpace(scanID)
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.data.RunScanRecords[:0]
	removed := false
	for _, w := range s.data.RunScanRecords {
		if w.ScanID == scanID {
			removed = true
			continue
		}
		kept = append(kept, w)
	}
	if !removed {
		return nil
	}
	s.data.RunScanRecords = kept
	return s.save()
}
