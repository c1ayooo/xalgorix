package platform

// Store approvals + guidance implementation (R2a HITL, REDESIGN §5). The PG
// store owns the cold-path approval records; the gate service (web layer)
// drives their lifecycle. Optimistic concurrency on DecideApproval: the
// UPDATE carries a status='pending' guard so the first of two racing
// decisions wins and the loser reads 0 rows → ErrConflict (409).

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

// nullableUUIDArg converts an optional row reference into an insert argument:
// "" → SQL NULL, anything else parsed as UUID (parseUUID maps garbage to
// ErrNotFound which the caller surfaces as 404, not a driver cast error).
func nullableUUIDArg(id string) (any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("%w: bad id %q", ErrNotFound, id)
	}
	return parsed, nil
}

// approvals scan/select plumbing ─────────────────────────────────────────────

const approvalColumns = `id, project_id,
	COALESCE(task_id::text, ''), COALESCE(run_id::text, ''), instance_id,
	kind, payload, slice, status, deadline_at, decided_at, decided_by, reason, created_at`

func scanApprovalRow(row pgx.Row) (*Approval, error) {
	var (
		a           Approval
		payloadJSON []byte
		sliceJSON   []byte
	)
	if err := row.Scan(&a.ID, &a.ProjectID, &a.TaskID, &a.RunID, &a.InstanceID,
		&a.Kind, &payloadJSON, &sliceJSON, &a.Status, &a.DeadlineAt, &a.DecidedAt, &a.DecidedBy, &a.Reason, &a.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: scan approval: %w", err)
	}
	if len(payloadJSON) > 0 {
		if err := json.Unmarshal(payloadJSON, &a.Payload); err != nil {
			return nil, fmt.Errorf("platform/postgres: decode approval payload: %w", err)
		}
	}
	if len(sliceJSON) > 0 {
		slice := ApprovalSlice{}
		if err := json.Unmarshal(sliceJSON, &slice); err != nil {
			return nil, fmt.Errorf("platform/postgres: decode approval slice: %w", err)
		}
		a.Slice = &slice
	}
	a.CreatedAt = a.CreatedAt.UTC()
	a.DeadlineAt = a.DeadlineAt.UTC()
	if a.DecidedAt != nil {
		t := a.DecidedAt.UTC()
		a.DecidedAt = &t
	}
	return &a, nil
}

