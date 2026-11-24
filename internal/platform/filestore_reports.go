package platform

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CreateReport implements ReportStore on the file snapshot.
func (s *FileStore) CreateReport(_ context.Context, mount ReportMount) (*Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.taskLocked(mount.TaskID)
	if !ok {
		return nil, fmt.Errorf("%w: task %s not found", ErrNotFound, mount.TaskID)
	}
	revision := 1
	for _, r := range s.data.Reports {
		if r.TaskID != mount.TaskID {
			continue
		}
		if r.Revision >= revision {
			revision = r.Revision + 1
		}
		if r.Status == ReportQueued || r.Status == ReportGenerating {
			return nil, fmt.Errorf("%w: task %s already has an active report", ErrConflict, mount.TaskID)
		}
	}
	report := Report{
		ID:            uuid.NewString(),
		ProjectID:     task.ProjectID,
		TaskID:        mount.TaskID,
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
		CreatedAt:     time.Now().UTC(),
	}
	s.data.Reports = append(s.data.Reports, report)
	if err := s.save(); err != nil {
		return nil, err
	}
	out := cloneReport(report)
	return &out, nil
}

// UpdateReportStatus implements ReportStore.
func (s *FileStore) UpdateReportStatus(_ context.Context, reportID, status, errMsg string) error {
	if !validReportStatus(status) {
		return fmt.Errorf("%w: unknown report status %q", ErrValidation, status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Reports {
		if s.data.Reports[i].ID != reportID {
			continue
		}
		now := time.Now().UTC()
		s.data.Reports[i].Status = status
		s.data.Reports[i].Error = errMsg
		if status == ReportGenerating && s.data.Reports[i].StartedAt == nil {
			s.data.Reports[i].StartedAt = &now
		}
		switch status {
		case ReportReady, ReportFailed, ReportCancelled:
			s.data.Reports[i].FinishedAt = &now
		}
		return s.save()
	}
	return fmt.Errorf("%w: report %s not found", ErrNotFound, reportID)
}

// GetReport implements ReportStore.
func (s *FileStore) GetReport(_ context.Context, reportID string) (*Report, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.data.Reports {
		if r.ID == reportID {
			out := cloneReport(r)
			return &out, nil
		}
	}
	return nil, fmt.Errorf("%w: report %s not found", ErrNotFound, reportID)
}

// ListReportsByTask implements ReportStore.
func (s *FileStore) ListReportsByTask(_ context.Context, taskID string) ([]Report, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Report{}
	for _, r := range s.data.Reports {
		if r.TaskID == taskID {
			out = append(out, cloneReport(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision > out[j].Revision })
	return out, nil
}

// PutReportArtifacts implements ReportStore.
func (s *FileStore) PutReportArtifacts(_ context.Context, reportID string, artifacts []ReportArtifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, r := range s.data.Reports {
		if r.ID == reportID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: report %s not found", ErrNotFound, reportID)
	}
	for _, a := range artifacts {
		if strings.TrimSpace(a.Kind) == "" || strings.TrimSpace(a.Format) == "" || strings.TrimSpace(a.Path) == "" {
			return fmt.Errorf("%w: report artifact kind/format/path are required", ErrValidation)
		}
		a.ReportID = reportID
		replaced := false
		for i := range s.data.ReportArtifacts {
			existing := s.data.ReportArtifacts[i]
			if existing.ReportID == reportID && existing.Kind == a.Kind && existing.ScanID == a.ScanID && existing.Format == a.Format {
				s.data.ReportArtifacts[i] = a
				replaced = true
				break
			}
		}
		if !replaced {
			s.data.ReportArtifacts = append(s.data.ReportArtifacts, a)
		}
	}
	return s.save()
}

// ListReportArtifacts implements ReportStore.
func (s *FileStore) ListReportArtifacts(_ context.Context, reportID string) ([]ReportArtifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []ReportArtifact{}
	for _, a := range s.data.ReportArtifacts {
		if a.ReportID == reportID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].ScanID != out[j].ScanID {
			return out[i].ScanID < out[j].ScanID
		}
		return out[i].Format < out[j].Format
	})
	return out, nil
}

// DeleteReport implements ReportStore.
func (s *FileStore) DeleteReport(_ context.Context, reportID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	reports := s.data.Reports[:0]
	for _, r := range s.data.Reports {
		if r.ID == reportID {
			found = true
			continue
		}
		reports = append(reports, r)
	}
	if !found {
		return fmt.Errorf("%w: report %s not found", ErrNotFound, reportID)
	}
	s.data.Reports = reports
	artifacts := s.data.ReportArtifacts[:0]
	for _, a := range s.data.ReportArtifacts {
		if a.ReportID == reportID {
			continue
		}
		artifacts = append(artifacts, a)
	}
	s.data.ReportArtifacts = artifacts
	return s.save()
}

func cloneReport(r Report) Report {
	r.RunIDs = append([]string(nil), r.RunIDs...)
	r.ScanIDs = append([]string(nil), r.ScanIDs...)
	r.Formats = append([]string(nil), r.Formats...)
	return r
}

// ListReports implements ReportLister.
func (s *FileStore) ListReports(_ context.Context, limit int) ([]Report, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Report, 0, len(s.data.Reports))
	for _, r := range s.data.Reports {
		out = append(out, cloneReport(r))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].Revision > out[j].Revision
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ReconcileActiveReports implements ReportReconciler.
func (s *FileStore) ReconcileActiveReports(_ context.Context, reason string) (int, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "interrupted by process restart"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	count := 0
	for i := range s.data.Reports {
		switch s.data.Reports[i].Status {
		case ReportQueued, ReportGenerating:
			s.data.Reports[i].Status = ReportFailed
			s.data.Reports[i].Error = reason
			s.data.Reports[i].FinishedAt = &now
			count++
		}
	}
	if count == 0 {
		return 0, nil
	}
	if err := s.save(); err != nil {
		return count, err
	}
	return count, nil
}
