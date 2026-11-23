// scan_record_backend.go — R6.1 runs-table takeover of scan.json (REDESIGN
// D12, charter v3.0 §5.1): the durable home of one scan record.
//
// Two writes happen per record save, in one transaction:
//  1. run_scan_records — the per-record row (scan_id = the record id = the
//     workspace directory basename) carrying the verbatim record JSON and the
//     dataDir-relative workspace anchor. This is the full scan set the web
//     layer's findAllScanSummaries sweeps (platform.Repository.ScanRunRecords
//     is its read API — R6.1 slice).
//  2. the runs mount row's takeover columns (migration 0012) — updated only
//     by TOP-LEVEL records (parent_target empty) so wildcard children never
//     overwrite the instance's newest state. No matching mount row is not an
//     error (the importer precedent: the state tables carry their rows
//     regardless; runs is an FK-bearing mount, not a scan registry).
//
// Writers: the R6.0 directory importer (AttachScanState, watermark-guarded,
// idempotent) and the web layer's record safe point (PutScanRecord — the
// runstate half of the web ScanStateBackend seam, registered once at startup
// like SetEventStateBackend). The failure path mirrors AppendJournalRecord /
// AppendEvent: the error returns to the mutation caller (persist-failure is
// not success, charter §5.2) and a failed watermark is recorded so the
// operator audit view sees the unpersisted window (§6.4). Success does not
// touch the watermark — the row itself is the durable proof. The in-memory
// record stays authoritative; no control flow blocks the scan on the failure.

package runstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// WatermarkKindScanRecord names the watermark stream of the per-record scan
// state (run_state_watermarks (scan_id, kind)).
const WatermarkKindScanRecord = "scan_record"

// ScanStateDetail is the runs-table takeover payload for one scan.json
// (typed hot fields + the verbatim record as TEXT). ScanID keys the
// per-record row (the workspace directory basename — the same key the
// events/journal rows use); ScanDir is the dataDir-relative workspace anchor
// the file layer still needs; ParentTarget gates the runs-column update to
// top-level records.
type ScanStateDetail struct {
	ScanID        string
	InstanceID    string
	LegacyScanID  string
	ScanDir       string
	ParentTarget  string
	Status        string
	Target        string
	StartedAt     *time.Time
	FinishedAt    *time.Time
	CurrentPhase  int
	PlanTotal     int
	PlanCompleted int
	Raw           []byte
}

// ScanRecordState is the web-seam shape of one durable record save (the
// web layer's ScanStateBackend PutScanRecord). ScanID is the workspace
// directory basename; ParentTarget gates the runs-column update.
type ScanRecordState struct {
	ScanID       string
	InstanceID   string
	ScanDir      string
	ParentTarget string
	Detail       []byte // verbatim scan record JSON (TEXT, byte fidelity)
	UpdatedAt    time.Time
}

// scanRecordPayload is the narrow decode of the verbatim record for the runs
// takeover columns (mirrors the web layer's ScanRecord without importing it —
// the same discipline as the importer's parseScanState).
type scanRecordPayload struct {
	Status             string `json:"status"`
	Target             string `json:"target"`
	ParentTarget       string `json:"parent_target"`
	StartedAt          string `json:"started_at"`
	FinishedAt         string `json:"finished_at"`
	CurrentPhase       int    `json:"current_phase"`
	PlanTasksTotal     int    `json:"plan_tasks_total"`
	PlanTasksCompleted int    `json:"plan_tasks_completed"`
}

