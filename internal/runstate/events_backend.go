// events_backend.go — synchronous single-event persistence for the
// conversation/telemetry journal (run_events, R6.1 events slice of the PG
// primary storage switch, REDESIGN D12).
//
// The web layer's journal stays the run-time writer; PG becomes the durable
// home. Idempotent on (scan_id, seq) like the importer and the batch
// PutEvents: the seq is the 1-based durable journal position, and instance_id
// is stamped at the write boundary from the session's record. The failure
// path mirrors AppendJournalRecord: the error returns to the mutation caller
// (persist-failure is not success, charter §5.2) and a failed watermark is
// recorded so the operator audit view sees the unpersisted window (§6.4).
// Success does not touch the watermark — the row itself is the durable proof.

package runstate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AppendEvent persists ONE conversation/telemetry journal event
// synchronously — the R6.1 persistence hook behind the web layer's
// appendScanEventJournal. payload is the verbatim WSEvent JSON (TEXT column:
// byte fidelity, NUL-safe per migration 0013).
func (s *Store) AppendEvent(scanID, instanceID string, seq uint64, typ string, occurredAt time.Time, payload []byte) error {
	if strings.TrimSpace(scanID) == "" {
		return errors.New("runstate: event append requires a scan id")
	}
	if seq == 0 {
		return fmt.Errorf("runstate: event append %s: seq must be positive", scanID)
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	ctx, cancel := s.backendCtx()
	defer cancel()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO run_events (scan_id, instance_id, seq, type, occurred_at, payload)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (scan_id, seq) DO UPDATE
		   SET instance_id = EXCLUDED.instance_id,
		       type = EXCLUDED.type,
		       payload = EXCLUDED.payload`,
		scanID, instanceID, seq, typ, occurredAt, string(payload),
	); err != nil {
		err = fmt.Errorf("runstate: append event %s/%d: %w", scanID, seq, err)
		if wmErr := s.RecordWatermarkFailure(context.Background(), scanID, WatermarkKindEvents, instanceID, int64(seq), err); wmErr != nil { //nolint:gosec // seq is the journal's 1-based line position, bounded far below 2^63
			return fmt.Errorf("%w (watermark also failed: %w)", err, wmErr)
		}
		return err
	}
	return nil
}

// DeleteEventsByScan drops one session's event rows — the reset-state re-run
// path (the file mode truncates that session's journal to empty; the durable
// equivalent is deleting its rows). Sibling sessions of the run are untouched,
// so instance-level projections keep the run's history.
func (s *Store) DeleteEventsByScan(ctx context.Context, scanID string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM run_events WHERE scan_id = $1`, scanID); err != nil {
		return fmt.Errorf("runstate: delete events %s: %w", scanID, err)
	}
	return nil
}
