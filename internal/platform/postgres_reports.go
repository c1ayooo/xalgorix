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

// CreateReport implements ReportStore. Revision allocation and the active-row
// guard run in one transaction: the task row is locked so two concurrent
// generation requests cannot pick the same revision, and the partial unique
// index (uq_reports_active_task) rejects a second queued/generating row.
func (s *Store) CreateReport(ctx context.Context, mount ReportMount) (*Report, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("platform/postgres: store unavailable")
	}
	taskUUID, err := parseUUID(mount.TaskID)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: begin report create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var projectUUID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1 FOR UPDATE`, taskUUID).Scan(&projectUUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: lock task for report: %w", err)
	}
	var revision int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM reports WHERE task_id = $1`, taskUUID).Scan(&revision); err != nil {
		return nil, fmt.Errorf("platform/postgres: next report revision: %w", err)
	}
	runIDs, err := jsonStringList(mount.RunIDs)
	if err != nil {
		return nil, err
	}
	scanIDs, err := jsonStringList(mount.ScanIDs)
	if err != nil {
		return nil, err
	}
	formats, err := jsonStringList(mount.Formats)
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	now := time.Now().UTC()
	report := &Report{
		ID:            id.String(),
		ProjectID:     projectUUID.String(),
		TaskID:        taskUUID.String(),
		Revision:      revision,
		RunIDs:        append([]string(nil), mount.RunIDs...),
		ScanIDs:       append([]string(nil), mount.ScanIDs...),
		Status:        ReportQueued,
		Template:      defaultString(mount.Template, "standard"),
		Language:      defaultString(mount.Language, "zh-CN"),
		Mode:          defaultString(mount.Mode, "template"),
		Model:         mount.Model,
		PromptVersion: mount.PromptVersion,
		Formats:       append([]string(nil), mount.Formats...),
		CreatedAt:     now,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO reports (id, project_id, task_id, revision, run_ids, scan_ids,
		                     status, template, language, mode, model, prompt_version,
		                     formats, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		id, projectUUID, taskUUID, revision, runIDs, scanIDs,
		report.Status, report.Template, report.Language, report.Mode,
		report.Model, report.PromptVersion, formats, now); err != nil {
		return nil, mapConstraintErr(err, "create report")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("platform/postgres: commit report create: %w", err)
	}
	return report, nil
}

// UpdateReportStatus implements ReportStore.
func (s *Store) UpdateReportStatus(ctx context.Context, reportID, status, errMsg string) error {
	parsed, err := parseUUID(reportID)
	if err != nil {
		return err
	}
	if !validReportStatus(status) {
		return fmt.Errorf("%w: unknown report status %q", ErrValidation, status)
	}
	tag, err := s.exec(ctx, `
		UPDATE reports
		SET status = $2,
		    error = $3,
		    started_at = CASE WHEN $2 = 'generating' AND started_at IS NULL THEN now() ELSE started_at END,
		    finished_at = CASE WHEN $2 IN ('ready','failed','cancelled') THEN now() ELSE finished_at END
		WHERE id = $1`, parsed, status, errMsg)
	if err != nil {
		return fmt.Errorf("platform/postgres: update report status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetReport implements ReportStore.
func (s *Store) GetReport(ctx context.Context, reportID string) (*Report, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("platform/postgres: store unavailable")
	}
	parsed, err := parseUUID(reportID)
	if err != nil {
		return nil, err
	}
	row := s.queryRow(ctx, reportSelect+` WHERE id = $1`, parsed)
	report, err := scanReport(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: get report: %w", err)
	}
	return report, nil
}

// ListReportsByTask implements ReportStore.
func (s *Store) ListReportsByTask(ctx context.Context, taskID string) ([]Report, error) {
	parsed, err := parseUUID(taskID)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, reportSelect+` WHERE task_id = $1 ORDER BY revision DESC`, parsed)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list reports: %w", err)
	}
	defer rows.Close()
	out := []Report{}
	for rows.Next() {
		report, err := scanReport(rows)
		if err != nil {
			return nil, fmt.Errorf("platform/postgres: scan report row: %w", err)
		}
		out = append(out, *report)
	}
	return out, rows.Err()
}

