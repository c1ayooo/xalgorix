// pg.go — the production projection store. It owns its pool (the repo's
// per-subsystem pool convention) and runs the shared migrator, so the access
// tables exist before any conclusion derives into them.

package intelaccess

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	infopg "github.com/c1ayooo/xalgorix/v4/internal/infoitem/postgres"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// Querier is the write surface derivation needs. It is satisfied by
// *pgxpool.Pool, pgx.Tx and graph.Tx, which is how the same derivation runs
// both standalone and inside a conclusion transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PGStore is the PostgreSQL authority for the reachability projection.
type PGStore struct {
	pool *pgxpool.Pool
}

// Open connects, applies the shared migrations and returns the store.
func Open(ctx context.Context, dsn string) (*PGStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("intelaccess: empty database URL")
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("intelaccess: parse database URL: %w", err)
	}
	if poolConfig.MaxConns < 2 || poolConfig.MaxConns > 8 {
		poolConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("intelaccess: create PostgreSQL pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("intelaccess: PostgreSQL unavailable: %w", err)
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

// accessColumns is the shared projection column list; targets are aggregated
// in a correlated subquery so every read returns one complete row.
const accessColumns = `
	a.id, a.project_id, a.scan_id, a.fingerprint, a.kind, a.subject, a.state,
	COALESCE(a.source_intent_id, 0), a.confirmed_by, a.confirmed_at, a.expires_at,
	a.last_used_at, a.use_count, a.fail_count, a.revoke_reason, a.created_at, a.updated_at,
	COALESCE((SELECT array_agg(t.target ORDER BY t.target)
	          FROM intel_access_targets t WHERE t.access_id = a.id), '{}')`

// DeriveScan projects every item of one scan into access rows.
func (s *PGStore) DeriveScan(ctx context.Context, scanID string, items []intelligence.Item) (int, error) {
	return s.DeriveTx(ctx, s.pool, scanID, 0, items)
}

// DeriveScanForIntent adds first-wins attribution to the projection.
func (s *PGStore) DeriveScanForIntent(ctx context.Context, scanID string, intentID int64, items []intelligence.Item) (int, error) {
	return s.DeriveTx(ctx, s.pool, scanID, intentID, items)
}

// DeriveTx is the derivation used inside a conclusion transaction. It is
// idempotent per (scan_id, fingerprint): a re-derivation updates the shape
// (kind/subject) and only ever adds targets — it never touches state,
// confirmations, counters or manual grants.
func (s *PGStore) DeriveTx(ctx context.Context, q Querier, scanID string, intentID int64, items []intelligence.Item) (int, error) {
	scanID = strings.TrimSpace(scanID)
	if scanID == "" {
		return 0, nil
	}
	derived := deriveAll(items)
	if len(derived) == 0 {
		return 0, nil
	}
	for _, d := range derived {
		var accessID int64
		var sourceIntent *int64
		if intentID > 0 {
			sourceIntent = &intentID
		}
		err := q.QueryRow(ctx, `
			INSERT INTO intel_access (scan_id, fingerprint, kind, subject, state, source_intent_id)
			VALUES ($1, $2, $3, $4, 'proposed', $5)
			ON CONFLICT (scan_id, fingerprint) DO UPDATE SET
				kind = EXCLUDED.kind,
				subject = EXCLUDED.subject,
				updated_at = now()
			RETURNING id`,
			scanID, d.Fingerprint, d.Kind, d.Subject, sourceIntent,
		).Scan(&accessID)
		if err != nil {
			return 0, fmt.Errorf("intelaccess: derive %s/%s: %w", scanID, d.Fingerprint, err)
		}
		if len(d.Targets) == 0 {
			continue
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO intel_access_targets (access_id, target)
			SELECT $1, unnest($2::text[])
			ON CONFLICT DO NOTHING`,
			accessID, d.Targets,
		); err != nil {
			return 0, fmt.Errorf("intelaccess: derive targets %s/%s: %w", scanID, d.Fingerprint, err)
		}
	}
	return len(derived), nil
}

// Get returns one row.
func (s *PGStore) Get(ctx context.Context, scanID, fingerprint string) (Access, bool, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+accessColumns+` FROM intel_access a WHERE a.scan_id = $1 AND a.fingerprint = $2`,
		strings.TrimSpace(scanID), strings.TrimSpace(fingerprint),
	)
	access, err := scanAccess(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Access{}, false, nil
	}
	if err != nil {
		return Access{}, false, err
	}
	return access, true, nil
}

// ListUsable returns usable rows, optionally narrowed to one exact target.
func (s *PGStore) ListUsable(ctx context.Context, scanID, target string, limit int) ([]Access, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+accessColumns+`
		FROM intel_access a
		WHERE a.scan_id = $1
		  AND a.state IN ('confirmed','active')
		  AND (a.expires_at IS NULL OR a.expires_at > now())
		  AND ($2 = '' OR EXISTS (
		      SELECT 1 FROM intel_access_targets f WHERE f.access_id = a.id AND f.target = $2))
		ORDER BY COALESCE(a.confirmed_at, a.updated_at) DESC, a.fingerprint ASC
		LIMIT $3`,
		strings.TrimSpace(scanID), strings.TrimSpace(target), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("intelaccess: list usable: %w", err)
	}
	defer rows.Close()
	out := make([]Access, 0, limit)
	for rows.Next() {
		access, err := scanAccess(rows)
		if err != nil {
			return nil, fmt.Errorf("intelaccess: list usable scan: %w", err)
		}
		out = append(out, access)
	}
	return out, rows.Err()
}

// Lookup resolves one fingerprint across scans, preferring a usable row.
func (s *PGStore) Lookup(ctx context.Context, scanIDs []string, fingerprint string) (Access, bool, error) {
	if len(scanIDs) == 0 {
		return Access{}, false, nil
	}
	row := s.pool.QueryRow(ctx, `
		SELECT `+accessColumns+`
		FROM intel_access a
		WHERE a.scan_id = ANY($1) AND a.fingerprint = $2
		ORDER BY (a.state IN ('confirmed','active')
		          AND (a.expires_at IS NULL OR a.expires_at > now())) DESC,
		         a.updated_at DESC
		LIMIT 1`,
		scanIDs, strings.TrimSpace(fingerprint),
	)
	access, err := scanAccess(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Access{}, false, nil
	}
	if err != nil {
		return Access{}, false, err
	}
	return access, true, nil
}

// Confirm upserts the operator endorsement (creating the row when triage came
// before the next conclusion).
func (s *PGStore) Confirm(ctx context.Context, in ConfirmInput) (Access, error) {
	scanID := strings.TrimSpace(in.ScanID)
	fingerprint := strings.TrimSpace(in.Fingerprint)
	if scanID == "" || fingerprint == "" {
		return Access{}, ErrNotFound
	}
	if strings.TrimSpace(in.Kind) == "" {
		return Access{}, fmt.Errorf("intelaccess: confirm %s/%s requires kind", scanID, fingerprint)
	}
	targets, err := normalizeAll(in.Targets)
	if err != nil {
		return Access{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Access{}, fmt.Errorf("intelaccess: confirm begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only after commit path

	var accessID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO intel_access
			(project_id, scan_id, fingerprint, kind, subject, state,
			 confirmed_by, confirmed_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, 'confirmed', $6, now(), $7)
		ON CONFLICT (scan_id, fingerprint) DO UPDATE SET
			state        = 'confirmed',
			kind         = EXCLUDED.kind,
			subject      = CASE WHEN EXCLUDED.subject <> '' THEN EXCLUDED.subject ELSE intel_access.subject END,
			confirmed_by = EXCLUDED.confirmed_by,
			confirmed_at = now(),
			expires_at   = EXCLUDED.expires_at,
			revoke_reason = '',
			updated_at   = now()
		RETURNING id`,
		in.ProjectID, scanID, fingerprint, in.Kind, in.Subject, in.By, in.ExpiresAt,
	).Scan(&accessID); err != nil {
		return Access{}, fmt.Errorf("intelaccess: confirm %s/%s: %w", scanID, fingerprint, err)
	}
	if len(targets) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO intel_access_targets (access_id, target)
			SELECT $1, unnest($2::text[])
			ON CONFLICT DO NOTHING`,
			accessID, targets,
		); err != nil {
			return Access{}, fmt.Errorf("intelaccess: confirm targets %s/%s: %w", scanID, fingerprint, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Access{}, fmt.Errorf("intelaccess: confirm commit: %w", err)
	}
	access, ok, err := s.Get(ctx, scanID, fingerprint)
	if err != nil {
		return Access{}, err
	}
	if !ok {
		return Access{}, ErrNotFound
	}
	return access, nil
}

// Revoke marks the row inert with an explicit reason.
func (s *PGStore) Revoke(ctx context.Context, scanID, fingerprint, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("intelaccess: revoke %s/%s requires a reason", scanID, fingerprint)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE intel_access
		SET state = 'revoked', revoke_reason = $3, updated_at = now()
		WHERE scan_id = $1 AND fingerprint = $2`,
		strings.TrimSpace(scanID), strings.TrimSpace(fingerprint), reason,
	)
	if err != nil {
		return fmt.Errorf("intelaccess: revoke %s/%s: %w", scanID, fingerprint, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddGrants appends operator targets to an existing row.
func (s *PGStore) AddGrants(ctx context.Context, scanID, fingerprint string, targets []string) (Access, error) {
	normalized, err := normalizeAll(targets)
	if err != nil {
		return Access{}, err
	}
	if len(normalized) == 0 {
		return Access{}, fmt.Errorf("intelaccess: add grants requires at least one target")
	}
	tag, err := s.pool.Exec(ctx, `
		WITH row AS (
			SELECT id FROM intel_access WHERE scan_id = $1 AND fingerprint = $2
		)
		INSERT INTO intel_access_targets (access_id, target)
		SELECT row.id, unnest($3::text[]) FROM row
		ON CONFLICT DO NOTHING`,
		strings.TrimSpace(scanID), strings.TrimSpace(fingerprint), normalized,
	)
	if err != nil {
		return Access{}, fmt.Errorf("intelaccess: add grants %s/%s: %w", scanID, fingerprint, err)
	}
	_ = tag
	access, ok, err := s.Get(ctx, scanID, fingerprint)
	if err != nil {
		return Access{}, err
	}
	if !ok {
		return Access{}, ErrNotFound
	}
	return access, nil
}

// rowScanner is satisfied by pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccess(row rowScanner) (Access, error) {
	var (
		a         Access
		confirmed *time.Time
		expires   *time.Time
		lastUsed  *time.Time
		targets   []string
	)
	if err := row.Scan(
		&a.ID, &a.ProjectID, &a.ScanID, &a.Fingerprint, &a.Kind, &a.Subject, &a.State,
		&a.SourceIntentID, &a.ConfirmedBy, &confirmed, &expires,
		&lastUsed, &a.UseCount, &a.FailCount, &a.RevokeReason, &a.CreatedAt, &a.UpdatedAt,
		&targets,
	); err != nil {
		return Access{}, err
	}
	a.ConfirmedAt = confirmed
	a.ExpiresAt = expires
	a.LastUsedAt = lastUsed
	a.Targets = targets
	return a, nil
}

// ── capabilities (M3) ───────────────────────────────────────────────────────

// FindGrant returns the usable access whose grants cover target.
func (s *PGStore) FindGrant(ctx context.Context, scanID, target string) (Access, bool, error) {
	canonical, ok := NormalizeTarget(target)
	if !ok {
		return Access{}, false, nil
	}
	usable, err := s.ListUsable(ctx, scanID, "", 200)
	if err != nil {
		return Access{}, false, err
	}
	for _, access := range usable {
		if grantMatches(access.Targets, canonical) {
			return access, true, nil
		}
	}
	return Access{}, false, nil
}

// IssueCapabilities upserts one row per target, sharing a batch id.
func (s *PGStore) IssueCapabilities(ctx context.Context, in IssueInput) ([]Capability, error) {
	scanID := strings.TrimSpace(in.ScanID)
	if scanID == "" || in.IntentID <= 0 || in.AccessID <= 0 {
		return nil, ErrNotFound
	}
	if in.UsesLeft <= 0 {
		return nil, fmt.Errorf("intelaccess: uses_left must be > 0")
	}
	if in.TTL <= 0 {
		return nil, fmt.Errorf("intelaccess: ttl must be > 0")
	}
	issuedBy := strings.TrimSpace(in.IssuedBy)
	if issuedBy == "" {
		return nil, fmt.Errorf("intelaccess: issued_by is required")
	}
	targets, err := normalizeAll(in.Targets)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("intelaccess: at least one target is required")
	}
	now := time.Now().UTC()
	batchID := capabilityBatchID(scanID, in.AccessID, now)
	out := make([]Capability, 0, len(targets))
	for _, target := range targets {
		row := s.pool.QueryRow(ctx, `
			INSERT INTO access_capabilities
				(scan_id, intent_id, access_id, target, batch_id, uses_left, expires_at, issued_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (scan_id, intent_id, access_id, target) DO UPDATE SET
				uses_left  = EXCLUDED.uses_left,
				expires_at = EXCLUDED.expires_at,
				issued_by  = EXCLUDED.issued_by,
				batch_id   = EXCLUDED.batch_id,
				created_at = now()
			RETURNING id, scan_id, intent_id, access_id, target, batch_id,
			          uses_left, expires_at, issued_by, created_at`,
			scanID, in.IntentID, in.AccessID, target, batchID, in.UsesLeft,
			now.Add(in.TTL), issuedBy,
		)
		capability, err := scanCapability(row)
		if err != nil {
			return nil, fmt.Errorf("intelaccess: issue capability %s/%s: %w", scanID, target, err)
		}
		out = append(out, capability)
	}
	return out, nil
}

// ConsumeCapability spends one use and bumps the access counters atomically.
func (s *PGStore) ConsumeCapability(ctx context.Context, scanID string, intentID int64, target string) (Capability, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Capability{}, false, fmt.Errorf("intelaccess: consume begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only after commit path

	row := tx.QueryRow(ctx, `
		SELECT id, scan_id, intent_id, access_id, target, batch_id,
		       uses_left, expires_at, issued_by, created_at
		FROM access_capabilities
		WHERE scan_id = $1 AND intent_id = $2 AND target = $3
		  AND uses_left > 0 AND expires_at > now()
		ORDER BY expires_at ASC
		LIMIT 1
		FOR UPDATE`,
		strings.TrimSpace(scanID), intentID, strings.TrimSpace(target),
	)
	capability, err := scanCapability(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Capability{}, false, nil
	}
	if err != nil {
		return Capability{}, false, fmt.Errorf("intelaccess: consume lookup: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE access_capabilities SET uses_left = uses_left - 1 WHERE id = $1`, capability.ID,
	); err != nil {
		return Capability{}, false, fmt.Errorf("intelaccess: consume decrement: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE intel_access
		SET use_count = use_count + 1,
		    last_used_at = now(),
		    state = CASE WHEN state = 'confirmed' THEN 'active' ELSE state END,
		    updated_at = now()
		WHERE id = $1`, capability.AccessID,
	); err != nil {
		return Capability{}, false, fmt.Errorf("intelaccess: consume access counters: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Capability{}, false, fmt.Errorf("intelaccess: consume commit: %w", err)
	}
	capability.UsesLeft--
	return capability, true, nil
}

// ListCapabilities returns one intent's permissions, newest first.
func (s *PGStore) ListCapabilities(ctx context.Context, scanID string, intentID int64) ([]Capability, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, scan_id, intent_id, access_id, target, batch_id,
		       uses_left, expires_at, issued_by, created_at
		FROM access_capabilities
		WHERE scan_id = $1 AND intent_id = $2
		ORDER BY created_at DESC, id DESC`,
		strings.TrimSpace(scanID), intentID,
	)
	if err != nil {
		return nil, fmt.Errorf("intelaccess: list capabilities: %w", err)
	}
	defer rows.Close()
	out := make([]Capability, 0, 4)
	for rows.Next() {
		capability, err := scanCapability(rows)
		if err != nil {
			return nil, fmt.Errorf("intelaccess: list capabilities scan: %w", err)
		}
		out = append(out, capability)
	}
	return out, rows.Err()
}

func scanCapability(row rowScanner) (Capability, error) {
	var c Capability
	if err := row.Scan(
		&c.ID, &c.ScanID, &c.IntentID, &c.AccessID, &c.Target, &c.BatchID,
		&c.UsesLeft, &c.ExpiresAt, &c.IssuedBy, &c.CreatedAt,
	); err != nil {
		return Capability{}, err
	}
	return c, nil
}

// ── audit projection (M4) ───────────────────────────────────────────────────

// RecordUse appends one audit row. A failure is returned for logging only; the
// judge ignores it (the receipt never blocks a dispatch).
func (s *PGStore) RecordUse(ctx context.Context, row UseRecord) error {
	if strings.TrimSpace(row.ScanID) == "" || strings.TrimSpace(row.Decision) == "" {
		return nil
	}
	var usesLeft any
	if row.UsesLeftAfter != nil {
		usesLeft = *row.UsesLeftAfter
	}
	var accessID any
	if row.AccessID != 0 {
		accessID = row.AccessID
	}
	var capabilityID any
	if row.CapabilityID != 0 {
		capabilityID = row.CapabilityID
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO intel_access_use
			(scan_id, intent_id, fingerprint, access_id, target, decision, reason,
			 capability_id, uses_left_after)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		row.ScanID, row.IntentID, row.Fingerprint, accessID, row.Target,
		row.Decision, row.Reason, capabilityID, usesLeft,
	)
	if err != nil {
		return fmt.Errorf("intelaccess: record use: %w", err)
	}
	return nil
}

// ListUses returns the newest audit rows of one run.
func (s *PGStore) ListUses(ctx context.Context, scanID string, limit int) ([]UseRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, scan_id, intent_id, fingerprint, COALESCE(access_id, 0), target,
		       decision, reason, COALESCE(capability_id, 0), uses_left_after, created_at
		FROM intel_access_use
		WHERE scan_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2`,
		strings.TrimSpace(scanID), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("intelaccess: list uses: %w", err)
	}
	defer rows.Close()
	out := make([]UseRecord, 0, limit)
	for rows.Next() {
		var row UseRecord
		if err := rows.Scan(
			&row.ID, &row.ScanID, &row.IntentID, &row.Fingerprint, &row.AccessID,
			&row.Target, &row.Decision, &row.Reason, &row.CapabilityID,
			&row.UsesLeftAfter, &row.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("intelaccess: list uses scan: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// RecordFailure increments the access failure counter and revokes at threshold.
func (s *PGStore) RecordFailure(ctx context.Context, scanID, fingerprint string, threshold int) (Access, bool, error) {
	if threshold <= 0 {
		threshold = 3
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE intel_access
		SET fail_count = fail_count + 1,
		    state = CASE WHEN fail_count + 1 >= $3 THEN 'revoked' ELSE state END,
		    revoke_reason = CASE WHEN fail_count + 1 >= $3 THEN 'repeated dispatch failure' ELSE revoke_reason END,
		    updated_at = now()
		WHERE scan_id = $1 AND fingerprint = $2`,
		strings.TrimSpace(scanID), strings.TrimSpace(fingerprint), threshold,
	)
	if err != nil {
		return Access{}, false, fmt.Errorf("intelaccess: record failure: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Access{}, false, ErrNotFound
	}
	access, ok, err := s.Get(ctx, scanID, fingerprint)
	if err != nil || !ok {
		return Access{}, false, err
	}
	return access, access.State == StateRevoked, nil
}

// ListAccess returns every access row of one run.
func (s *PGStore) ListAccess(ctx context.Context, scanID string) ([]Access, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+accessColumns+`
		FROM intel_access a
		WHERE a.scan_id = $1
		ORDER BY a.updated_at DESC, a.fingerprint ASC`,
		strings.TrimSpace(scanID),
	)
	if err != nil {
		return nil, fmt.Errorf("intelaccess: list access: %w", err)
	}
	defer rows.Close()
	out := make([]Access, 0, 8)
	for rows.Next() {
		access, err := scanAccess(rows)
		if err != nil {
			return nil, fmt.Errorf("intelaccess: list access scan: %w", err)
		}
		out = append(out, access)
	}
	return out, rows.Err()
}

// ListScanCapabilities returns the run's permissions, newest first.
func (s *PGStore) ListScanCapabilities(ctx context.Context, scanID string, limit int) ([]Capability, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, scan_id, intent_id, access_id, target, batch_id,
		       uses_left, expires_at, issued_by, created_at
		FROM access_capabilities
		WHERE scan_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2`,
		strings.TrimSpace(scanID), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("intelaccess: list scan capabilities: %w", err)
	}
	defer rows.Close()
	out := make([]Capability, 0, 4)
	for rows.Next() {
		capability, err := scanCapability(rows)
		if err != nil {
			return nil, fmt.Errorf("intelaccess: list scan capabilities scan: %w", err)
		}
		out = append(out, capability)
	}
	return out, rows.Err()
}
