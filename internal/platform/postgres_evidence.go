package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/c1ayooo/xalgorix/v4/internal/evidence"
)

// EvidenceStore is implemented here because the pgx pool is private to
// platform. The evidence package owns the bytes and hashing; this file only
// persists metadata, bindings and the per-finding version.

// UpsertEvidenceSnapshots implements evidence.DB.
func (s *Store) UpsertEvidenceSnapshots(ctx context.Context, snaps []evidence.Snapshot) error {
	if s == nil || s.pool == nil {
		return errors.New("platform/postgres: store unavailable")
	}
	if len(snaps) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("platform/postgres: begin evidence upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, snap := range snaps {
		if _, err := tx.Exec(ctx, `
			INSERT INTO finding_evidence_snapshots (
				id, scan_id, session, seq, source, source_ref, captured_at,
				method, url, status, req_sha256, resp_sha256, req_len, resp_len,
				req_head, resp_head, req_truncated, resp_truncated, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
			ON CONFLICT (id) DO UPDATE SET
				captured_at = EXCLUDED.captured_at,
				req_head = EXCLUDED.req_head,
				resp_head = EXCLUDED.resp_head`,
			snap.ID, snap.ScanID, snap.Session, snap.Seq, snap.Source, snap.SourceRef,
			nullTime(snap.CapturedAt), snap.Method, snap.URL, snap.Status,
			snap.ReqSHA256, snap.RespSHA256, snap.ReqLen, snap.RespLen,
			snap.ReqHead, snap.RespHead, snap.ReqTruncated, snap.RespTruncated,
			nonZeroTime(snap.CreatedAt)); err != nil {
			return fmt.Errorf("platform/postgres: upsert evidence snapshot %s: %w", snap.ID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("platform/postgres: commit evidence snapshots: %w", err)
	}
	return nil
}

// BindFindingEvidence implements evidence.DB. Re-binding the same set keeps the
// version stable; a changed set bumps it.
func (s *Store) BindFindingEvidence(ctx context.Context, scanID, findingID string, bindings []evidence.Binding) (int, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("platform/postgres: store unavailable")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("platform/postgres: begin evidence bind: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Diff against the existing set so an unchanged re-report does not bump the
	// version (idempotent re-reporting is normal).
	existing := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT snapshot_id FROM finding_evidence_bindings WHERE scan_id=$1 AND finding_id=$2`, scanID, findingID)
	if err != nil {
		return 0, fmt.Errorf("platform/postgres: read existing bindings: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		existing[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}

	changed := len(existing) != len(bindings)
	for _, b := range bindings {
		if !existing[b.SnapshotID] {
			changed = true
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM finding_evidence_bindings WHERE scan_id=$1 AND finding_id=$2`, scanID, findingID); err != nil {
		return 0, fmt.Errorf("platform/postgres: clear bindings: %w", err)
	}
	for i, b := range bindings {
		role := b.Role
		if role == "" {
			role = evidence.RoleProof
		}
		pos := b.Position
		if pos == 0 {
			pos = i
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO finding_evidence_bindings (scan_id, finding_id, snapshot_id, role, position)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (scan_id, finding_id, snapshot_id) DO UPDATE SET
				role = EXCLUDED.role, position = EXCLUDED.position`,
			scanID, findingID, b.SnapshotID, role, pos); err != nil {
			return 0, fmt.Errorf("platform/postgres: insert binding: %w", err)
		}
	}

	var version int
	if err := tx.QueryRow(ctx, `
		INSERT INTO finding_evidence_versions (scan_id, finding_id, version, report_version, updated_at)
		VALUES ($1,$2,1,0,now())
		ON CONFLICT (scan_id, finding_id) DO UPDATE SET
			version = CASE WHEN $3 THEN finding_evidence_versions.version + 1 ELSE finding_evidence_versions.version END,
			updated_at = now()
		RETURNING version`, scanID, findingID, changed).Scan(&version); err != nil {
		return 0, fmt.Errorf("platform/postgres: bump evidence version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("platform/postgres: commit evidence binding: %w", err)
	}
	return version, nil
}

// ListFindingEvidence implements evidence.DB.
func (s *Store) ListFindingEvidence(ctx context.Context, scanID, findingID string) ([]evidence.Snapshot, int, error) {
	if s == nil || s.pool == nil {
		return nil, 0, errors.New("platform/postgres: store unavailable")
	}
	var version int
	if err := s.pool.QueryRow(ctx, `SELECT version FROM finding_evidence_versions WHERE scan_id=$1 AND finding_id=$2`, scanID, findingID).Scan(&version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.scan_id, s.session, s.seq, s.source, s.source_ref,
		       s.captured_at, s.method, s.url, s.status, s.req_sha256, s.resp_sha256,
		       s.req_len, s.resp_len, s.req_head, s.resp_head,
		       s.req_truncated, s.resp_truncated, s.created_at
		FROM finding_evidence_bindings b
		JOIN finding_evidence_snapshots s ON s.id = b.snapshot_id
		WHERE b.scan_id=$1 AND b.finding_id=$2
		ORDER BY b.position`, scanID, findingID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []evidence.Snapshot
	for rows.Next() {
		var snap evidence.Snapshot
		var captured *time.Time
		if err := rows.Scan(&snap.ID, &snap.ScanID, &snap.Session, &snap.Seq, &snap.Source, &snap.SourceRef,
			&captured, &snap.Method, &snap.URL, &snap.Status, &snap.ReqSHA256, &snap.RespSHA256,
			&snap.ReqLen, &snap.RespLen, &snap.ReqHead, &snap.RespHead,
			&snap.ReqTruncated, &snap.RespTruncated, &snap.CreatedAt); err != nil {
			return nil, 0, err
		}
		if captured != nil {
			snap.CapturedAt = *captured
		}
		out = append(out, snap)
	}
	return out, version, rows.Err()
}

// GetEvidenceSnapshot implements evidence.DB.
func (s *Store) GetEvidenceSnapshot(ctx context.Context, snapshotID string) (evidence.Snapshot, error) {
	var snap evidence.Snapshot
	var captured *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT id, scan_id, session, seq, source, source_ref, captured_at,
		       method, url, status, req_sha256, resp_sha256, req_len, resp_len,
		       req_head, resp_head, req_truncated, resp_truncated, created_at
		FROM finding_evidence_snapshots WHERE id=$1`, snapshotID).Scan(
		&snap.ID, &snap.ScanID, &snap.Session, &snap.Seq, &snap.Source, &snap.SourceRef,
		&captured, &snap.Method, &snap.URL, &snap.Status, &snap.ReqSHA256, &snap.RespSHA256,
		&snap.ReqLen, &snap.RespLen, &snap.ReqHead, &snap.RespHead,
		&snap.ReqTruncated, &snap.RespTruncated, &snap.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return snap, ErrNotFound
		}
		return snap, err
	}
	if captured != nil {
		snap.CapturedAt = *captured
	}
	return snap, nil
}

// CollectEvidence implements evidence.DB: snapshots unreferenced for 24h are
// dropped, bindings cascade with them.
func (s *Store) CollectEvidence(ctx context.Context, now time.Time) error {
	if s == nil || s.pool == nil {
		return errors.New("platform/postgres: store unavailable")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM finding_evidence_snapshots s
		WHERE s.created_at < $1
		  AND NOT EXISTS (SELECT 1 FROM finding_evidence_bindings b WHERE b.snapshot_id = s.id)`,
		now.Add(-24*time.Hour))
	return err
}

// EvidenceHashLive reports whether any surviving snapshot still references the
// content hash (shared across findings by content addressing).
func (s *Store) EvidenceHashLive(ctx context.Context, hash string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("platform/postgres: store unavailable")
	}
	var live bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM finding_evidence_snapshots
			WHERE req_sha256=$1 OR resp_sha256=$1
		)`, hash).Scan(&live)
	return live, err
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func nonZeroTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}
