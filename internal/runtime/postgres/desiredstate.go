// Package postgres persists the toolbox desired-state watermark in PostgreSQL.
//
// It implements runtime.DesiredStateStore; the table is created by the shared
// info-model migrator (0015_toolbox_projects.sql), which platform.Open and the
// cold path already run on startup.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/c1ayooo/xalgorix/v4/internal/runtime"
)

// Store is the PG-backed runtime.DesiredStateStore.
type Store struct {
	pool *pgxpool.Pool
}

// New wraps an existing pool (the platform already owns one).
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ runtime.DesiredStateStore = (*Store)(nil)

const columns = `project_id, scan_id, instance_id, image_digest, profile, net_profile,
	desired_state, desired_gen, applied_gen, status, container_id, last_error, spec,
	created_at, updated_at`

func scanRow(row pgx.Row) (runtime.DesiredProject, error) {
	var p runtime.DesiredProject
	err := row.Scan(&p.ProjectID, &p.ScanID, &p.InstanceID, &p.ImageDigest, &p.Profile, &p.NetProfile,
		&p.DesiredState, &p.DesiredGen, &p.AppliedGen, &p.Status, &p.ContainerID, &p.LastError, &p.Spec,
		&p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// RecordProject implements runtime.DesiredStateStore.
//
// The generation is bumped only when the desired state, spec, or image actually
// change. Without that guard every hot-path EnsureProject would look like new
// work and the reconciler would churn.
func (s *Store) RecordProject(ctx context.Context, row runtime.DesiredProject) (runtime.DesiredProject, error) {
	if s == nil || s.pool == nil {
		return runtime.DesiredProject{}, errors.New("runtime/postgres: nil pool")
	}
	if row.ProjectID == "" {
		return runtime.DesiredProject{}, errors.New("runtime/postgres: project id is required")
	}
	desiredState := row.DesiredState
	if desiredState == "" {
		desiredState = runtime.DesiredPending
	}
	const q = `
INSERT INTO toolbox_projects
    (project_id, scan_id, instance_id, image_digest, profile, net_profile, desired_state, spec)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (project_id) DO UPDATE SET
    scan_id       = EXCLUDED.scan_id,
    instance_id   = EXCLUDED.instance_id,
    profile       = EXCLUDED.profile,
    net_profile   = EXCLUDED.net_profile,
    desired_state = EXCLUDED.desired_state,
    image_digest  = EXCLUDED.image_digest,
    spec          = EXCLUDED.spec,
    desired_gen   = CASE
                      WHEN toolbox_projects.desired_state IS DISTINCT FROM EXCLUDED.desired_state
                        OR toolbox_projects.spec IS DISTINCT FROM EXCLUDED.spec
                        OR toolbox_projects.image_digest IS DISTINCT FROM EXCLUDED.image_digest
                      THEN toolbox_projects.desired_gen + 1
                      ELSE toolbox_projects.desired_gen
                    END,
    status        = CASE
                      WHEN toolbox_projects.desired_state IS DISTINCT FROM EXCLUDED.desired_state
                        OR toolbox_projects.spec IS DISTINCT FROM EXCLUDED.spec
                        OR toolbox_projects.image_digest IS DISTINCT FROM EXCLUDED.image_digest
                      THEN 'pending'
                      ELSE toolbox_projects.status
                    END,
    updated_at    = now()
RETURNING ` + columns
	return scanRow(s.pool.QueryRow(ctx, q,
		row.ProjectID, row.ScanID, row.InstanceID, row.ImageDigest, row.Profile, row.NetProfile,
		desiredState, row.Spec))
}

// MarkApplied implements runtime.DesiredStateStore.
func (s *Store) MarkApplied(ctx context.Context, projectID string, gen int64, containerID, state string) error {
	const q = `
UPDATE toolbox_projects SET
    applied_gen  = GREATEST(applied_gen, $2),
    container_id = CASE WHEN $3 = '' THEN container_id ELSE $3 END,
    status       = CASE WHEN GREATEST(applied_gen, $2) >= desired_gen THEN 'applied' ELSE 'pending' END,
    last_error   = '',
    updated_at   = now()
WHERE project_id = $1`
	tag, err := s.pool.Exec(ctx, q, projectID, gen, containerID)
	if err != nil {
		return fmt.Errorf("runtime/postgres: mark applied %s: %w", projectID, err)
	}
	if tag.RowsAffected() == 0 {
		return runtime.ErrProjectNotFound
	}
	_ = state
	return nil
}

// MarkFailed implements runtime.DesiredStateStore.
func (s *Store) MarkFailed(ctx context.Context, projectID string, _ int64, msg string) error {
	const q = `UPDATE toolbox_projects SET status = 'failed', last_error = $2, updated_at = now() WHERE project_id = $1`
	tag, err := s.pool.Exec(ctx, q, projectID, msg)
	if err != nil {
		return fmt.Errorf("runtime/postgres: mark failed %s: %w", projectID, err)
	}
	if tag.RowsAffected() == 0 {
		return runtime.ErrProjectNotFound
	}
	return nil
}

// MarkGap implements runtime.DesiredStateStore.
func (s *Store) MarkGap(ctx context.Context, projectID string, _ int64, msg string) error {
	const q = `UPDATE toolbox_projects SET status = 'gap', last_error = $2, updated_at = now() WHERE project_id = $1`
	tag, err := s.pool.Exec(ctx, q, projectID, msg)
	if err != nil {
		return fmt.Errorf("runtime/postgres: mark gap %s: %w", projectID, err)
	}
	if tag.RowsAffected() == 0 {
		return runtime.ErrProjectNotFound
	}
	return nil
}

// Unreconciled implements runtime.DesiredStateStore.
func (s *Store) Unreconciled(ctx context.Context, limit int) ([]runtime.DesiredProject, error) {
	if limit <= 0 {
		limit = 64
	}
	const q = `SELECT ` + columns + ` FROM toolbox_projects
WHERE applied_gen < desired_gen OR status IN ('pending', 'gap', 'failed')
ORDER BY updated_at ASC
LIMIT $1`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("runtime/postgres: list unreconciled: %w", err)
	}
	defer rows.Close()
	var out []runtime.DesiredProject
	for rows.Next() {
		row, scanErr := scanRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("runtime/postgres: scan project: %w", scanErr)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Active implements runtime.DesiredStateStore.
func (s *Store) Active(ctx context.Context) ([]runtime.DesiredProject, error) {
	const q = `SELECT ` + columns + ` FROM toolbox_projects
WHERE desired_state IN ('pending', 'ready')
ORDER BY updated_at ASC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("runtime/postgres: list active projects: %w", err)
	}
	defer rows.Close()
	var out []runtime.DesiredProject
	for rows.Next() {
		row, scanErr := scanRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("runtime/postgres: scan project: %w", scanErr)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Get implements runtime.DesiredStateStore.
func (s *Store) Get(ctx context.Context, projectID string) (runtime.DesiredProject, error) {
	const q = `SELECT ` + columns + ` FROM toolbox_projects WHERE project_id = $1`
	row, err := scanRow(s.pool.QueryRow(ctx, q, projectID))
	if errors.Is(err, pgx.ErrNoRows) {
		return runtime.DesiredProject{}, runtime.ErrProjectNotFound
	}
	if err != nil {
		return runtime.DesiredProject{}, fmt.Errorf("runtime/postgres: get %s: %w", projectID, err)
	}
	return row, nil
}

// Delete implements runtime.DesiredStateStore.
func (s *Store) Delete(ctx context.Context, projectID string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM toolbox_projects WHERE project_id = $1`, projectID); err != nil {
		return fmt.Errorf("runtime/postgres: delete %s: %w", projectID, err)
	}
	return nil
}

// ImageHealth is the read-only projection of the last selftest run.
type ImageHealth struct {
	ImageDigest string
	Profile     string
	Passed      int
	Failed      int
	Report      string
}

// RecordImageHealth upserts a selftest result for the dashboard.
func (s *Store) RecordImageHealth(ctx context.Context, h ImageHealth) error {
	const q = `
INSERT INTO toolbox_image_health (image_digest, profile, passed, failed, report, checked_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (image_digest) DO UPDATE SET
    profile = EXCLUDED.profile, passed = EXCLUDED.passed, failed = EXCLUDED.failed,
    report = EXCLUDED.report, checked_at = now()`
	if _, err := s.pool.Exec(ctx, q, h.ImageDigest, h.Profile, h.Passed, h.Failed, h.Report); err != nil {
		return fmt.Errorf("runtime/postgres: record image health: %w", err)
	}
	return nil
}

// ListImageHealth returns the latest health rows, newest first.
func (s *Store) ListImageHealth(ctx context.Context, limit int) ([]ImageHealth, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.pool.Query(ctx,
		`SELECT image_digest, profile, passed, failed, report FROM toolbox_image_health ORDER BY checked_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("runtime/postgres: list image health: %w", err)
	}
	defer rows.Close()
	var out []ImageHealth
	for rows.Next() {
		var h ImageHealth
		if err := rows.Scan(&h.ImageDigest, &h.Profile, &h.Passed, &h.Failed, &h.Report); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// CountActiveProjects returns how many projects still have a live desired state.
func (s *Store) CountActiveProjects(ctx context.Context, instanceID string) (int, error) {
	const q = `SELECT count(*) FROM toolbox_projects WHERE instance_id = $1 AND desired_state <> 'removed'`
	var n int
	if err := s.pool.QueryRow(ctx, q, instanceID).Scan(&n); err != nil {
		return 0, fmt.Errorf("runtime/postgres: count active projects: %w", err)
	}
	return n, nil
}
