package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Desired-state bookkeeping.
//
// Authority split (charter §5 / D12): the ACTUAL state of a container belongs to
// the container runtime and is read back through labels; the DESIRED state and
// the reconciliation watermark belong to PostgreSQL. Without a durable intent, a
// crash between "container created" and "state recorded" would be unrecoverable.
//
// The watermark protocol mirrors the InfoItem cold path: desired_gen is bumped
// by the writer, applied_gen is advanced by the reconciler, and status records
// pending/applied/gap/failed.

// Desired-state values stored in toolbox_projects.desired_state.
const (
	DesiredPending  = "pending"
	DesiredReady    = "ready"
	DesiredStopping = "stopping"
	DesiredRemoved  = "removed"
)

// Reconcile status values stored in toolbox_projects.status. These mirror
// runstate.Status* so a single operational vocabulary covers both pipelines.
const (
	ReconcilePending = "pending"
	ReconcileApplied = "applied"
	ReconcileGap     = "gap"
	ReconcileFailed  = "failed"
)

// DesiredProject is one row of toolbox_projects.
type DesiredProject struct {
	ProjectID    string    `json:"project_id"`
	ScanID       string    `json:"scan_id"`
	InstanceID   string    `json:"instance_id"`
	ImageDigest  string    `json:"image_digest"`
	Profile      string    `json:"profile"`
	NetProfile   string    `json:"net_profile"`
	DesiredState string    `json:"desired_state"`
	DesiredGen   int64     `json:"desired_gen"`
	AppliedGen   int64     `json:"applied_gen"`
	Status       string    `json:"status"`
	ContainerID  string    `json:"container_id"`
	LastError    string    `json:"last_error"`
	// Spec is the serialised ProjectSpec so the reconciler can rebuild the
	// exact container without consulting live memory after a restart.
	Spec      string    `json:"spec"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProjectSpecFromRow decodes the stored spec.
func (r DesiredProject) ProjectSpec() (ProjectSpec, error) {
	if r.Spec == "" {
		return ProjectSpec{}, fmt.Errorf("runtime: project %q has no stored spec", r.ProjectID)
	}
	var spec ProjectSpec
	if err := json.Unmarshal([]byte(r.Spec), &spec); err != nil {
		return ProjectSpec{}, fmt.Errorf("runtime: decode spec for %q: %w", r.ProjectID, err)
	}
	return spec, nil
}

// DesiredStateStore persists desired state and reconciliation watermarks.
type DesiredStateStore interface {
	// RecordProject writes (or refreshes) the desired state. It bumps
	// desired_gen only when the desired state or the spec actually changed, so
	// repeated EnsureProject calls on the hot path do not create churn.
	RecordProject(ctx context.Context, row DesiredProject) (DesiredProject, error)
	// MarkApplied advances applied_gen and records the actual container.
	MarkApplied(ctx context.Context, projectID string, gen int64, containerID, state string) error
	// MarkFailed records a failed reconciliation attempt.
	MarkFailed(ctx context.Context, projectID string, gen int64, msg string) error
	// MarkGap flags a row whose applied_gen is behind by more than the caller's
	// tolerance, so the GC job retries it instead of the hot path blocking.
	MarkGap(ctx context.Context, projectID string, gen int64, msg string) error
	// Unreconciled returns rows needing work, oldest first.
	Unreconciled(ctx context.Context, limit int) ([]DesiredProject, error)
	// Active returns rows that still want a live container (pending/ready),
	// regardless of their watermark. Idle reaping needs this view.
	Active(ctx context.Context) ([]DesiredProject, error)
	// Get returns one row.
	Get(ctx context.Context, projectID string) (DesiredProject, error)
	// Delete drops bookkeeping for a removed project.
	Delete(ctx context.Context, projectID string) error
}

// ErrProjectNotFound is returned by DesiredStateStore.Get.
var ErrProjectNotFound = errors.New("runtime: project not found")

// MemoryDesiredState is an in-memory DesiredStateStore for tests and for
// native-mode runs where no container bookkeeping is required.
type MemoryDesiredState struct {
	mu   sync.Mutex
	rows map[string]DesiredProject
	now  func() time.Time
}

// NewMemoryDesiredState returns an empty in-memory store.
func NewMemoryDesiredState() *MemoryDesiredState {
	return &MemoryDesiredState{rows: make(map[string]DesiredProject), now: time.Now}
}

// RecordProject implements DesiredStateStore.
func (m *MemoryDesiredState) RecordProject(_ context.Context, row DesiredProject) (DesiredProject, error) {
	if row.ProjectID == "" {
		return DesiredProject{}, fmt.Errorf("runtime: project id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	cur, ok := m.rows[row.ProjectID]
	if !ok {
		if row.DesiredGen <= 0 {
			row.DesiredGen = 1
		}
		if row.DesiredState == "" {
			row.DesiredState = DesiredPending
		}
		row.Status = ReconcilePending
		row.CreatedAt = now
		row.UpdatedAt = now
		m.rows[row.ProjectID] = row
		return row, nil
	}
	changed := cur.DesiredState != row.DesiredState || cur.Spec != row.Spec || cur.ImageDigest != row.ImageDigest
	cur.ScanID = row.ScanID
	cur.InstanceID = row.InstanceID
	cur.ImageDigest = row.ImageDigest
	cur.Profile = row.Profile
	cur.NetProfile = row.NetProfile
	cur.Spec = row.Spec
	cur.DesiredState = row.DesiredState
	if changed {
		cur.DesiredGen++
		cur.Status = ReconcilePending
	}
	cur.UpdatedAt = now
	m.rows[row.ProjectID] = cur
	return cur, nil
}

// MarkApplied implements DesiredStateStore.
func (m *MemoryDesiredState) MarkApplied(_ context.Context, projectID string, gen int64, containerID, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[projectID]
	if !ok {
		return ErrProjectNotFound
	}
	if gen > row.AppliedGen {
		row.AppliedGen = gen
	}
	if row.AppliedGen >= row.DesiredGen {
		row.Status = ReconcileApplied
	} else {
		row.Status = ReconcilePending
	}
	row.LastError = ""
	if containerID != "" {
		row.ContainerID = containerID
	}
	row.UpdatedAt = m.now().UTC()
	m.rows[projectID] = row
	return nil
}

// MarkFailed implements DesiredStateStore.
func (m *MemoryDesiredState) MarkFailed(_ context.Context, projectID string, _ int64, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[projectID]
	if !ok {
		return ErrProjectNotFound
	}
	row.Status = ReconcileFailed
	row.LastError = msg
	row.UpdatedAt = m.now().UTC()
	m.rows[projectID] = row
	return nil
}

// MarkGap implements DesiredStateStore.
func (m *MemoryDesiredState) MarkGap(_ context.Context, projectID string, _ int64, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[projectID]
	if !ok {
		return ErrProjectNotFound
	}
	row.Status = ReconcileGap
	row.LastError = msg
	row.UpdatedAt = m.now().UTC()
	m.rows[projectID] = row
	return nil
}

// Unreconciled implements DesiredStateStore.
func (m *MemoryDesiredState) Unreconciled(_ context.Context, limit int) ([]DesiredProject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DesiredProject, 0, len(m.rows))
	for _, row := range m.rows {
		if row.AppliedGen < row.DesiredGen || row.Status != ReconcileApplied {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Active implements DesiredStateStore.
func (m *MemoryDesiredState) Active(_ context.Context) ([]DesiredProject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DesiredProject, 0, len(m.rows))
	for _, row := range m.rows {
		if row.DesiredState == DesiredPending || row.DesiredState == DesiredReady {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	return out, nil
}

// Get implements DesiredStateStore.
func (m *MemoryDesiredState) Get(_ context.Context, projectID string) (DesiredProject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[projectID]
	if !ok {
		return DesiredProject{}, ErrProjectNotFound
	}
	return row, nil
}

// Delete implements DesiredStateStore.
func (m *MemoryDesiredState) Delete(_ context.Context, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, projectID)
	return nil
}

// Rows returns a snapshot for assertions in tests.
func (m *MemoryDesiredState) Rows() []DesiredProject {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DesiredProject, 0, len(m.rows))
	for _, row := range m.rows {
		out = append(out, row)
	}
	return out
}
