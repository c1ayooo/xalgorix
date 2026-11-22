package platform

// Intel triage store — operator annotations on recorded intelligence (task
// detail 情报 tab). The observed intel ITEMS live in <scanDir>/intelligence.json
// (hot path, written by the record_intelligence tool) and are never mutated by
// triage; this store holds only the human label, keyed (task_id, fingerprint)
// so re-recorded observations across runs keep one decision. Field authority:
// engine observation and human annotation stay separate, conflict kept visible
// by overlaying at read time (charter §5).
//
// Dual implementation matches approval_rules (0006): PostgreSQL rows here,
// platform.json snapshot fields in the file store — one file, both backs.
// The optional IntelTriageStore interface is type-asserted by the web layer;
// a file-mode store without triage simply doesn't implement it and triage is
// unavailable while aggregation still works (charter §9: no-DSN scans run).

import (
	"context"
	"fmt"
	"time"
)

// Intel triage status vocabulary. Generic on purpose: one honest operator
// question per item ("is this real and actionable?"), not 4zreco's per-kind
// state machines.
const (
	IntelStatusOpen      = "open"
	IntelStatusConfirmed = "confirmed"
	IntelStatusDismissed = "dismissed"
)

// ValidIntelStatus gates external status values.
var ValidIntelStatus = map[string]bool{
	IntelStatusOpen:      true,
	IntelStatusConfirmed: true,
	IntelStatusDismissed: true,
}

// IntelTriage is one operator decision for one fingerprint within one task.
type IntelTriage struct {
	TaskID      string    `json:"task_id"`
	Fingerprint string    `json:"fingerprint"`
	Status      string    `json:"status"`
	DecidedBy   string    `json:"decided_by"`
	DecidedAt   time.Time `json:"decided_at"`
}

// IntelTriageStore is the optional triage seam. Implemented by both Store
// (PostgreSQL) and FileStore (platform.json); type-asserted by the web layer
// like FindingsStore.
type IntelTriageStore interface {
	// SaveIntelTriage upserts the triage decision for (taskID, fingerprint).
	// Re-triage overwrites the previous label (the decision record is the
	// operator's current answer, decided_at moves with it).
	SaveIntelTriage(ctx context.Context, taskID, fingerprint, status, decidedBy string) error
	// ListIntelTriage returns every triage decision of one task keyed by
	// fingerprint.
	ListIntelTriage(ctx context.Context, taskID string) (map[string]IntelTriage, error)
}

func validateIntelStatus(status string) error {
	if !ValidIntelStatus[status] {
		return fmt.Errorf("%w: intel triage status must be open|confirmed|dismissed, got %q", ErrValidation, status)
	}
	return nil
}

// SaveIntelTriage implements IntelTriageStore (file snapshot back).
func (s *FileStore) SaveIntelTriage(_ context.Context, taskID, fingerprint, status, decidedBy string) error {
	if err := validateIntelStatus(status); err != nil {
		return err
	}
	if taskID == "" || fingerprint == "" {
		return fmt.Errorf("%w: intel triage requires task id and fingerprint", ErrValidation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.taskLocked(taskID); !ok {
		return fmt.Errorf("%w: task %s not found", ErrNotFound, taskID)
	}
	now := time.Now().UTC()
	for i := range s.data.IntelTriage {
		if s.data.IntelTriage[i].TaskID == taskID && s.data.IntelTriage[i].Fingerprint == fingerprint {
			s.data.IntelTriage[i].Status = status
			s.data.IntelTriage[i].DecidedBy = decidedBy
			s.data.IntelTriage[i].DecidedAt = now
			return s.save()
		}
	}
	s.data.IntelTriage = append(s.data.IntelTriage, IntelTriage{
		TaskID:      taskID,
		Fingerprint: fingerprint,
		Status:      status,
		DecidedBy:   decidedBy,
		DecidedAt:   now,
	})
	return s.save()
}

// ListIntelTriage implements IntelTriageStore (file snapshot back).
func (s *FileStore) ListIntelTriage(_ context.Context, taskID string) (map[string]IntelTriage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]IntelTriage)
	for _, t := range s.data.IntelTriage {
		if t.TaskID == taskID {
			out[t.Fingerprint] = t
		}
	}
	return out, nil
}

// SaveIntelTriage implements IntelTriageStore (PostgreSQL back).
func (s *Store) SaveIntelTriage(ctx context.Context, taskID, fingerprint, status, decidedBy string) error {
	if err := validateIntelStatus(status); err != nil {
		return err
	}
	if taskID == "" || fingerprint == "" {
		return fmt.Errorf("%w: intel triage requires task id and fingerprint", ErrValidation)
	}
	parsed, err := parseUUID(taskID)
	if err != nil {
		return err
	}
	if decidedBy == "" {
		decidedBy = "operator"
	}
	if _, err := s.exec(ctx, `
		INSERT INTO intel_triage (task_id, fingerprint, status, decided_by, decided_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (task_id, fingerprint)
		DO UPDATE SET status = EXCLUDED.status, decided_by = EXCLUDED.decided_by, decided_at = EXCLUDED.decided_at`,
		parsed, fingerprint, status, decidedBy, time.Now().UTC()); err != nil {
		return fmt.Errorf("platform/postgres: save intel triage: %w", err)
	}
	return nil
}

// ListIntelTriage implements IntelTriageStore (PostgreSQL back).
func (s *Store) ListIntelTriage(ctx context.Context, taskID string) (map[string]IntelTriage, error) {
	parsed, err := parseUUID(taskID)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `
		SELECT task_id, fingerprint, status, decided_by, decided_at
		FROM intel_triage WHERE task_id = $1`, parsed)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list intel triage: %w", err)
	}
	defer rows.Close()
	out := make(map[string]IntelTriage)
	for rows.Next() {
		var t IntelTriage
		if err := rows.Scan(&t.TaskID, &t.Fingerprint, &t.Status, &t.DecidedBy, &t.DecidedAt); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan intel triage: %w", err)
		}
		t.TaskID = taskID
		t.DecidedAt = t.DecidedAt.UTC()
		out[t.Fingerprint] = t
	}
	return out, rows.Err()
}
