package platform

// FileStore approval-rules implementation ("approve & remember", REDESIGN
// §5.2, API_CONTRACT §3.2). Same posture as approvals: snapshot-backed CRUD
// behind the store RWMutex. Expired rules are deliberately returned by
// ListApprovalRules — expiry is a match-time concern and the UI displays the
// state, so the operator can see (and delete) what their rules did.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// CreateApprovalRule implements Repository.CreateApprovalRule.
func (s *FileStore) CreateApprovalRule(_ context.Context, in ApprovalRuleInput) (*ApprovalRule, error) {
	if err := validateApprovalRule(in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projectLocked(in.ProjectID); !ok {
		return nil, fmt.Errorf("%w: project %s not found", ErrNotFound, in.ProjectID)
	}
	r := ApprovalRule{
		ID:                    uuid.NewString(),
		ProjectID:             in.ProjectID,
		Kind:                  in.Kind,
		Match:                 in.Match,
		Reason:                in.Reason,
		CreatedBy:             in.CreatedBy,
		CreatedFromApprovalID: in.CreatedFromApprovalID,
		CreatedAt:             time.Now().UTC(),
		ExpiresAt:             in.ExpiresAt,
	}
	s.data.ApprovalRules = append(s.data.ApprovalRules, r)
	if err := s.save(); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListApprovalRules implements Repository.ListApprovalRules (newest first).
func (s *FileStore) ListApprovalRules(_ context.Context, filter ApprovalRuleFilter) ([]ApprovalRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []ApprovalRule
	for _, r := range s.data.ApprovalRules {
		if filter.ProjectID != "" && r.ProjectID != filter.ProjectID {
			continue
		}
		if filter.Kind != "" && r.Kind != filter.Kind {
			continue
		}
		matched = append(matched, r)
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	out := make([]ApprovalRule, 0, len(matched))
	out = append(out, matched...)
	return out, nil
}

// DeleteApprovalRule implements Repository.DeleteApprovalRule.
func (s *FileStore) DeleteApprovalRule(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.ApprovalRules {
		if s.data.ApprovalRules[i].ID == id {
			s.data.ApprovalRules = append(s.data.ApprovalRules[:i], s.data.ApprovalRules[i+1:]...)
			return s.save()
		}
	}
	return ErrNotFound
}

// RecordApprovalRuleHit implements Repository.RecordApprovalRuleHit. A
// missing rule is not an error: hit bookkeeping is a best-effort audit
// projection and must never wedge the gate's allow path.
func (s *FileStore) RecordApprovalRuleHit(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.ApprovalRules {
		w := &s.data.ApprovalRules[i]
		if w.ID != id {
			continue
		}
		now := time.Now().UTC()
		w.HitCount++
		w.LastHitAt = &now
		return s.save()
	}
	return nil
}