// PutReportArtifacts implements ReportStore.
func (s *Store) PutReportArtifacts(ctx context.Context, reportID string, artifacts []ReportArtifact) error {
	parsed, err := parseUUID(reportID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("platform/postgres: begin report artifacts: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, a := range artifacts {
		if strings.TrimSpace(a.Kind) == "" || strings.TrimSpace(a.Format) == "" || strings.TrimSpace(a.Path) == "" {
			return fmt.Errorf("%w: report artifact kind/format/path are required", ErrValidation)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO report_artifacts (report_id, kind, scan_id, target, format, path, bytes, sha256)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (report_id, kind, scan_id, format) DO UPDATE
			SET target = EXCLUDED.target,
			    path = EXCLUDED.path,
			    bytes = EXCLUDED.bytes,
			    sha256 = EXCLUDED.sha256`,
			parsed, a.Kind, a.ScanID, a.Target, a.Format, a.Path, a.Bytes, a.SHA256); err != nil {
			return fmt.Errorf("platform/postgres: put report artifact: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("platform/postgres: commit report artifacts: %w", err)
	}
	return nil
}

// ListReportArtifacts implements ReportStore.
func (s *Store) ListReportArtifacts(ctx context.Context, reportID string) ([]ReportArtifact, error) {
	parsed, err := parseUUID(reportID)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `
		SELECT report_id, kind, scan_id, target, format, path, bytes, sha256
		FROM report_artifacts
		WHERE report_id = $1
		ORDER BY kind, scan_id, format`, parsed)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list report artifacts: %w", err)
	}
	defer rows.Close()
	out := []ReportArtifact{}
	for rows.Next() {
		var a ReportArtifact
		var id uuid.UUID
		if err := rows.Scan(&id, &a.Kind, &a.ScanID, &a.Target, &a.Format, &a.Path, &a.Bytes, &a.SHA256); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan report artifact: %w", err)
		}
		a.ReportID = id.String()
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteReport implements ReportStore.
func (s *Store) DeleteReport(ctx context.Context, reportID string) error {
	parsed, err := parseUUID(reportID)
	if err != nil {
		return err
	}
	tag, err := s.exec(ctx, `DELETE FROM reports WHERE id = $1`, parsed)
	if err != nil {
		return fmt.Errorf("platform/postgres: delete report: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const reportSelect = `SELECT id, COALESCE(project_id::text, ''), task_id, revision,
	run_ids, scan_ids, status, template, language, mode, model, prompt_version,
	formats, error, created_at, started_at, finished_at FROM reports`

func scanReport(row pgx.Row) (*Report, error) {
	var (
		r        Report
		id       uuid.UUID
		taskID   uuid.UUID
		runJSON  []byte
		scanJSON []byte
		fmtJSON  []byte
	)
	if err := row.Scan(&id, &r.ProjectID, &taskID, &r.Revision, &runJSON, &scanJSON,
		&r.Status, &r.Template, &r.Language, &r.Mode, &r.Model, &r.PromptVersion,
		&fmtJSON, &r.Error, &r.CreatedAt, &r.StartedAt, &r.FinishedAt); err != nil {
		return nil, err
	}
	r.ID = id.String()
	r.TaskID = taskID.String()
	r.CreatedAt = r.CreatedAt.UTC()
	_ = json.Unmarshal(runJSON, &r.RunIDs)
	_ = json.Unmarshal(scanJSON, &r.ScanIDs)
	_ = json.Unmarshal(fmtJSON, &r.Formats)
	return &r, nil
}

func jsonStringList(values []string) ([]byte, error) {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("%w: report list field: %w", ErrValidation, err)
	}
	return data, nil
}

func validReportStatus(status string) bool {
	switch status {
	case ReportQueued, ReportGenerating, ReportReady, ReportFailed, ReportCancelled:
		return true
	default:
		return false
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// ListReports implements ReportLister.
func (s *Store) ListReports(ctx context.Context, limit int) ([]Report, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("platform/postgres: store unavailable")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.query(ctx, reportSelect+` ORDER BY created_at DESC, revision DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list reports: %w", err)
	}
	defer rows.Close()
	out := []Report{}
	for rows.Next() {
		report, err := scanReport(rows)
		if err != nil {
			return nil, fmt.Errorf("platform/postgres: scan report row: %w", err)
		}
		out = append(out, *report)
	}
	return out, rows.Err()
}

// ReconcileActiveReports implements ReportReconciler.
func (s *Store) ReconcileActiveReports(ctx context.Context, reason string) (int, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("platform/postgres: store unavailable")
	}
	if strings.TrimSpace(reason) == "" {
		reason = "interrupted by process restart"
	}
	tag, err := s.exec(ctx, `
		UPDATE reports
		SET status = 'failed', error = $1, finished_at = now()
		WHERE status IN ('queued','generating')`, reason)
	if err != nil {
		return 0, fmt.Errorf("platform/postgres: reconcile reports: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
