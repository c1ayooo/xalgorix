package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReplaceRunFindings implements FindingsStore. The replacement is one
// transaction so a scan that reports fewer findings can never leave stale rows
// visible between a delete and the inserts.
func (s *Store) ReplaceRunFindings(ctx context.Context, runID string, scanStartedAt time.Time, findings []RunFinding) error {
	if s == nil || s.pool == nil {
		return errors.New("platform/postgres: store unavailable")
	}
	parsedRun, err := parseUUID(runID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("platform/postgres: begin findings replace: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var taskID, projectID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT r.task_id, t.project_id
		FROM runs r JOIN tasks t ON t.id = r.task_id
		WHERE r.id = $1`, parsedRun).Scan(&taskID, &projectID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("platform/postgres: locate run for findings: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM run_findings WHERE run_id = $1`, parsedRun); err != nil {
		return fmt.Errorf("platform/postgres: clear run findings: %w", err)
	}
	if scanStartedAt.IsZero() {
		scanStartedAt = time.Now().UTC()
	}
	for _, finding := range findings {
		key := strings.TrimSpace(finding.DedupKey)
		if key == "" {
			return fmt.Errorf("%w: finding dedup_key is required", ErrValidation)
		}
		if len(finding.Finding) == 0 || !json.Valid(finding.Finding) {
			return fmt.Errorf("%w: finding payload for %q is not valid JSON", ErrValidation, key)
		}
		at := finding.ScanStartedAt
		if at.IsZero() {
			at = scanStartedAt
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO run_findings (run_id, task_id, project_id, dedup_key, scan_started_at, finding)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (run_id, dedup_key) DO UPDATE
			SET scan_started_at = EXCLUDED.scan_started_at,
			    finding = EXCLUDED.finding`,
			parsedRun, taskID, projectID, key, at, []byte(finding.Finding)); err != nil {
			return fmt.Errorf("platform/postgres: insert run finding: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("platform/postgres: commit findings replace: %w", err)
	}
	return nil
}

// ListTaskFindings implements FindingsStore. DISTINCT ON keeps one newest
// finding per dedup key; run_id is a deterministic tie-break for equal scan
// start times.
func (s *Store) ListTaskFindings(ctx context.Context, taskID string) ([]RunFinding, error) {
	parsed, err := parseUUID(taskID)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `
		SELECT dedup_key, scan_started_at, finding
		FROM (
			SELECT DISTINCT ON (dedup_key)
			       dedup_key, scan_started_at, finding, run_id
			FROM run_findings
			WHERE task_id = $1
			ORDER BY dedup_key, scan_started_at DESC, run_id ASC
		) newest`, parsed)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list task findings: %w", err)
	}
	defer rows.Close()
	out := []RunFinding{}
	for rows.Next() {
		var (
			item        RunFinding
			findingJSON []byte
		)
		if err := rows.Scan(&item.DedupKey, &item.ScanStartedAt, &findingJSON); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan task finding: %w", err)
		}
		item.Finding = append(json.RawMessage(nil), findingJSON...)
		item.ScanStartedAt = item.ScanStartedAt.UTC()
		out = append(out, item)
	}
	return out, rows.Err()
}

// CountTaskFindings implements FindingsStore.
func (s *Store) CountTaskFindings(ctx context.Context, taskID string) (int, error) {
	parsed, err := parseUUID(taskID)
	if err != nil {
		return 0, err
	}
	var count int
	if err := s.queryRow(ctx,
		`SELECT count(DISTINCT dedup_key) FROM run_findings WHERE task_id = $1`, parsed).Scan(&count); err != nil {
		return 0, fmt.Errorf("platform/postgres: count task findings: %w", err)
	}
	return count, nil
}
