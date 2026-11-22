package platform

// FileStore approvals + guidance implementation (R2a HITL, REDESIGN §5,
// API_CONTRACT §3/§3.1). Same posture as the project/task/run subset: one
// full JSON snapshot under <dataDir>/_platform/platform.json written with
// storage.WriteAtomic behind the store RWMutex. The cross-project operator
// view (empty ProjectID filter) is the documented D7 exception for the
// approval queue.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CreateApproval implements Repository.CreateApproval.
func (s *FileStore) CreateApproval(_ context.Context, in ApprovalInput) (*Approval, error) {
	if in.Kind != ApprovalKindCommand && in.Kind != ApprovalKindHTTP {
		return nil, fmt.Errorf("%w: approval kind must be command or http", ErrValidation)
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		return nil, fmt.Errorf("%w: approval project_id is required", ErrValidation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projectLocked(in.ProjectID); !ok {
		return nil, fmt.Errorf("%w: project %s not found", ErrNotFound, in.ProjectID)
	}
	now := time.Now().UTC()
	a := Approval{
		ID:         uuid.NewString(),
		ProjectID:  in.ProjectID,
		TaskID:     in.TaskID,
		RunID:      in.RunID,
		InstanceID: in.InstanceID,
		Kind:       in.Kind,
		Payload:    in.Payload,
		Slice:      in.Slice,
		Status:     ApprovalPending,
		CreatedAt:  now,
		DeadlineAt: in.DeadlineAt.UTC(),
	}
	s.data.Approvals = append(s.data.Approvals, approvalToWire(a))
	if err := s.save(); err != nil {
		return nil, err
	}
	return &a, nil
}

// ListApprovals implements Repository.ListApprovals (newest first, paginated).
func (s *FileStore) ListApprovals(_ context.Context, filter ApprovalFilter) ([]Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []approvalWire
	for _, w := range s.data.Approvals {
		if filter.ProjectID != "" && w.ProjectID != filter.ProjectID {
			continue
		}
		if filter.TaskID != "" && w.TaskID != filter.TaskID {
			continue
		}
		switch filter.Status {
		case "", "all":
		case "pending":
			if w.Status != ApprovalPending {
				continue
			}
		case "decided":
			if w.Status == ApprovalPending {
				continue
			}
		default:
			return nil, fmt.Errorf("%w: unknown approval status filter %q", ErrValidation, filter.Status)
		}
		matched = append(matched, w)
	}
	sortApprovalsNewestFirst(matched)
	page, size := normalizePage(filter.Page, filter.Size)
	total := len(matched)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	out := make([]Approval, 0, end-start)
	for _, w := range matched[start:end] {
		out = append(out, approvalFromWire(w))
	}
	return out, nil
}

// GetApproval implements Repository.GetApproval.
func (s *FileStore) GetApproval(_ context.Context, id string) (*Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, w := range s.data.Approvals {
		if w.ID == id {
			a := approvalFromWire(w)
			return &a, nil
		}
	}
	return nil, ErrNotFound
}

// DecideApproval implements Repository.DecideApproval. Optimistic locking:
// only a pending row transitions; anything else is ErrConflict so the second
// of two racing decisions (operator vs timeout sweep) fails instead of
// overwriting the first verdict.
func (s *FileStore) DecideApproval(_ context.Context, id string, decision ApprovalDecision) (*Approval, error) {
	switch decision.Decision {
	case ApprovalApproved, ApprovalRejected, ApprovalTimeout, ApprovalCancelled:
	default:
		return nil, fmt.Errorf("%w: decision must be approved, rejected, timeout or cancelled", ErrValidation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Approvals {
		w := &s.data.Approvals[i]
		if w.ID != id {
			continue
		}
		if w.Status != ApprovalPending {
			return nil, fmt.Errorf("%w: approval %s is %s, not pending", ErrConflict, id, w.Status)
		}
		now := time.Now().UTC()
		w.Status = decision.Decision
		w.DecidedAt = &now
		w.DecidedBy = decision.DecidedBy
		if decision.Reason != "" {
			w.Reason = decision.Reason
		}
		if err := s.save(); err != nil {
			return nil, err
		}
		a := approvalFromWire(*w)
		return &a, nil
	}
	return nil, ErrNotFound
}

// CountPendingByInstance implements Repository.CountPendingByInstance.
func (s *FileStore) CountPendingByInstance(_ context.Context, instanceID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, w := range s.data.Approvals {
		if w.InstanceID == instanceID && w.Status == ApprovalPending {
			n++
		}
	}
	return n, nil
}

// CancelPendingByInstance implements Repository.CancelPendingByInstance.
func (s *FileStore) CancelPendingByInstance(_ context.Context, instanceID, reason string) ([]Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Approval
	now := time.Now().UTC()
	for i := range s.data.Approvals {
		w := &s.data.Approvals[i]
		if w.InstanceID != instanceID || w.Status != ApprovalPending {
			continue
		}
		w.Status = ApprovalCancelled
		w.DecidedAt = &now
		w.DecidedBy = "system"
		w.Reason = reason
		out = append(out, approvalFromWire(*w))
	}
	if len(out) > 0 {
		if err := s.save(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PendingSummary implements Repository.PendingSummary (cross-project).
func (s *FileStore) PendingSummary(_ context.Context) (ApprovalSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	summary := ApprovalSummary{}
	for _, w := range s.data.Approvals {
		if w.Status != ApprovalPending {
			continue
		}
		summary.Total++
		if summary.NearestDeadline == nil || w.DeadlineAt.Before(*summary.NearestDeadline) {
			deadline := w.DeadlineAt
			summary.NearestDeadline = &deadline
		}
	}
	return summary, nil
}

// CreateGuidance implements Repository.CreateGuidance. Idempotent on
// (task_id, request_id) when request_id is non-empty.
func (s *FileStore) CreateGuidance(_ context.Context, in GuidanceInput) (*Guidance, bool, error) {
	if strings.TrimSpace(in.Message) == "" {
		return nil, false, fmt.Errorf("%w: guidance message is required", ErrValidation)
	}
	if strings.TrimSpace(in.TaskID) == "" {
		return nil, false, fmt.Errorf("%w: guidance task_id is required", ErrValidation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.taskLocked(in.TaskID); !ok {
		return nil, false, fmt.Errorf("%w: task %s not found", ErrNotFound, in.TaskID)
	}
	if in.RequestID != "" {
		for _, w := range s.data.Guidance {
			if w.TaskID == in.TaskID && w.RequestID == in.RequestID {
				g := guidanceFromWire(w)
				return &g, false, nil
			}
		}
	}
	g := Guidance{
		ID:         uuid.NewString(),
		TaskID:     in.TaskID,
		RunID:      in.RunID,
		InstanceID: in.InstanceID,
		Message:    in.Message,
		Status:     GuidanceQueued,
		CreatedBy:  in.CreatedBy,
		CreatedAt:  time.Now().UTC(),
		RequestID:  in.RequestID,
	}
	s.data.Guidance = append(s.data.Guidance, guidanceToWire(g))
	if err := s.save(); err != nil {
		return nil, false, err
	}
	return &g, true, nil
}

// ListGuidance implements Repository.ListGuidance (newest first).
func (s *FileStore) ListGuidance(_ context.Context, taskID string, limit int) ([]Guidance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []guidanceWire
	for _, w := range s.data.Guidance {
		if w.TaskID == taskID {
			matched = append(matched, w)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	out := make([]Guidance, 0, len(matched))
	for _, w := range matched {
		out = append(out, guidanceFromWire(w))
	}
	return out, nil
}

// UpdateGuidanceStatus implements Repository.UpdateGuidanceStatus.
func (s *FileStore) UpdateGuidanceStatus(_ context.Context, id, status string) (*Guidance, error) {
	switch status {
	case GuidanceInjected, GuidanceQueued, GuidanceConsumed:
	default:
		return nil, fmt.Errorf("%w: guidance status must be injected, queued or consumed", ErrValidation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Guidance {
		w := &s.data.Guidance[i]
		if w.ID != id {
			continue
		}
		w.Status = status
		if status == GuidanceConsumed && w.ConsumedAt == nil {
			now := time.Now().UTC()
			w.ConsumedAt = &now
		}
		if err := s.save(); err != nil {
			return nil, err
		}
		g := guidanceFromWire(*w)
		return &g, nil
	}
	return nil, ErrNotFound
}

// sortApprovalsNewestFirst orders approval wires by created_at descending,
// ID ascending as the deterministic tie-break.
func sortApprovalsNewestFirst(approvals []approvalWire) {
	sort.Slice(approvals, func(i, j int) bool {
		if !approvals[i].CreatedAt.Equal(approvals[j].CreatedAt) {
			return approvals[i].CreatedAt.After(approvals[j].CreatedAt)
		}
		return approvals[i].ID < approvals[j].ID
	})
}

// approvalToWire / approvalFromWire convert between the wire approval and its
// persisted subset.
func approvalToWire(a Approval) approvalWire {
	return approvalWire{Approval: a}
}

func approvalFromWire(w approvalWire) Approval {
	a := w.Approval
	a.ProjectName = ""
	a.TaskName = ""
	return a
}

// guidanceToWire / guidanceFromWire carry the idempotency key through the
// snapshot while keeping it off the API wire.
func guidanceToWire(g Guidance) guidanceWire {
	return guidanceWire{Guidance: g, RequestID: g.RequestID}
}

func guidanceFromWire(w guidanceWire) Guidance {
	g := w.Guidance
	g.RequestID = w.RequestID
	return g
}
