package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	infopg "github.com/c1ayooo/xalgorix/v4/internal/infoitem/postgres"
)

// ErrNotFound is returned by GetNode for an unknown (scan, node) pair.
var ErrNotFound = errors.New("graph: node not found")

// PGStore is the production PostgreSQL authority. The shared migrator owns
// the schema; Open applies it (idempotent) so CLI/test callers cannot run
// against a half-migrated database.
type PGStore struct {
	pool *pgxpool.Pool
}

// Open validates the DSN, clamps the pool, applies the embedded migrations
// and returns the PG graph authority.
func Open(ctx context.Context, dsn string) (*PGStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("graph: empty database URL")
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("graph: parse database URL: %w", err)
	}
	if poolConfig.MaxConns < 2 || poolConfig.MaxConns > 8 {
		poolConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("graph: create PostgreSQL pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("graph: PostgreSQL unavailable: %w", err)
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

// AddNode inserts one authoritative node.
func (s *PGStore) AddNode(ctx context.Context, in NodeInput) (*Node, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO exploration_nodes
			(scan_id, instance_id, unit_key, kind, state, priority, origin, owner, lease_until, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+nodeColumns,
		in.ScanID, in.InstanceID, in.UnitKey, string(in.Kind), string(in.State),
		in.Priority, in.Origin, in.Owner, in.LeaseUntil, in.Payload,
	)
	return scanNode(row)
}

// rowQuerier is satisfied by *pgxpool.Pool and pgx.Tx; it lets the same
// upsert statement run on the pool or inside a conclusion transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// UpsertNode inserts or updates the node identified by (scan, kind, unit_key).
// A node without a unit key is a plain insert (lineage-only rows).
func (s *PGStore) UpsertNode(ctx context.Context, in NodeInput) (*Node, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.UnitKey) == "" {
		return s.AddNode(ctx, in)
	}
	return upsertNodeRow(ctx, s.pool, in)
}

// upsertNodeRow is the shared idempotent node write. The caller must have
// already normalized in.
func upsertNodeRow(ctx context.Context, q rowQuerier, in NodeInput) (*Node, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO exploration_nodes
			(scan_id, instance_id, unit_key, kind, state, priority, origin, owner, lease_until, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (scan_id, kind, unit_key) WHERE unit_key <> ''
		DO UPDATE SET
			instance_id = EXCLUDED.instance_id,
			state = EXCLUDED.state,
			priority = EXCLUDED.priority,
			origin = EXCLUDED.origin,
			owner = EXCLUDED.owner,
			lease_until = EXCLUDED.lease_until,
			payload = EXCLUDED.payload
		RETURNING `+nodeColumns,
		in.ScanID, in.InstanceID, in.UnitKey, string(in.Kind), string(in.State),
		in.Priority, in.Origin, in.Owner, in.LeaseUntil, in.Payload,
	)
	return scanNode(row)
}

// AddEdge inserts one lineage edge; duplicate edges are idempotent.
func (s *PGStore) AddEdge(ctx context.Context, in EdgeInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO exploration_edges (scan_id, src_id, rel, dst_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (scan_id, src_id, rel, dst_id) DO NOTHING`,
		in.ScanID, in.SrcID, string(in.Rel), in.DstID,
	)
	if err != nil {
		return fmt.Errorf("graph: add edge %d -%s-> %d: %w", in.SrcID, in.Rel, in.DstID, err)
	}
	return nil
}

// UpdateNode rewrites one intent's payload/priority (build_plan refresh).
// State is deliberately not touched; callers use Transition for lifecycle.
func (s *PGStore) UpdateNode(ctx context.Context, scanID string, id int64, payload string, priority int) error {
	if !json.Valid([]byte(payload)) {
		return fmt.Errorf("graph: payload is not valid JSON")
	}
	ct, err := s.pool.Exec(ctx, `
		UPDATE exploration_nodes
		SET payload = $3, priority = $4
		WHERE scan_id = $1 AND id = $2 AND kind = 'intent'`,
		scanID, id, payload, priority,
	)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceDependencyEdges rewrites the depends_on edges into one intent.
func (s *PGStore) ReplaceDependencyEdges(ctx context.Context, scanID string, dstID int64, srcIDs []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only after commit path
	if _, err := tx.Exec(ctx,
		`DELETE FROM exploration_edges WHERE scan_id = $1 AND dst_id = $2 AND rel = 'depends_on'`,
		scanID, dstID,
	); err != nil {
		return err
	}
	for _, src := range srcIDs {
		if src <= 0 || src == dstID {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO exploration_edges (scan_id, src_id, rel, dst_id)
			VALUES ($1, $2, 'depends_on', $3)
			ON CONFLICT (scan_id, src_id, rel, dst_id) DO NOTHING`,
			scanID, src, dstID,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetNode returns one node or ErrNotFound.
func (s *PGStore) GetNode(ctx context.Context, scanID string, id int64) (*Node, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+nodeColumns+` FROM exploration_nodes WHERE scan_id = $1 AND id = $2`,
		scanID, id,
	)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

// ListNodes returns nodes of one kind, optionally filtered by state.
func (s *PGStore) ListNodes(ctx context.Context, scanID string, kind Kind, states ...State) ([]Node, error) {
	if !ValidKind(kind) {
		return nil, fmt.Errorf("graph: invalid kind %q", kind)
	}
	args := []any{scanID, string(kind)}
	query := `SELECT ` + nodeColumns + ` FROM exploration_nodes WHERE scan_id = $1 AND kind = $2`
	if len(states) > 0 {
		raw := make([]string, 0, len(states))
		for _, st := range states {
			raw = append(raw, string(st))
		}
		args = append(args, raw)
		query += ` AND state = ANY($3)`
	}
	query += ` ORDER BY priority DESC, id ASC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// ListEdges returns every lineage edge in one scan.
func (s *PGStore) ListEdges(ctx context.Context, scanID string) ([]Edge, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT src_id, rel, dst_id FROM exploration_edges WHERE scan_id = $1 ORDER BY src_id, rel, dst_id`,
		scanID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.SrcID, &e.Rel, &e.DstID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClaimNextIntent atomically claims the next ready open intent for owner.
// Expired leases are reclaimed in the same transaction. Dependencies are
// lineage edges: a parent intent/belief must be terminal, otherwise the
// child is skipped until the parent settles. The intent contract is enforced
// in the same statement: operator confirmation (when required) and every
// (kind, unit_key, state) precondition must already hold.
func (s *PGStore) ClaimNextIntent(ctx context.Context, scanID, owner string, ttl time.Duration) (*Node, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("graph: begin claim: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only after commit path
	if _, err := reclaimExpiredTx(ctx, tx, scanID); err != nil {
		return nil, err
	}
	row := tx.QueryRow(ctx, `
		WITH candidate AS (
			SELECT n.id
			FROM exploration_nodes n
			WHERE n.scan_id = $1
			  AND n.kind = 'intent'
			  AND n.state = 'open'
			  AND jsonb_typeof(n.payload::jsonb) = 'object'
			  AND NOT EXISTS (
				  SELECT 1
				  FROM exploration_edges e
				  JOIN exploration_nodes p
				    ON p.scan_id = e.scan_id AND p.id = e.src_id
				  WHERE e.scan_id = n.scan_id
				    AND e.dst_id = n.id
				    AND e.rel IN ('spawns','derived_from','depends_on')
				    AND p.kind IN ('intent','belief')
				    AND NOT (
					  (p.kind = 'intent' AND p.state IN ('done','blocked','exhausted','stopped','deleted'))
					  OR
					  (p.kind = 'belief' AND p.state IN ('proven','rejected','blocked','exhausted'))
				    )
			  )
			  AND (
				  (n.payload::jsonb -> 'contract') IS NULL
				  OR (n.payload::jsonb -> 'contract') = 'null'::jsonb
				  OR (
					  jsonb_typeof(n.payload::jsonb -> 'contract') = 'object'
					  AND (
						  COALESCE(n.payload::jsonb -> 'contract' ->> 'requires_confirmation', 'false')
						      NOT IN ('true','1')
						  OR COALESCE(n.payload::jsonb -> 'contract' ->> 'confirmed_at', '') <> ''
					  )
					  AND (
						  jsonb_typeof(n.payload::jsonb -> 'contract' -> 'preconditions')
						      IS DISTINCT FROM 'array'
						  OR NOT EXISTS (
							  SELECT 1
							  FROM jsonb_array_elements(
								  n.payload::jsonb -> 'contract' -> 'preconditions'
							  ) AS pre
							  WHERE NOT EXISTS (
								  SELECT 1
								  FROM exploration_nodes pn
								  WHERE pn.scan_id = n.scan_id
								    AND pn.kind = pre ->> 'ref_kind'
								    AND pn.unit_key = pre ->> 'ref'
								    AND pn.state = pre ->> 'need_state'
							  )
						  )
					  )
				  )
			  )
			ORDER BY n.priority DESC, n.id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE exploration_nodes n
		SET state = 'running',
		    owner = $2,
		    lease_until = now() + ($3 * interval '1 second'),
		    completed_at = NULL
		FROM candidate c
		WHERE n.id = c.id
		RETURNING `+returningNodeColumns,
		scanID, owner, int(ttl.Seconds()),
	)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		if cerr := tx.Commit(ctx); cerr != nil {
			return nil, cerr
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("graph: commit claim: %w", err)
	}
	return n, nil
}

// RenewLease extends a live claim. False means the caller lost ownership.
func (s *PGStore) RenewLease(ctx context.Context, scanID string, id int64, owner string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	ct, err := s.pool.Exec(ctx, `
		UPDATE exploration_nodes
		SET lease_until = now() + ($4 * interval '1 second')
		WHERE scan_id = $1 AND id = $2 AND owner = $3 AND kind = 'intent' AND state = 'running'`,
		scanID, id, owner, int(ttl.Seconds()),
	)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// ReclaimExpired returns expired running intents to open and expired testing
// beliefs to queued. It is also the first step of ClaimNextIntent.
func (s *PGStore) ReclaimExpired(ctx context.Context, scanID string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only after commit path
	n, err := reclaimExpiredTx(ctx, tx, scanID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

// ResetRunning is the process-start/session-restore recovery: every running
// intent goes back to open and every testing belief to queued. A crashed
// worker must never leave a phantom owner behind.
func (s *PGStore) ResetRunning(ctx context.Context, scanID string) (int64, error) {
	ct, err := s.pool.Exec(ctx, `
		UPDATE exploration_nodes
		SET state = CASE WHEN kind = 'intent' THEN 'open' ELSE 'queued' END,
		    owner = '',
		    lease_until = NULL
		WHERE scan_id = $1
		  AND ((kind = 'intent' AND state = 'running')
		    OR (kind = 'belief' AND state = 'testing'))`,
		scanID,
	)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// Transition performs one CAS state transition. False means the expected
// state no longer holds (another worker/planner already settled it).
//
// intent -> done is structurally refused: done requires the owner/lease/
// confirmation/precondition checks and the yields receipt that only
// ConcludeIntent (or ConcludePlanned for the plan surface) performs in one
// transaction. Settling to blocked/exhausted/stopped stays a plain CAS.
func (s *PGStore) Transition(ctx context.Context, scanID string, id int64, kind Kind, from, to State) (bool, error) {
	if !ValidKind(kind) {
		return false, fmt.Errorf("graph: invalid kind %q", kind)
	}
	if kind == KindIntent && to == StateDone {
		return false, fmt.Errorf("graph: intent %d: done is reachable only via ConcludeIntent (owner/lease/confirmation/precondition checks + yields receipt), not Transition", id)
	}
	if !ValidState(kind, from) || !ValidState(kind, to) {
		return false, fmt.Errorf("graph: invalid transition %s %s -> %s", kind, from, to)
	}
	terminal := TerminalState(kind, to)
	openState := to == StateOpen || to == StateQueued
	ct, err := s.pool.Exec(ctx, `
		UPDATE exploration_nodes
		SET state = $4,
		    owner = CASE WHEN $5 OR $6 THEN '' ELSE owner END,
		    lease_until = CASE WHEN $5 OR $6 THEN NULL ELSE lease_until END,
		    completed_at = CASE WHEN $5 THEN now() ELSE NULL END
		WHERE scan_id = $1 AND id = $2 AND kind = $3 AND state = $7`,
		scanID, id, string(kind), string(to), terminal, openState, string(from),
	)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// SettleIntent is the worker-owned terminal transition. Unlike Transition it
// fences on the owner, so an old worker whose expired lease was reclaimed by a
// new claimant cannot rewrite the new worker's running row. The state change
// and the audit reason are one statement: a terminal row never exists without
// its engine reason.
func (s *PGStore) SettleIntent(ctx context.Context, scanID string, id int64, owner string, to State, reason string) (bool, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return false, fmt.Errorf("graph: settle intent %d: owner is required", id)
	}
	// StatePaused parks the intent for an operator decision (INTEL_HANDOFF_SPEC
	// M3: a capability request); it is the one non-terminal settlement.
	if to != StateBlocked && to != StateExhausted && to != StateStopped && to != StateOpen && to != StatePaused {
		return false, fmt.Errorf("graph: settle intent %d: %q is not a worker settlement target", id, to)
	}
	terminal := TerminalState(KindIntent, to)
	parked := to == StatePaused
	ct, err := s.pool.Exec(ctx, `
		UPDATE exploration_nodes
		SET state = $4,
		    owner = '',
		    lease_until = NULL,
		    completed_at = CASE WHEN $5 THEN now() ELSE NULL END,
		    blocked_reason = CASE WHEN ($5 OR $7) AND $6 <> '' THEN $6 ELSE blocked_reason END,
		    updated_at = now()
		WHERE scan_id = $1 AND id = $2 AND kind = 'intent'
		  AND state = 'running' AND owner = $3`,
		scanID, id, owner, string(to), terminal, strings.TrimSpace(reason), parked,
	)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// ConcludeIntent settles one claimed intent and its produced nodes in a
// single transaction. done requires at least one produced node and writes a
// yields edge for each; blocked/exhausted/stopped must not produce nodes.
// Owner, lease, confirmation and preconditions are re-checked here because a
// claim is not a promise: the dependency may have changed mid-flight.
func (s *PGStore) ConcludeIntent(ctx context.Context, scanID string, id int64, owner string, to State, produced []NodeInput) (*Node, error) {
	return s.conclude(ctx, scanID, id, to, produced, concludeMode{owner: owner, requireClaim: true})
}

// ConcludePlanned settles an intent from the plan surface without a worker
// claim. Only plan_graph may call it; the same receipt/yield rules apply.
func (s *PGStore) ConcludePlanned(ctx context.Context, scanID string, id int64, to State, produced []NodeInput) (*Node, error) {
	return s.conclude(ctx, scanID, id, to, produced, concludeMode{})
}

type concludeMode struct {
	owner        string
	requireClaim bool
}

func (s *PGStore) conclude(ctx context.Context, scanID string, id int64, to State, produced []NodeInput, mode concludeMode) (*Node, error) {
	if !ValidState(KindIntent, to) || !TerminalState(KindIntent, to) {
		return nil, fmt.Errorf("graph: conclude intent: %q is not a terminal intent state", to)
	}
	if to == StateDone && len(produced) == 0 {
		return nil, ErrNoProduct
	}
	if to != StateDone && len(produced) > 0 {
		return nil, fmt.Errorf("graph: conclude intent: %s must not produce nodes", to)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("graph: begin conclude: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only after commit path

	row := tx.QueryRow(ctx,
		`SELECT `+nodeColumns+` FROM exploration_nodes
		 WHERE scan_id = $1 AND id = $2 AND kind = 'intent'
		 FOR UPDATE`,
		scanID, id,
	)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if mode.requireClaim {
		if n.State != StateRunning || n.Owner != mode.owner {
			return nil, fmt.Errorf("%w: intent %d is %s owned by %q", ErrConflict, n.ID, n.State, n.Owner)
		}
		if n.LeaseUntil != nil && n.LeaseUntil.Before(time.Now().UTC()) {
			return nil, fmt.Errorf("%w: intent %d lease expired at %s", ErrLeaseExpired, n.ID, n.LeaseUntil.UTC().Format(time.RFC3339))
		}
	} else if n.State != StateOpen && n.State != StateRunning && n.State != StatePaused {
		return nil, fmt.Errorf("%w: intent %d is %s", ErrConflict, n.ID, n.State)
	} else if n.State == StateRunning && strings.HasPrefix(n.Owner, "work#") {
		// A worker owns a live lease; the plan surface must not settle it
		// underneath the worker. The worker's ConcludeIntent is authoritative.
		return nil, fmt.Errorf("%w: intent %d is owned by live worker %q", ErrConflict, n.ID, n.Owner)
	}

	contract, err := ParseContractStrict(n.Payload)
	if err != nil {
		return nil, fmt.Errorf("graph: conclude intent %d: %w", n.ID, err)
	}
	if to == StateDone && contract.NeedsConfirmation() {
		return nil, fmt.Errorf("%w: intent %d", ErrConfirmationRequired, n.ID)
	}
	if to == StateDone {
		if err := ValidateProducedYields(contract, produced); err != nil {
			return nil, fmt.Errorf("graph: conclude intent %d: %w", n.ID, err)
		}
	}
	for i := range produced {
		in := &produced[i]
		in.ScanID = scanID
		if in.Kind == "" {
			in.Kind = KindFact
		}
		if in.State == "" {
			in.State = StateOrigin
		}
		if in.Kind != KindFact && in.Kind != KindFinding {
			return nil, fmt.Errorf("graph: conclude intent %d: produced kind %q must be fact or finding", n.ID, in.Kind)
		}
		if strings.TrimSpace(in.UnitKey) == "" {
			return nil, fmt.Errorf("graph: conclude intent %d: produced node requires a unit_key", n.ID)
		}
		if in.Origin == "" {
			in.Origin = "intent:" + n.UnitKey
		}
		if err := in.Normalize(); err != nil {
			return nil, err
		}
	}
	if to == StateDone {
		if err := checkPreconditionsTx(ctx, tx, scanID, contract); err != nil {
			return nil, err
		}
	}

	for i := range produced {
		pn, err := upsertNodeRow(ctx, tx, produced[i])
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO exploration_edges (scan_id, src_id, rel, dst_id)
			VALUES ($1, $2, 'yields', $3)
			ON CONFLICT (scan_id, src_id, rel, dst_id) DO NOTHING`,
			scanID, n.ID, pn.ID,
		); err != nil {
			return nil, fmt.Errorf("graph: conclude intent %d: yields edge: %w", n.ID, err)
		}
	}

	var ct pgconn.CommandTag
	if mode.requireClaim {
		ct, err = tx.Exec(ctx, `
			UPDATE exploration_nodes
			SET state = $3, owner = '', lease_until = NULL, completed_at = now()
			WHERE scan_id = $1 AND id = $2 AND kind = 'intent'
			  AND state = 'running' AND owner = $4`,
			scanID, id, string(to), mode.owner,
		)
	} else {
		ct, err = tx.Exec(ctx, `
			UPDATE exploration_nodes
			SET state = $3, owner = '', lease_until = NULL, completed_at = now()
			WHERE scan_id = $1 AND id = $2 AND kind = 'intent'
			  AND state IN ('open','running','paused')`,
			scanID, id, string(to),
		)
	}
	if err != nil {
		return nil, err
	}
	if ct.RowsAffected() != 1 {
		return nil, fmt.Errorf("%w: intent %d changed during conclusion", ErrConflict, id)
	}
	row = tx.QueryRow(ctx,
		`SELECT `+nodeColumns+` FROM exploration_nodes WHERE scan_id = $1 AND id = $2`,
		scanID, id,
	)
	out, err := scanNode(row)
	if err != nil {
		return nil, err
	}
	// Derived projections (M2: intel reachability) settle on the same
	// transaction, under a savepoint that the hook's own failure rolls back.
	runConcludeHookPG(ctx, tx, scanID, id)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("graph: commit conclude: %w", err)
	}
	return out, nil
}

// checkPreconditionsTx locks and re-validates the contract preconditions.
func checkPreconditionsTx(ctx context.Context, tx pgx.Tx, scanID string, c Contract) error {
	for _, pre := range c.Preconditions {
		var state State
		err := tx.QueryRow(ctx, `
			SELECT state FROM exploration_nodes
			WHERE scan_id = $1 AND kind = $2 AND unit_key = $3
			FOR SHARE`,
			scanID, string(pre.RefKind), pre.Ref,
		).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s/%s is missing", ErrPreconditionUnmet, pre.RefKind, pre.Ref)
		}
		if err != nil {
			return err
		}
		if state != pre.NeedState {
			return fmt.Errorf("%w: %s/%s is %s, want %s", ErrPreconditionUnmet, pre.RefKind, pre.Ref, state, pre.NeedState)
		}
	}
	return nil
}

// SetBlockedReason records the operator-facing reason for a terminal intent.
// Despite the historical name and column it covers every terminal state the
// engine can explain (blocked, exhausted, stopped), because round 5 killed 79
// workers with "context canceled" and recorded no cause at all. It never
// changes state; live rows are left untouched.
func (s *PGStore) SetBlockedReason(ctx context.Context, scanID string, id int64, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE exploration_nodes
		SET blocked_reason = $3
		WHERE scan_id = $1 AND id = $2 AND kind = 'intent'
		  AND state IN ('blocked','exhausted','stopped')`,
		scanID, id, strings.TrimSpace(reason),
	)
	return err
}

// Counts aggregates the graph header counters.
func (s *PGStore) Counts(ctx context.Context, scanID string) (Counts, error) {
	var c Counts
	err := s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE kind='intent' AND state='open'),
			COUNT(*) FILTER (WHERE kind='intent' AND state='running'),
			COUNT(*) FILTER (WHERE kind='intent' AND state='paused'),
			COUNT(*) FILTER (WHERE kind='intent' AND state='done'),
			COUNT(*) FILTER (WHERE kind='intent' AND state IN ('blocked','exhausted','stopped')),
			COUNT(*) FILTER (WHERE kind='belief' AND state='queued'),
			COUNT(*) FILTER (WHERE kind='belief' AND state='testing'),
			COUNT(*) FILTER (WHERE kind='belief' AND state='proven'),
			COUNT(*) FILTER (WHERE kind='fact' AND state IN ('confirmed','origin')),
			COUNT(*) FILTER (WHERE kind='finding' AND state='confirmed'),
			COUNT(*) FILTER (WHERE kind='goal' AND state='open'),
			COUNT(*) FILTER (WHERE kind='goal' AND state='met')
		FROM exploration_nodes
		WHERE scan_id = $1`, scanID,
	).Scan(
		&c.OpenIntents, &c.RunningIntents, &c.PausedIntents, &c.DoneIntents,
		&c.BlockedIntents, &c.QueuedBeliefs, &c.TestingBeliefs, &c.ProvenBeliefs,
		&c.ConfirmedFacts, &c.ConfirmedFinding, &c.OpenGoals, &c.MetGoals,
	)
	return c, err
}

// PlanTasks reads the legacy PlanTask view.
func (s *PGStore) PlanTasks(ctx context.Context, scanID string) ([]PlanTask, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT node_id, task_id, instance_id, title, phase, vuln_class, endpoint, target,
		       status, graph_state, disposition, origin, depends_on, notes,
		       prerequisite, whole_target, objective_ref, owner, lease_until,
		       priority, created_at, updated_at
		FROM v_plan_tasks
		WHERE scan_id = $1 AND graph_state <> 'deleted'
		ORDER BY phase ASC, node_id ASC`, scanID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanTask
	for rows.Next() {
		var (
			t         PlanTask
			dependsOn []byte
		)
		if err := rows.Scan(
			&t.NodeID, &t.TaskID, &t.InstanceID, &t.Title, &t.Phase,
			&t.VulnClass, &t.Endpoint, &t.Target, &t.Status, &t.GraphState,
			&t.Disposition, &t.Origin, &dependsOn, &t.Notes,
			&t.Prerequisite, &t.WholeTarget, &t.ObjectiveRef,
			&t.Owner, &t.LeaseUntil, &t.Priority, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if len(dependsOn) > 0 {
			_ = json.Unmarshal(dependsOn, &t.DependsOn)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Units reads the unified intent+belief view.
func (s *PGStore) Units(ctx context.Context, scanID string) ([]Unit, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, node_id, unit_id, subject, state, owner, lease_until,
		       vuln_class, phase, expired
		FROM v_units
		WHERE scan_id = $1 AND state <> 'deleted'
		ORDER BY kind ASC, phase ASC, node_id ASC`, scanID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Unit
	for rows.Next() {
		var u Unit
		if err := rows.Scan(
			&u.Kind, &u.NodeID, &u.UnitID, &u.Subject, &u.State,
			&u.Owner, &u.LeaseUntil, &u.VulnClass, &u.Phase, &u.Expired,
		); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

const nodeColumns = `id, scan_id, instance_id, unit_key, kind, state, priority,
	origin, owner, lease_until, blocked_reason, delete_reason, payload,
	content_version, created_at, updated_at, completed_at`

// returningNodeColumns is the same projection with the UPDATE alias, used by
// the claim statement where both the target and the candidate CTE expose id.
const returningNodeColumns = `n.id, n.scan_id, n.instance_id, n.unit_key,
	n.kind, n.state, n.priority, n.origin, n.owner, n.lease_until,
	n.blocked_reason, n.delete_reason, n.payload, n.content_version,
	n.created_at, n.updated_at, n.completed_at`

func scanNode(row interface {
	Scan(dest ...any) error
}) (*Node, error) {
	var (
		n         Node
		lease     *time.Time
		completed *time.Time
	)
	if err := row.Scan(
		&n.ID, &n.ScanID, &n.InstanceID, &n.UnitKey, &n.Kind, &n.State,
		&n.Priority, &n.Origin, &n.Owner, &lease, &n.BlockedReason,
		&n.DeleteReason, &n.Payload, &n.ContentVersion, &n.CreatedAt,
		&n.UpdatedAt, &completed,
	); err != nil {
		return nil, err
	}
	n.LeaseUntil = lease
	n.CompletedAt = completed
	return &n, nil
}

func reclaimExpiredTx(ctx context.Context, tx pgx.Tx, scanID string) (int64, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE exploration_nodes
		SET state = CASE WHEN kind = 'intent' THEN 'open' ELSE 'queued' END,
		    owner = '',
		    lease_until = NULL
		WHERE scan_id = $1
		  AND lease_until IS NOT NULL
		  AND lease_until < now()
		  AND ((kind = 'intent' AND state = 'running')
		    OR (kind = 'belief' AND state = 'testing'))`,
		scanID,
	)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}