func scanApprovalRows(rows pgx.Rows) ([]Approval, error) {
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		var (
			a           Approval
			payloadJSON []byte
			sliceJSON   []byte
		)
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.TaskID, &a.RunID, &a.InstanceID,
			&a.Kind, &payloadJSON, &sliceJSON, &a.Status, &a.DeadlineAt, &a.DecidedAt, &a.DecidedBy, &a.Reason, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan approval: %w", err)
		}
		if len(payloadJSON) > 0 {
			if err := json.Unmarshal(payloadJSON, &a.Payload); err != nil {
				return nil, fmt.Errorf("platform/postgres: decode approval payload: %w", err)
			}
		}
		if len(sliceJSON) > 0 {
			slice := ApprovalSlice{}
			if err := json.Unmarshal(sliceJSON, &slice); err != nil {
				return nil, fmt.Errorf("platform/postgres: decode approval slice: %w", err)
			}
			a.Slice = &slice
		}
		a.CreatedAt = a.CreatedAt.UTC()
		a.DeadlineAt = a.DeadlineAt.UTC()
		if a.DecidedAt != nil {
			t := a.DecidedAt.UTC()
			a.DecidedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateApproval implements Repository.CreateApproval.
func (s *Store) CreateApproval(ctx context.Context, in ApprovalInput) (*Approval, error) {
	if in.Kind != ApprovalKindCommand && in.Kind != ApprovalKindHTTP {
		return nil, fmt.Errorf("%w: approval kind must be command or http", ErrValidation)
	}
	projectArg, err := nullableUUIDArg(in.ProjectID)
	if err != nil {
		return nil, err
	}
	taskArg, err := nullableUUIDArg(in.TaskID)
	if err != nil {
		return nil, err
	}
	runArg, err := nullableUUIDArg(in.RunID)
	if err != nil {
		return nil, err
	}
	payloadJSON, err := marshalJSON(in.Payload)
	if err != nil {
		return nil, err
	}
	var sliceJSON []byte
	if in.Slice != nil {
		if sliceJSON, err = marshalJSON(in.Slice); err != nil {
			return nil, err
		}
	}
	id := uuid.NewString()
	if _, err := s.exec(ctx, `
		INSERT INTO approvals (id, project_id, task_id, run_id, instance_id, kind, payload, slice, status, deadline_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		id, projectArg, taskArg, runArg, in.InstanceID, in.Kind, payloadJSON, sliceJSON,
		ApprovalPending, in.DeadlineAt.UTC(), time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("platform/postgres: create approval: %w", err)
	}
	return s.GetApproval(ctx, id)
}

// ListApprovals implements Repository.ListApprovals.
func (s *Store) ListApprovals(ctx context.Context, filter ApprovalFilter) ([]Approval, error) {
	switch filter.Status {
	case "", "all", "pending", "decided":
	default:
		return nil, fmt.Errorf("%w: unknown approval status filter %q", ErrValidation, filter.Status)
	}
	where := "WHERE TRUE"
	args := []any{}
	argN := 1
	if filter.ProjectID != "" {
		parsed, err := uuid.Parse(filter.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("%w: bad project_id %q", ErrNotFound, filter.ProjectID)
		}
		where += fmt.Sprintf(` AND project_id = $%d`, argN)
		args = append(args, parsed)
		argN++
	}
	if filter.TaskID != "" {
		parsed, err := uuid.Parse(filter.TaskID)
		if err != nil {
			return nil, fmt.Errorf("%w: bad task_id %q", ErrNotFound, filter.TaskID)
		}
		where += fmt.Sprintf(` AND task_id = $%d`, argN)
		args = append(args, parsed)
		argN++
	}
	switch filter.Status {
	case "pending":
		where += ` AND status = 'pending'`
	case "decided":
		where += ` AND status <> 'pending'`
	}
	page, size := normalizePage(filter.Page, filter.Size)
	rows, err := s.query(ctx, `
		SELECT `+approvalColumns+` FROM approvals `+where+`
		ORDER BY created_at DESC, id ASC LIMIT $`+fmt.Sprint(argN)+` OFFSET $`+fmt.Sprint(argN+1),
		append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list approvals: %w", err)
	}
	return scanApprovalRows(rows)
}

// GetApproval implements Repository.GetApproval.
func (s *Store) GetApproval(ctx context.Context, id string) (*Approval, error) {
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	return scanApprovalRow(s.queryRow(ctx,
		`SELECT `+approvalColumns+` FROM approvals WHERE id = $1`, parsed))
}

// DecideApproval implements Repository.DecideApproval. The status='pending'
// guard IS the optimistic lock: two racing decisions (operator vs the
// deadline sweep) cannot both win; the loser gets ErrConflict → 409.
func (s *Store) DecideApproval(ctx context.Context, id string, decision ApprovalDecision) (*Approval, error) {
	switch decision.Decision {
	case ApprovalApproved, ApprovalRejected, ApprovalTimeout, ApprovalCancelled:
	default:
		return nil, fmt.Errorf("%w: decision must be approved, rejected, timeout or cancelled", ErrValidation)
	}
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tag, err := s.exec(ctx, `
		UPDATE approvals
		SET status = $2, decided_at = $3, decided_by = $4, reason = $5
		WHERE id = $1 AND status = 'pending'`,
		parsed, decision.Decision, now, decision.DecidedBy, decision.Reason)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: decide approval: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Distinguish "never existed" from "no longer pending".
		var status string
		err := s.queryRow(ctx, `SELECT status FROM approvals WHERE id = $1`, parsed).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("platform/postgres: decide approval lookup: %w", err)
		}
		return nil, fmt.Errorf("%w: approval %s is %s, not pending", ErrConflict, id, status)
	}
	return s.GetApproval(ctx, id)
}

// CountPendingByInstance implements Repository.CountPendingByInstance.
func (s *Store) CountPendingByInstance(ctx context.Context, instanceID string) (int, error) {
	var n int
	if err := s.queryRow(ctx,
		`SELECT count(*) FROM approvals WHERE instance_id = $1 AND status = 'pending'`, instanceID).Scan(&n); err != nil {
		return 0, fmt.Errorf("platform/postgres: count pending approvals: %w", err)
	}
	return n, nil
}

// CancelPendingByInstance implements Repository.CancelPendingByInstance.
func (s *Store) CancelPendingByInstance(ctx context.Context, instanceID, reason string) ([]Approval, error) {
	rows, err := s.query(ctx, `
		UPDATE approvals
		SET status = 'cancelled', decided_at = $2, decided_by = 'system', reason = $3
		WHERE instance_id = $1 AND status = 'pending'
		RETURNING `+approvalColumns, instanceID, time.Now().UTC(), reason)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: cancel pending approvals: %w", err)
	}
	return scanApprovalRows(rows)
}

// PendingSummary implements Repository.PendingSummary (cross-project — the
// documented D7 exception powering the OpsPulse pending counter).
func (s *Store) PendingSummary(ctx context.Context) (ApprovalSummary, error) {
	summary := ApprovalSummary{}
	if err := s.queryRow(ctx,
		`SELECT count(*) FROM approvals WHERE status = 'pending'`).Scan(&summary.Total); err != nil {
		return summary, fmt.Errorf("platform/postgres: pending summary count: %w", err)
	}
	if summary.Total == 0 {
		return summary, nil
	}
	var nearest *time.Time
	if err := s.queryRow(ctx,
		`SELECT min(deadline_at) FROM approvals WHERE status = 'pending'`).Scan(&nearest); err != nil {
		return summary, fmt.Errorf("platform/postgres: pending summary deadline: %w", err)
	}
	if nearest != nil {
		t := nearest.UTC()
		summary.NearestDeadline = &t
	}
	return summary, nil
}

// ── guidance ─────────────────────────────────────────────────────────────────

const guidanceColumns = `id, task_id, COALESCE(run_id::text, ''), instance_id, message, status, request_id, created_by, created_at, consumed_at`

func scanGuidanceRow(row pgx.Row) (*Guidance, error) {
	var g Guidance
	if err := row.Scan(&g.ID, &g.TaskID, &g.RunID, &g.InstanceID, &g.Message, &g.Status, &g.RequestID, &g.CreatedBy, &g.CreatedAt, &g.ConsumedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: scan guidance: %w", err)
	}
	g.CreatedAt = g.CreatedAt.UTC()
	if g.ConsumedAt != nil {
		t := g.ConsumedAt.UTC()
		g.ConsumedAt = &t
	}
	return &g, nil
}

func scanGuidanceRows(rows pgx.Rows) ([]Guidance, error) {
	defer rows.Close()
	out := []Guidance{}
	for rows.Next() {
		var g Guidance
		if err := rows.Scan(&g.ID, &g.TaskID, &g.RunID, &g.InstanceID, &g.Message, &g.Status, &g.RequestID, &g.CreatedBy, &g.CreatedAt, &g.ConsumedAt); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan guidance: %w", err)
		}
		g.CreatedAt = g.CreatedAt.UTC()
		if g.ConsumedAt != nil {
			t := g.ConsumedAt.UTC()
			g.ConsumedAt = &t
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CreateGuidance implements Repository.CreateGuidance. Idempotency rides the
// partial unique index uq_guidance_request_id: ON CONFLICT DO NOTHING + re-read
// (same first-start race pattern as EnsureImportTask).
func (s *Store) CreateGuidance(ctx context.Context, in GuidanceInput) (*Guidance, bool, error) {
	if strings.TrimSpace(in.Message) == "" {
		return nil, false, fmt.Errorf("%w: guidance message is required", ErrValidation)
	}
	taskArg, err := nullableUUIDArg(in.TaskID)
	if err != nil {
		return nil, false, err
	}
	runArg, err := nullableUUIDArg(in.RunID)
	if err != nil {
		return nil, false, err
	}
	if in.RequestID != "" {
		if existing, err := s.guidanceByRequestID(ctx, in.TaskID, in.RequestID); err == nil {
			return existing, false, nil
		} else if !errors.Is(err, ErrNotFound) {
			return nil, false, err
		}
	}
	id := uuid.NewString()
	tag, err := s.exec(ctx, `
		INSERT INTO guidance (id, task_id, run_id, instance_id, message, status, request_id, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING`,
		id, taskArg, runArg, in.InstanceID, in.Message, GuidanceQueued, in.RequestID, in.CreatedBy, time.Now().UTC())
	if err != nil {
		// A concurrent creator won the partial unique index before the
		// ON CONFLICT clause could infer it (or an unexpected constraint
		// fired) — resolve by re-read when it is the request-id key.
		if in.RequestID != "" {
			if existing, selErr := s.guidanceByRequestID(ctx, in.TaskID, in.RequestID); selErr == nil {
				return existing, false, nil
			}
		}
		return nil, false, fmt.Errorf("platform/postgres: create guidance: %w", err)
	}
	if tag.RowsAffected() == 0 && in.RequestID != "" {
		existing, selErr := s.guidanceByRequestID(ctx, in.TaskID, in.RequestID)
		if selErr != nil {
			return nil, false, selErr
		}
		return existing, false, nil
	}
	created, err := s.getGuidance(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

func (s *Store) guidanceByRequestID(ctx context.Context, taskID, requestID string) (*Guidance, error) {
	parsed, err := parseUUID(taskID)
	if err != nil {
		return nil, err
	}
	return scanGuidanceRow(s.queryRow(ctx,
		`SELECT `+guidanceColumns+` FROM guidance WHERE task_id = $1 AND request_id = $2 ORDER BY created_at, id LIMIT 1`,
		parsed, requestID))
}

func (s *Store) getGuidance(ctx context.Context, id string) (*Guidance, error) {
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	return scanGuidanceRow(s.queryRow(ctx,
		`SELECT `+guidanceColumns+` FROM guidance WHERE id = $1`, parsed))
}

// ListGuidance implements Repository.ListGuidance (newest first).
func (s *Store) ListGuidance(ctx context.Context, taskID string, limit int) ([]Guidance, error) {
	parsed, err := parseUUID(taskID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.query(ctx,
		`SELECT `+guidanceColumns+` FROM guidance WHERE task_id = $1 ORDER BY created_at DESC, id ASC LIMIT $2`,
		parsed, limit)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list guidance: %w", err)
	}
	return scanGuidanceRows(rows)
}

// UpdateGuidanceStatus implements Repository.UpdateGuidanceStatus.
func (s *Store) UpdateGuidanceStatus(ctx context.Context, id, status string) (*Guidance, error) {
	switch status {
	case GuidanceInjected, GuidanceQueued, GuidanceConsumed:
	default:
		return nil, fmt.Errorf("%w: guidance status must be injected, queued or consumed", ErrValidation)
	}
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	var consumedAt any
	if status == GuidanceConsumed {
		consumedAt = time.Now().UTC()
	}
	tag, err := s.exec(ctx, `
		UPDATE guidance SET status = $2, consumed_at = $3 WHERE id = $1`,
		parsed, status, consumedAt)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: update guidance status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.getGuidance(ctx, id)
}
