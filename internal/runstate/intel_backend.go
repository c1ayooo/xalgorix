// intel_backend.go — the intelligence snapshot's durable row operations
// (REDESIGN D12, charter v3.0 §6.3): the run_state_files row
// (scan_id, kind="intelligence") is the durable home of the intel store.
//
// The runstate store implements internal/tools/intelligence.Backend by duck
// typing — same discipline as the ledger backend (raw snapshot bytes only, no
// interface import). The web layer's task-intelligence aggregation reads
// through intelligence.LoadSnapshot, which consults the registered backend
// first and keeps intelligence.json as the pre-import fallback.
//
// Failure discipline (charter §5.2/§6.4): a save failure returns to the
// mutation caller (persist-failure is not success) and is recorded on the
// run_state_watermarks "state:intelligence" stream by the intelligence
// store's existing acknowledgement path.

package runstate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// SaveIntelSnapshot rewrites the durable intel snapshot for one scan
// (whole-snapshot semantics; the store is bounded and deduplicated). seq is
// the intel store's item counter, persisted on the row for watermark
// comparisons. Idempotent per scan_id.
func (s *Store) SaveIntelSnapshot(scanID, instanceID string, seq int64, data []byte) error {
	scanID = strings.TrimSpace(scanID)
	if scanID == "" {
		return errors.New("runstate: intel snapshot save requires a scan id")
	}
	ctx, cancel := s.backendCtx()
	defer cancel()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO run_state_files (scan_id, kind, instance_id, seq, data, updated_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (scan_id, kind) DO UPDATE
		   SET instance_id = EXCLUDED.instance_id,
		       seq = EXCLUDED.seq,
		       data = EXCLUDED.data,
		       updated_at = now()`,
		scanID, KindIntelligence, instanceID, seq, string(data),
	); err != nil {
		return fmt.Errorf("runstate: put intel snapshot %s: %w", scanID, err)
	}
	return nil
}

// LoadIntelSnapshot returns the scan's own durable intel snapshot.
// Absent → (nil, 0, false, nil).
func (s *Store) LoadIntelSnapshot(scanID string) ([]byte, int64, bool, error) {
	scanID = strings.TrimSpace(scanID)
	if scanID == "" {
		return nil, 0, false, nil
	}
	ctx, cancel := s.backendCtx()
	defer cancel()
	var data []byte
	var seq int64
	err := s.pool.QueryRow(ctx,
		`SELECT data, seq FROM run_state_files WHERE scan_id = $1 AND kind = $2`,
		scanID, KindIntelligence,
	).Scan(&data, &seq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, false, nil
		}
		return nil, 0, false, fmt.Errorf("runstate: load intel snapshot %s: %w", scanID, err)
	}
	return data, seq, true, nil
}

// LoadLatestIntelSnapshotForInstance returns the newest sibling session's
// intel snapshot — the D11 continuity source for a re-dispatched session.
// source is the owning scan id (honest load logs).
func (s *Store) LoadLatestIntelSnapshotForInstance(instanceID, excludeScanID string) ([]byte, int64, string, bool, error) {
	if strings.TrimSpace(instanceID) == "" {
		return nil, 0, "", false, nil
	}
	ctx, cancel := s.backendCtx()
	defer cancel()
	var data []byte
	var seq int64
	var source string
	err := s.pool.QueryRow(ctx,
		`SELECT data, seq, scan_id FROM run_state_files
		 WHERE instance_id = $1 AND scan_id <> $2 AND kind = $3
		 ORDER BY updated_at DESC LIMIT 1`,
		instanceID, strings.TrimSpace(excludeScanID), KindIntelligence,
	).Scan(&data, &seq, &source)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, "", false, nil
		}
		return nil, 0, "", false, fmt.Errorf("runstate: load intel continuity %s: %w", instanceID, err)
	}
	return data, seq, source, true, nil
}

// PurgeScanState drops every durable run-state row of one scan: the domain
// journal records, the ledger snapshot, the bounded state documents and the
// watermark protocol rows. Run deletion / retention — PG rows follow the run
// lifecycle (REDESIGN D12, PG spec user story 9). The conversation events go
// through the events seam (DeleteEventsByScan) so both writers keep one
// keying discipline; absent rows are no-ops.
func (s *Store) PurgeScanState(ctx context.Context, scanID string) error {
	scanID = strings.TrimSpace(scanID)
	if scanID == "" {
		return errors.New("runstate: purge requires a scan id")
	}
	for name, stmt := range map[string]string{
		"journal":    `DELETE FROM run_journal WHERE scan_id = $1`,
		"ledger":     `DELETE FROM run_ledger_snapshots WHERE scan_id = $1`,
		"statefiles": `DELETE FROM run_state_files WHERE scan_id = $1`,
		"watermarks": `DELETE FROM run_state_watermarks WHERE scan_id = $1`,
	} {
		if _, err := s.pool.Exec(ctx, stmt, scanID); err != nil {
			return fmt.Errorf("runstate: purge %s rows for %s: %w", name, scanID, err)
		}
	}
	return nil
}

// DeleteDispatchSnapshot drops one run's dispatch tombstone row (the run's
// mount row goes with the runs table; the tombstone must not survive the
// run). Absent rows are a no-op.
func (s *Store) DeleteDispatchSnapshot(ctx context.Context, instanceID string) error {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM dispatch_snapshots WHERE instance_id = $1`,
		instanceID,
	); err != nil {
		return fmt.Errorf("runstate: delete dispatch snapshot %s: %w", instanceID, err)
	}
	return nil
}
