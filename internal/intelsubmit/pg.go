// pg.go — the PostgreSQL receipt store. It owns its pool (the repo's
// per-subsystem pool convention) and runs the shared migrator, so the table
// exists before any receipt is written.
package intelsubmit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	infopg "github.com/c1ayooo/xalgorix/v4/internal/infoitem/postgres"
)

// PGStore is the PostgreSQL authority for submission receipts.
type PGStore struct {
	pool *pgxpool.Pool
}

// Open connects, applies the shared migrations and returns the store.
func Open(ctx context.Context, dsn string) (*PGStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("intelsubmit: empty database URL")
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("intelsubmit: parse database URL: %w", err)
	}
	if poolConfig.MaxConns < 2 || poolConfig.MaxConns > 8 {
		poolConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("intelsubmit: create PostgreSQL pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("intelsubmit: PostgreSQL unavailable: %w", err)
	}
	if err := infopg.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &PGStore{pool: pool}, nil
}

// Close releases the pool.
func (s *PGStore) Close() error {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
	return nil
}

const submitColumns = `
	id, project_id, task_id, unique_code, fingerprint, endpoint, state,
	awarded, attempts, last_response, last_attempt_at, created_at, updated_at`

func scanSubmit(row pgx.Row) (Submit, error) {
	var s Submit
	if err := row.Scan(
		&s.ID, &s.ProjectID, &s.TaskID, &s.UniqueCode, &s.Fingerprint, &s.Endpoint,
		&s.State, &s.Awarded, &s.Attempts, &s.LastResponse, &s.LastAttemptAt,
		&s.CreatedAt, &s.UpdatedAt,
	); err != nil {
		return Submit{}, err
	}
	return s, nil
}

// upsertSQL keeps two invariants the Go layer would otherwise have to re-read
// for: a scored challenge is never downgraded to pending/invalid by a later
// start or close, and awarded only ever grows (it comes from the platform).
const upsertSQL = `
INSERT INTO intel_submit (
	project_id, task_id, unique_code, fingerprint, endpoint, state,
	awarded, attempts, last_response, last_attempt_at
) VALUES (
	$1, $2, $3, $4, $5, COALESCE(NULLIF($6, ''), 'pending'),
	$7, CASE WHEN $8 THEN 1 ELSE 0 END, $9,
	CASE WHEN $8 THEN now() ELSE NULL END
)
ON CONFLICT (task_id, unique_code) DO UPDATE SET
	fingerprint = CASE WHEN EXCLUDED.fingerprint <> ''
	                   THEN EXCLUDED.fingerprint ELSE intel_submit.fingerprint END,
	endpoint    = CASE WHEN EXCLUDED.endpoint <> ''
	                   THEN EXCLUDED.endpoint ELSE intel_submit.endpoint END,
	state = CASE
		WHEN intel_submit.state = 'submitted' AND EXCLUDED.state <> 'submitted'
			THEN intel_submit.state
		WHEN intel_submit.state = 'duplicate' AND EXCLUDED.state = 'pending'
			THEN intel_submit.state
		ELSE EXCLUDED.state
	END,
	awarded = GREATEST(intel_submit.awarded, EXCLUDED.awarded),
	attempts = intel_submit.attempts + EXCLUDED.attempts,
	last_response = CASE WHEN EXCLUDED.last_response <> ''
	                     THEN EXCLUDED.last_response ELSE intel_submit.last_response END,
	last_attempt_at = COALESCE(EXCLUDED.last_attempt_at, intel_submit.last_attempt_at),
	updated_at = now()
RETURNING` + submitColumns

// Upsert writes one receipt observation.
func (s *PGStore) Upsert(ctx context.Context, in UpsertInput) (Submit, error) {
	if s == nil || s.pool == nil {
		return Submit{}, errors.New("intelsubmit: nil store")
	}
	taskID := strings.TrimSpace(in.TaskID)
	code := strings.TrimSpace(in.UniqueCode)
	if taskID == "" {
		return Submit{}, errors.New("intelsubmit: task_id is required")
	}
	if code == "" {
		return Submit{}, errors.New("intelsubmit: unique_code is required")
	}
	if in.State != "" && !in.State.Valid() {
		return Submit{}, fmt.Errorf("intelsubmit: invalid state %q", in.State)
	}
	if in.State == StatePending && in.Awarded != 0 {
		return Submit{}, errors.New("intelsubmit: pending receipts cannot carry awarded points")
	}
	row := s.pool.QueryRow(ctx, upsertSQL,
		in.ProjectID, taskID, code, in.Fingerprint, in.Endpoint,
		string(in.State), in.Awarded, in.Attempted, in.Response,
	)
	out, err := scanSubmit(row)
	if err != nil {
		return Submit{}, fmt.Errorf("intelsubmit: upsert %s/%s: %w", taskID, code, err)
	}
	return out, nil
}

