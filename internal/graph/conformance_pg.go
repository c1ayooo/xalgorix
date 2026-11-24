package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// RecordConformance persists one read-only conformance snapshot. Repeated
// checks at the same timestamp are idempotent. It never repairs graph state.
func (s *PGStore) RecordConformance(ctx context.Context, report Report) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("graph: store unavailable")
	}
	checkedAt := report.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	missing, stale := []string{}, []string{}
	expected, observed := 0, 0
	if report.Projection != nil {
		missing = report.Projection.Missing
		stale = report.Projection.Stale
		expected = report.Projection.Expected
		observed = report.Projection.Observed
	}
	missingJSON, err := json.Marshal(missing)
	if err != nil {
		return fmt.Errorf("graph: marshal missing: %w", err)
	}
	staleJSON, err := json.Marshal(stale)
	if err != nil {
		return fmt.Errorf("graph: marshal stale: %w", err)
	}
	violations := report.Violations
	if violations == nil {
		violations = []Violation{}
	}
	violationsJSON, err := json.Marshal(violations)
	if err != nil {
		return fmt.Errorf("graph: marshal violations: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph_conformance
			(scan_id, checked_at, ok, errors, warnings, expected, observed, missing, stale, violations, truncated)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (scan_id, checked_at) DO NOTHING`,
		report.ScanID, checkedAt, report.OK, report.Errors, report.Warnings,
		expected, observed, missingJSON, staleJSON, violationsJSON, report.Truncated,
	)
	if err != nil {
		return fmt.Errorf("graph: record conformance: %w", err)
	}
	return nil
}

// DeleteConformance drops one run's conformance snapshots. Called by run
// retention/deletion; read-only snapshots carry no cross-run references.
func (s *PGStore) DeleteConformance(ctx context.Context, scanID string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("graph: store unavailable")
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM graph_conformance WHERE scan_id = $1`, scanID); err != nil {
		return fmt.Errorf("graph: delete conformance: %w", err)
	}
	return nil
}