// AttachScanState attaches one scan record's state: the per-record
// run_scan_records row plus, for top-level records, the runs mount row's
// takeover columns (instance_id first, legacy_scan_id fallback — the R6.0
// importer's contract, now sharing one transaction with the per-record row).
// Idempotent per scan_id.
func (s *Store) AttachScanState(ctx context.Context, detail ScanStateDetail) error {
	scanID := strings.TrimSpace(detail.ScanID)
	if scanID == "" {
		scanID = strings.TrimSpace(detail.LegacyScanID)
	}
	if scanID == "" {
		return errors.New("runstate: attach scan state requires a scan id")
	}
	scanDir := strings.TrimSpace(detail.ScanDir)
	if scanDir == "" {
		scanDir = scanID // legacy flat convention: dataDir/<scan id>
	}
	if detail.ParentTarget == "" && len(detail.Raw) > 0 {
		var payload scanRecordPayload
		if err := json.Unmarshal(detail.Raw, &payload); err == nil {
			detail.ParentTarget = strings.TrimSpace(payload.ParentTarget)
		}
	}
	updatedAt := time.Now().UTC()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("runstate: begin scan record tx %s: %w", scanID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only after commit path
	if _, err := tx.Exec(ctx,
		`INSERT INTO run_scan_records (scan_id, instance_id, scan_dir, scan_detail, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (scan_id) DO UPDATE
		   SET instance_id = EXCLUDED.instance_id,
		       scan_dir = EXCLUDED.scan_dir,
		       scan_detail = EXCLUDED.scan_detail,
		       updated_at = EXCLUDED.updated_at`,
		scanID, detail.InstanceID, scanDir, string(detail.Raw), updatedAt,
	); err != nil {
		return fmt.Errorf("runstate: put scan record %s: %w", scanID, err)
	}
	// Top-level records own the instance's runs-row state; wildcard children
	// must not overwrite the newest top-level snapshot. An absent mount row is
	// not an error (the state tables carry their rows regardless).
	if detail.ParentTarget == "" {
		if err := updateRunsTakeoverColumns(ctx, tx, detail.InstanceID, scanID, detail, updatedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// updateRunsTakeoverColumns writes migration 0012's columns onto the runs
// mount row, matching by instance_id first and legacy_scan_id second (the
// AttachScanState contract). A row that matches neither identity is a no-op.
func updateRunsTakeoverColumns(ctx context.Context, tx pgx.Tx, instanceID, legacyScanID string, detail ScanStateDetail, updatedAt time.Time) error {
	const update = `UPDATE runs SET
		   scan_status = $2, scan_target = $3, scan_parent_target = $4,
		   scan_started_at = $5, scan_finished_at = $6,
		   scan_current_phase = $7, scan_plan_total = $8, scan_plan_completed = $9,
		   scan_detail = $10
		 WHERE `
	args := func(key string) []any {
		return []any{key, detail.Status, detail.Target, detail.ParentTarget,
			detail.StartedAt, detail.FinishedAt, detail.CurrentPhase, detail.PlanTotal,
			detail.PlanCompleted, string(detail.Raw)}
	}
	if strings.TrimSpace(instanceID) != "" {
		if _, err := tx.Exec(ctx, update+`instance_id = $1 AND $1 <> ''`, args(instanceID)...); err != nil {
			return fmt.Errorf("runstate: attach scan state %s: %w", instanceID, err)
		}
		return nil
	}
	if _, err := tx.Exec(ctx, update+`legacy_scan_id = $1 AND $1 <> ''`, args(legacyScanID)...); err != nil {
		return fmt.Errorf("runstate: attach scan state %s: %w", legacyScanID, err)
	}
	return nil
}

// PutScanRecord persists one record save synchronously (the web layer's
// record safe point, behind ScanStateBackend). Idempotent per scan_id: a
// retried save after a failed write reuses the row. The error returns to the
// caller and a failed watermark is recorded by this method.
func (s *Store) PutScanRecord(rec ScanRecordState) error {
	scanID := strings.TrimSpace(rec.ScanID)
	if scanID == "" {
		return errors.New("runstate: scan record save requires a scan id")
	}
	var payload scanRecordPayload
	if len(rec.Detail) > 0 {
		if err := json.Unmarshal(rec.Detail, &payload); err != nil {
			return fmt.Errorf("runstate: decode scan record %s: %w", scanID, err)
		}
	}
	if rec.ParentTarget == "" {
		rec.ParentTarget = strings.TrimSpace(payload.ParentTarget)
	}
	started, serr := parseTime(payload.StartedAt)
	if serr != nil {
		started = nil // unparsable stamps stay NULL, like the importer's
	}
	finished, ferr := parseTime(payload.FinishedAt)
	if ferr != nil {
		finished = nil
	}
	detail := ScanStateDetail{
		ScanID:        scanID,
		InstanceID:    strings.TrimSpace(rec.InstanceID),
		ScanDir:       strings.TrimSpace(rec.ScanDir),
		ParentTarget:  strings.TrimSpace(rec.ParentTarget),
		Status:        payload.Status,
		Target:        payload.Target,
		StartedAt:     started,
		FinishedAt:    finished,
		CurrentPhase:  payload.CurrentPhase,
		PlanTotal:     payload.PlanTasksTotal,
		PlanCompleted: payload.PlanTasksCompleted,
		Raw:           rec.Detail,
	}
	ctx, cancel := s.backendCtx()
	defer cancel()
	if err := s.AttachScanState(ctx, detail); err != nil {
		return s.failScanRecord(context.Background(), rec, err)
	}
	return nil
}

// failScanRecord returns the save error and lands the failed watermark
// (best-effort: a watermark failure is folded into the returned error, never
// swallowed silently).
func (s *Store) failScanRecord(ctx context.Context, rec ScanRecordState, cause error) error {
	if wmErr := s.RecordWatermarkFailure(ctx, rec.ScanID, WatermarkKindScanRecord, rec.InstanceID, 0, cause); wmErr != nil {
		return fmt.Errorf("%w (watermark also failed: %w)", cause, wmErr)
	}
	return cause
}

// LoadScanRecord reads one record's durable row. Absent → (zero, false, nil).
func (s *Store) LoadScanRecord(ctx context.Context, scanID string) (ScanRecordState, bool, error) {
	var row ScanRecordState
	var detail []byte
	err := s.pool.QueryRow(ctx,
		`SELECT scan_id, instance_id, scan_dir, scan_detail, updated_at
		 FROM run_scan_records WHERE scan_id = $1`,
		strings.TrimSpace(scanID),
	).Scan(&row.ScanID, &row.InstanceID, &row.ScanDir, &detail, &row.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ScanRecordState{}, false, nil
		}
		return ScanRecordState{}, false, fmt.Errorf("runstate: load scan record %s: %w", scanID, err)
	}
	row.Detail = detail
	row.UpdatedAt = row.UpdatedAt.UTC()
	return row, true, nil
}

// DeleteScanRecord drops one record's durable row (run deletion/retention —
// PG rows follow the run lifecycle). Absent rows are a no-op.
func (s *Store) DeleteScanRecord(ctx context.Context, scanID string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM run_scan_records WHERE scan_id = $1`,
		strings.TrimSpace(scanID),
	); err != nil {
		return fmt.Errorf("runstate: delete scan record %s: %w", scanID, err)
	}
	return nil
}

// DeleteJournalByScan drops one session's domain journal rows — the
// reset/deletion companion of DeleteEventsByScan. Absent rows are a no-op.
func (s *Store) DeleteJournalByScan(ctx context.Context, scanID string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM run_journal WHERE scan_id = $1`,
		strings.TrimSpace(scanID),
	); err != nil {
		return fmt.Errorf("runstate: delete journal %s: %w", scanID, err)
	}
	return nil
}

// DeleteWatermarksByScan drops one scan's watermark rows — used together with
// the deletes above so a deleted scan leaves no stale protocol rows behind
// (deleting a dir_import row re-arms the importer by design).
func (s *Store) DeleteWatermarksByScan(ctx context.Context, scanID string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM run_state_watermarks WHERE scan_id = $1`,
		strings.TrimSpace(scanID),
	)
	if err != nil {
		return 0, fmt.Errorf("runstate: delete watermarks %s: %w", scanID, err)
	}
	return tag.RowsAffected(), nil
}
