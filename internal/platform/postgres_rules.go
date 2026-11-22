package platform

// Store approval-rules implementation ("approve & remember", REDESIGN §5.2,
// API_CONTRACT §3.2). created_from_approval_id rides ON DELETE SET NULL
// (0006_approval_rules.sql) so retention deleting old approval rows never
// silently deletes the operator's remembered exemption.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const approvalRuleColumns = `id, project_id, kind, decision, match, reason, created_by,
	COALESCE(created_from_approval_id::text, ''), created_at, expires_at, hit_count, last_hit_at`

func scanApprovalRuleRow(row pgx.Row) (*ApprovalRule, error) {
	var (
		r         ApprovalRule
		matchJSON []byte
	)
	if err := row.Scan(&r.ID, &r.ProjectID, &r.Kind, &r.Decision, &matchJSON, &r.Reason,
		&r.CreatedBy, &r.CreatedFromApprovalID, &r.CreatedAt, &r.ExpiresAt, &r.HitCount, &r.LastHitAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: scan approval rule: %w", err)
	}
	if len(matchJSON) > 0 {
		if err := json.Unmarshal(matchJSON, &r.Match); err != nil {
			return nil, fmt.Errorf("platform/postgres: decode approval rule match: %w", err)
		}
	}
	r.CreatedAt = r.CreatedAt.UTC()
	if r.ExpiresAt != nil {
		t := r.ExpiresAt.UTC()
		r.ExpiresAt = &t
	}
	if r.LastHitAt != nil {
		t := r.LastHitAt.UTC()
		r.LastHitAt = &t
	}
	return &r, nil
}

func scanApprovalRuleRows(rows pgx.Rows) ([]ApprovalRule, error) {
	defer rows.Close()
	out := []ApprovalRule{}
	for rows.Next() {
		var (
			r         ApprovalRule
			matchJSON []byte
		)
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Kind, &r.Decision, &matchJSON, &r.Reason,
			&r.CreatedBy, &r.CreatedFromApprovalID, &r.CreatedAt, &r.ExpiresAt, &r.HitCount, &r.LastHitAt); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan approval rule: %w", err)
		}
		if len(matchJSON) > 0 {
			if err := json.Unmarshal(matchJSON, &r.Match); err != nil {
				return nil, fmt.Errorf("platform/postgres: decode approval rule match: %w", err)
			}
		}
		r.CreatedAt = r.CreatedAt.UTC()
		if r.ExpiresAt != nil {
			t := r.ExpiresAt.UTC()
			r.ExpiresAt = &t
		}
		if r.LastHitAt != nil {
			t := r.LastHitAt.UTC()
			r.LastHitAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateApprovalRule implements Repository.CreateApprovalRule.
func (s *Store) CreateApprovalRule(ctx context.Context, in ApprovalRuleInput) (*ApprovalRule, error) {
	if err := validateApprovalRule(in); err != nil {
		return nil, err
	}
	projectArg, err := nullableUUIDArg(in.ProjectID)
	if err != nil {
		return nil, err
	}
	fromArg, err := nullableUUIDArg(in.CreatedFromApprovalID)
	if err != nil {
		return nil, err
	}
	matchJSON, err := marshalJSON(in.Match)
	if err != nil {
		return nil, err
	}
	var expiresArg any
	if in.ExpiresAt != nil {
		expiresArg = in.ExpiresAt.UTC()
	}
	id := uuid.NewString()
	if _, err := s.exec(ctx, `
		INSERT INTO approval_rules (id, project_id, kind, decision, match, reason, created_by, created_from_approval_id, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		id, projectArg, in.Kind, NormalizeApprovalRuleDecision(in.Decision), matchJSON, in.Reason, in.CreatedBy, fromArg, time.Now().UTC(), expiresArg); err != nil {
		return nil, fmt.Errorf("platform/postgres: create approval rule: %w", err)
	}
	return s.getApprovalRule(ctx, id)
}

// ListApprovalRules implements Repository.ListApprovalRules (newest first).
func (s *Store) ListApprovalRules(ctx context.Context, filter ApprovalRuleFilter) ([]ApprovalRule, error) {
	if filter.Kind != "" && filter.Kind != ApprovalKindCommand && filter.Kind != ApprovalKindHTTP {
		return nil, fmt.Errorf("%w: unknown approval rule kind filter %q", ErrValidation, filter.Kind)
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
	if filter.Kind != "" {
		where += fmt.Sprintf(` AND kind = $%d`, argN)
		args = append(args, filter.Kind)
	}
	rows, err := s.query(ctx,
		`SELECT `+approvalRuleColumns+` FROM approval_rules `+where+` ORDER BY created_at DESC, id ASC`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list approval rules: %w", err)
	}
	return scanApprovalRuleRows(rows)
}

// DeleteApprovalRule implements Repository.DeleteApprovalRule.
func (s *Store) DeleteApprovalRule(ctx context.Context, id string) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	tag, err := s.exec(ctx, `DELETE FROM approval_rules WHERE id = $1`, parsed)
	if err != nil {
		return fmt.Errorf("platform/postgres: delete approval rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordApprovalRuleHit implements Repository.RecordApprovalRuleHit. The
// UPDATE is intentionally unconditional: a missing rule (0 rows) is not an
// error — hit bookkeeping is a best-effort audit projection.
func (s *Store) RecordApprovalRuleHit(ctx context.Context, id string) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	if _, err := s.exec(ctx,
		`UPDATE approval_rules SET hit_count = hit_count + 1, last_hit_at = $2 WHERE id = $1`,
		parsed, time.Now().UTC()); err != nil {
		return fmt.Errorf("platform/postgres: record approval rule hit: %w", err)
	}
	return nil
}

func (s *Store) getApprovalRule(ctx context.Context, id string) (*ApprovalRule, error) {
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	return scanApprovalRuleRow(s.queryRow(ctx,
		`SELECT `+approvalRuleColumns+` FROM approval_rules WHERE id = $1`, parsed))
}