const getSQL = `
SELECT` + submitColumns + `
FROM intel_submit
WHERE task_id = $1 AND unique_code = $2`

// Get returns one receipt, or ok=false when the challenge was never observed.
func (s *PGStore) Get(ctx context.Context, taskID, uniqueCode string) (Submit, bool, error) {
	if s == nil || s.pool == nil {
		return Submit{}, false, nil
	}
	out, err := scanSubmit(s.pool.QueryRow(ctx, getSQL, taskID, uniqueCode))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Submit{}, false, nil
		}
		return Submit{}, false, fmt.Errorf("intelsubmit: get %s/%s: %w", taskID, uniqueCode, err)
	}
	return out, true, nil
}

const listByStateSQL = `
SELECT` + submitColumns + `
FROM intel_submit
WHERE task_id = $1 AND state = $2
ORDER BY updated_at ASC
LIMIT $3`

// ListPending returns the handoff list: unsubmitted receipts, oldest first.
func (s *PGStore) ListPending(ctx context.Context, taskID string, limit int) ([]Submit, error) {
	return s.listByState(ctx, taskID, StatePending, limit)
}

func (s *PGStore) listByState(ctx context.Context, taskID string, state State, limit int) ([]Submit, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, listByStateSQL, taskID, string(state), limit)
	if err != nil {
		return nil, fmt.Errorf("intelsubmit: list %s: %w", state, err)
	}
	defer rows.Close()
	out := make([]Submit, 0, limit)
	for rows.Next() {
		item, err := scanSubmit(rows)
		if err != nil {
			return nil, fmt.Errorf("intelsubmit: scan %s: %w", state, err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

const listByTaskSQL = `
SELECT` + submitColumns + `
FROM intel_submit
WHERE task_id = $1
ORDER BY updated_at DESC
LIMIT $2`

// ListByTask returns every receipt for a task, newest update first. The
// intelligence panel joins this to decorate items with their submit state.
func (s *PGStore) ListByTask(ctx context.Context, taskID string, limit int) ([]Submit, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, listByTaskSQL, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("intelsubmit: list task: %w", err)
	}
	defer rows.Close()
	out := make([]Submit, 0, limit)
	for rows.Next() {
		item, err := scanSubmit(rows)
		if err != nil {
			return nil, fmt.Errorf("intelsubmit: scan task: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

const invalidateByCodeSQL = `
UPDATE intel_submit
SET state = 'invalid',
    last_response = CASE WHEN $3 <> '' THEN $3 ELSE last_response END,
    updated_at = now()
WHERE task_id = $1 AND unique_code = $2 AND state = 'pending'`

// InvalidateByCode closes one pending receipt by challenge code.
func (s *PGStore) InvalidateByCode(ctx context.Context, taskID, uniqueCode, reason string) (int, error) {
	if s == nil || s.pool == nil {
		return 0, nil
	}
	taskID = strings.TrimSpace(taskID)
	code := strings.TrimSpace(uniqueCode)
	if taskID == "" || code == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, invalidateByCodeSQL, taskID, code, reason)
	if err != nil {
		return 0, fmt.Errorf("intelsubmit: invalidate %s/%s: %w", taskID, code, err)
	}
	return int(tag.RowsAffected()), nil
}

const invalidateByEndpointSQL = `
UPDATE intel_submit
SET state = 'invalid',
    last_response = CASE WHEN $3 <> '' THEN $3 ELSE last_response END,
    updated_at = now()
WHERE task_id = $1 AND endpoint = $2 AND state = 'pending'`

// InvalidateByEndpoint closes every pending receipt from one container. A
// closed container takes its flag with it, so the pending row becomes a stale
// fact rather than a debt someone keeps chasing.
func (s *PGStore) InvalidateByEndpoint(ctx context.Context, taskID, endpoint, reason string) (int, error) {
	if s == nil || s.pool == nil {
		return 0, nil
	}
	taskID = strings.TrimSpace(taskID)
	endpoint = strings.TrimSpace(endpoint)
	if taskID == "" || endpoint == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, invalidateByEndpointSQL, taskID, endpoint, reason)
	if err != nil {
		return 0, fmt.Errorf("intelsubmit: invalidate %s/%s: %w", taskID, endpoint, err)
	}
	return int(tag.RowsAffected()), nil
}
