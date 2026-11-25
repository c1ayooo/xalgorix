// Package intelsubmit records whether a proven result actually became points.
//
// It answers one question per (task, challenge): pending / submitted /
// invalid / duplicate. identity is the platform's unique_code, not the
// scan_id, because scoring is a fact about the task while notes and findings
// are partitioned per scan session — the gap that let a run finish with
// verified findings and no submission.
//
// The package is a projection, never an authority on truth: state is written
// from platform responses the engine observed, and awarded only ever comes
// from the platform's own `awarded` field.
package intelsubmit

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// State is the lifecycle of one submission receipt.
type State string

const (
	// StatePending means a container is (or was) up and a result may exist for
	// it, but no correct submission has been observed.
	StatePending State = "pending"
	// StateSubmitted means the platform answered correct=true; Awarded is set.
	StateSubmitted State = "submitted"
	// StateInvalid means the platform answered correct=false, or the container
	// was closed after the pending receipt (the flag died with it).
	StateInvalid State = "invalid"
	// StateDuplicate means the platform answered duplicate (idempotent re-submit
	// of a flag already scored); it is neither a gain nor a loss.
	StateDuplicate State = "duplicate"
)

func (s State) Valid() bool {
	switch s {
	case StatePending, StateSubmitted, StateInvalid, StateDuplicate:
		return true
	default:
		return false
	}
}

// Submit is one receipt row.
type Submit struct {
	ID            int64      `json:"id"`
	ProjectID     string     `json:"project_id"`
	TaskID        string     `json:"task_id"`
	UniqueCode    string     `json:"unique_code"`
	Fingerprint   string     `json:"fingerprint,omitempty"`
	Endpoint      string     `json:"endpoint,omitempty"`
	State         State      `json:"state"`
	Awarded       int        `json:"awarded"`
	Attempts      int        `json:"attempts"`
	LastResponse  string     `json:"last_response,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// UpsertInput is one observation of a platform response.
//
// A zero State keeps whatever the row already holds, which is what start and
// close need: start creates a pending row (or leaves an existing submitted one
// alone), close invalidates pending rows without overwriting a scored one.
type UpsertInput struct {
	ProjectID   string
	TaskID      string
	UniqueCode  string
	Fingerprint string
	Endpoint    string
	State       State
	Awarded     int
	Response    string
	// Attempted marks this observation as a submit attempt (increments attempts
	// and stamps last_attempt_at). start/close set it false.
	Attempted bool
}

// Store is the persistence surface. Every method is idempotent on
// (TaskID, UniqueCode).
type Store interface {
	// Upsert writes one receipt observation.
	Upsert(ctx context.Context, in UpsertInput) (Submit, error)
	// ListPending returns unsubmitted receipts for a task, oldest first, capped.
	// This is the handoff list: it is what a drained frontier, the finish gate
	// and a fresh session's seed all read.
	ListPending(ctx context.Context, taskID string, limit int) ([]Submit, error)
	// ListByTask returns every receipt for a task (newest update first).
	ListByTask(ctx context.Context, taskID string, limit int) ([]Submit, error)
	// InvalidateByCode closes one pending receipt by the platform's own
	// challenge code. A closed container takes its flag with it, so a pending
	// row that outlives it is a stale fact, not a debt.
	InvalidateByCode(ctx context.Context, taskID, uniqueCode, reason string) (int, error)
	// InvalidateByEndpoint closes every pending receipt that came from one
	// container endpoint (defensive: a close response without a code still
	// releases the slots it was holding).
	InvalidateByEndpoint(ctx context.Context, taskID, endpoint, reason string) (int, error)
}

var (
	storeMu sync.RWMutex
	store   Store
)

// SetStore registers the process-wide store (nil resets it). Same discipline as
// the other backends: registered once at startup, read through StoreOrNil.
func SetStore(s Store) {
	storeMu.Lock()
	defer storeMu.Unlock()
	store = s
}

// StoreOrNil returns the registered store, or nil when PG was never wired
// (legacy CLI runs stay functional without receipts).
func StoreOrNil() Store {
	storeMu.RLock()
	defer storeMu.RUnlock()
	return store
}

// PendingDigest renders the unsubmitted receipts of one task as a single
// compact line ("3 pending (a-05@10.0.177.16:80, …)"), or "" when nothing is
// pending or no store is wired.
//
// Both the drained-frontier notice and the finish gate use this so they cannot
// disagree about what "done" means: a drained plan with unsubmitted results is
// not done.
func PendingDigest(ctx context.Context, taskID string, limit int) string {
	s := StoreOrNil()
	taskID = strings.TrimSpace(taskID)
	if s == nil || taskID == "" {
		return ""
	}
	list, err := s.ListPending(ctx, taskID, limit)
	if err != nil || len(list) == 0 {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, r := range list {
		where := r.Endpoint
		if where == "" {
			where = "endpoint unknown"
		}
		parts = append(parts, r.UniqueCode+"@"+where)
	}
	return fmt.Sprintf("%d pending (%s)", len(list), strings.Join(parts, ", "))
}
