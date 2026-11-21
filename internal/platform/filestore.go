package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xalgord/xalgorix/v4/internal/storage"
)

// FileStore is the no-DSN degradation of the platform repository
// (REDESIGN §3.3): one full JSON snapshot at <dataDir>/_platform/platform.json,
// written with storage.WriteAtomic behind an RWMutex (same posture as
// auth-profiles). The implicit project carries the fixed ID "default" and can
// never be deleted.
type FileStore struct {
	mu   sync.RWMutex
	path string
	data fileSnapshot
}

// fileSnapshot is the whole persisted state. Wire variants exist only where
// the model hides fields from the API (task.Context) or projects fields the
// web layer fills at read time (approval.project_name/task_name).
type fileSnapshot struct {
	Version   int            `json:"version"`
	Projects  []Project      `json:"projects"`
	Tasks     []taskWire     `json:"tasks"`
	Runs      []runWire      `json:"runs"`
	Approvals []approvalWire `json:"approvals,omitempty"`
	Guidance  []guidanceWire `json:"guidance,omitempty"`
}

// approvalWire persists the approval record while suppressing the projected
// fields (project_name / task_name — the repository never fills them, mirroring
// the taskWire statistics). Shadow fields win the encoding/json name conflict
// at the shallower depth.
type approvalWire struct {
	Approval
	ProjectName string `json:"-"`
	TaskName    string `json:"-"`
}

// guidanceWire re-exposes RequestID (json:"-" on the model) for persistence:
// the idempotency key must survive the snapshot or a client retry after a
// restart would duplicate the human turn.
type guidanceWire struct {
	Guidance
	RequestID string `json:"request_id,omitempty"`
}

// taskWire re-exposes Context (json:"-" on the model) for persistence and
// suppresses the projected statistics (run_count / last_run_* /
// finding_count — the repository never fills them, so persisting the zero
// values would only blur the "cold-path projection, never stored" line).
// Shadow fields win the encoding/json name conflict at the shallower depth.
type taskWire struct {
	Task
	Context map[string]any `json:"context,omitempty"`

	RunCount      int    `json:"-"`
	LastRunAt     string `json:"-"`
	LastRunStatus string `json:"-"`
	FindingCount  int    `json:"-"`
}

// runWire persists only the mount identity; projected fields are never
// written so a stale snapshot cannot masquerade as status authority.
type runWire struct {
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	LegacyScanID string    `json:"legacy_scan_id,omitempty"`
	InstanceID   string    `json:"instance_id,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	Trigger      string    `json:"trigger,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

const fileSnapshotVersion = 1

// NewFileStore loads (or initializes) the snapshot at
// <dataDir>/_platform/platform.json. A missing or empty file starts an empty
// store; the default project is created lazily by EnsureDefaultProject.
func NewFileStore(dataDir string) (*FileStore, error) {
	dir := strings.TrimSpace(dataDir)
	if dir == "" {
		return nil, fmt.Errorf("platform: data directory is required for the file store")
	}
	path := FilePath(dataDir)
	s := &FileStore{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// First start: keep the in-memory empty snapshot.
	case err != nil:
		return nil, fmt.Errorf("platform: read snapshot %s: %w", path, err)
	default:
		if err := json.Unmarshal(raw, &s.data); err != nil {
			return nil, fmt.Errorf("platform: decode snapshot %s: %w", path, err)
		}
	}
	if s.data.Version == 0 {
		s.data.Version = fileSnapshotVersion
	}
	return s, nil
}

// FilePath is the canonical snapshot location for a data dir.
func FilePath(dataDir string) string {
	return filepath.Join(dataDir, "_platform", "platform.json")
}

// save persists the snapshot. Callers must hold s.mu for writing.
func (s *FileStore) save() error {
	if err := storage.EnsureSecureDir(filepath.Dir(s.path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("platform: marshal snapshot: %w", err)
	}
	if err := storage.WriteAtomic(s.path, data); err != nil {
		return fmt.Errorf("platform: write snapshot: %w", err)
	}
	return nil
}

// EnsureDefaultProject implements Repository.EnsureDefaultProject.
func (s *FileStore) EnsureDefaultProject(_ context.Context) (*Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Projects {
		if s.data.Projects[i].ID == DefaultProjectID {
			p := s.data.Projects[i]
			return &p, nil
		}
	}
	now := time.Now().UTC()
	p := Project{
		ID:        DefaultProjectID,
		Name:      "Default",
		Slug:      DefaultProjectSlug,
		Scope:     normalizeScope(Scope{}),
		CreatedAt: now,
	}
	s.data.Projects = append(s.data.Projects, p)
	if err := s.save(); err != nil {
		return nil, err
	}
	returned := p
	return &returned, nil
}

// CreateProject implements Repository.CreateProject.
func (s *FileStore) CreateProject(_ context.Context, name string, scope Scope) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: project name is required", ErrValidation)
	}
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// The default slug is owned by the implicit project.
	if DefaultProjectSlug == slugify(name, "") {
		return nil, fmt.Errorf("%w: slug %q is reserved", ErrConflict, DefaultProjectSlug)
	}
	existing := make(map[string]struct{}, len(s.data.Projects))
	for _, p := range s.data.Projects {
		existing[p.Slug] = struct{}{}
	}
	base := slugify(name, "")
	if base == "" {
		return nil, fmt.Errorf("%w: project name must contain ASCII slug characters", ErrValidation)
	}
	p := Project{
		ID:        uuid.NewString(),
		Name:      name,
		Slug:      uniqueSlug(base, existing),
		Scope:     normalizeScope(scope),
		CreatedAt: time.Now().UTC(),
	}
	s.data.Projects = append(s.data.Projects, p)
	if err := s.save(); err != nil {
		return nil, err
	}
	returned := p
	return &returned, nil
}

// ListProjects implements Repository.ListProjects (archived excluded).
func (s *FileStore) ListProjects(_ context.Context) ([]Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Project, 0, len(s.data.Projects))
	for _, p := range s.data.Projects {
		if p.ArchivedAt != nil {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// GetProject implements Repository.GetProject.
func (s *FileStore) GetProject(_ context.Context, id string) (*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.data.Projects {
		if p.ID == id {
			returned := p
			return &returned, nil
		}
	}
	return nil, ErrNotFound
}

// UpdateProject implements Repository.UpdateProject.
func (s *FileStore) UpdateProject(_ context.Context, id string, upd ProjectUpdate) (*Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Projects {
		p := &s.data.Projects[i]
		if p.ID != id {
			continue
		}
		if upd.Name != nil {
			name := strings.TrimSpace(*upd.Name)
			if name == "" {
				return nil, fmt.Errorf("%w: project name is required", ErrValidation)
			}
			p.Name = name
		}
		if upd.Scope != nil {
			if err := validateScope(*upd.Scope); err != nil {
				return nil, err
			}
			p.Scope = normalizeScope(*upd.Scope)
		}
		if upd.Archived != nil {
			if *upd.Archived {
				if p.ArchivedAt == nil {
					now := time.Now().UTC()
					p.ArchivedAt = &now
				}
			} else {
				p.ArchivedAt = nil
			}
		}
		if err := s.save(); err != nil {
			return nil, err
		}
		returned := *p
		return &returned, nil
	}
	return nil, ErrNotFound
}

// DeleteProject implements Repository.DeleteProject.
func (s *FileStore) DeleteProject(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.data.Projects {
		if s.data.Projects[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	if s.data.Projects[idx].ID == DefaultProjectID || s.data.Projects[idx].Slug == DefaultProjectSlug {
		return fmt.Errorf("%w: the default project cannot be deleted", ErrConflict)
	}
	deleted := s.data.Projects[idx].ID
	s.data.Projects = append(s.data.Projects[:idx], s.data.Projects[idx+1:]...)
	taskIDs := map[string]struct{}{}
	keptTasks := s.data.Tasks[:0]
	for _, t := range s.data.Tasks {
		if t.ProjectID == deleted {
			taskIDs[t.ID] = struct{}{}
			continue
		}
		keptTasks = append(keptTasks, t)
	}
	s.data.Tasks = keptTasks
	keptRuns := s.data.Runs[:0]
	for _, r := range s.data.Runs {
		if _, hit := taskIDs[r.TaskID]; hit {
			continue
		}
		keptRuns = append(keptRuns, r)
	}
	s.data.Runs = keptRuns
	return s.save()
}

// CreateTask implements Repository.CreateTask.
func (s *FileStore) CreateTask(ctx context.Context, projectID string, in TaskInput) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projectLocked(projectID); !ok {
		return nil, fmt.Errorf("%w: project %s not found", ErrNotFound, projectID)
	}
	if p, _ := s.projectLocked(projectID); p.ArchivedAt != nil {
		return nil, fmt.Errorf("%w: project %s is archived", ErrConflict, projectID)
	}
	return s.createTaskLocked(projectID, in, nil)
}

// projectLocked returns the project by ID. Callers hold s.mu.
func (s *FileStore) projectLocked(id string) (Project, bool) {
	for _, p := range s.data.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

// ListTasks implements Repository.ListTasks (archived excluded).
func (s *FileStore) ListTasks(_ context.Context, projectID string) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0)
	for _, t := range s.data.Tasks {
		if t.ProjectID != projectID || t.ArchivedAt != nil {
			continue
		}
		task := t.Task
		task.Context = t.Context
		out = append(out, task)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// GetTask implements Repository.GetTask.
func (s *FileStore) GetTask(_ context.Context, id string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.data.Tasks {
		if t.ID == id {
			returned := t.Task
			returned.Context = t.Context
			return &returned, nil
		}
	}
	return nil, ErrNotFound
}

// UpdateTask implements Repository.UpdateTask.
func (s *FileStore) UpdateTask(_ context.Context, id string, upd TaskUpdate) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		tw := &s.data.Tasks[i]
		if tw.ID != id {
			continue
		}
		if upd.Name != nil {
			name := strings.TrimSpace(*upd.Name)
			if name == "" {
				return nil, fmt.Errorf("%w: task name is required", ErrValidation)
			}
			tw.Name = name
		}
		if upd.Goal != nil {
			tw.Goal = *upd.Goal
		}
		if upd.Instruction != nil {
			tw.Instruction = *upd.Instruction
		}
		if upd.PhasePolicy != nil {
			tw.PhasePolicy = upd.PhasePolicy
		}
		if upd.Schedule != nil {
			tw.Schedule = upd.Schedule
		}
		if upd.ContextPaths != nil {
			tw.ContextPaths = normalizeContextPaths(upd.ContextPaths)
		}
		if upd.Context != nil {
			merged := tw.Context
			if merged == nil {
				merged = map[string]any{}
			}
			for k, v := range upd.Context {
				merged[k] = v
			}
			tw.Context = merged
		}
		if upd.Archived != nil {
			if *upd.Archived {
				if tw.ArchivedAt == nil {
					now := time.Now().UTC()
					tw.ArchivedAt = &now
				}
			} else {
				tw.ArchivedAt = nil
			}
		}
		now := time.Now().UTC()
		tw.UpdatedAt = &now
		tw.Task.UpdatedAt = tw.UpdatedAt
		tw.Task.Context = tw.Context
		if err := s.save(); err != nil {
			return nil, err
		}
		returned := tw.Task
		return &returned, nil
	}
	return nil, ErrNotFound
}

// DeleteTask implements Repository.DeleteTask.
func (s *FileStore) DeleteTask(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	s.data.Tasks = append(s.data.Tasks[:idx], s.data.Tasks[idx+1:]...)
	keptRuns := s.data.Runs[:0]
	for _, r := range s.data.Runs {
		if r.TaskID == id {
			continue
		}
		keptRuns = append(keptRuns, r)
	}
	s.data.Runs = keptRuns
	return s.save()
}

// CreateRun implements Repository.CreateRun.
func (s *FileStore) CreateRun(_ context.Context, taskID string, mount RunMount) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.taskLocked(taskID); !ok {
		return nil, fmt.Errorf("%w: task %s not found", ErrNotFound, taskID)
	}
	if strings.TrimSpace(mount.InstanceID) == "" {
		return nil, fmt.Errorf("%w: run instance_id is required", ErrValidation)
	}
	for _, r := range s.data.Runs {
		if r.InstanceID == mount.InstanceID {
			return nil, fmt.Errorf("%w: instance %s is already mounted", ErrConflict, mount.InstanceID)
		}
	}
	run := Run{
		ID:           uuid.NewString(),
		TaskID:       taskID,
		LegacyScanID: mount.LegacyScanID,
		InstanceID:   mount.InstanceID,
		Mode:         mount.Mode,
		Trigger:      mount.Trigger,
		CreatedAt:    time.Now().UTC(),
	}
	s.data.Runs = append(s.data.Runs, runToWire(run))
	if err := s.save(); err != nil {
		return nil, err
	}
	return &run, nil
}

// taskLocked returns the task wire by ID. Callers hold s.mu.
func (s *FileStore) taskLocked(id string) (taskWire, bool) {
	for _, t := range s.data.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return taskWire{}, false
}

// ListRuns implements Repository.ListRuns (newest first, paginated).
func (s *FileStore) ListRuns(_ context.Context, taskID string, page, size int) ([]Run, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var all []runWire
	for _, r := range s.data.Runs {
		if r.TaskID == taskID {
			all = append(all, r)
		}
	}
	sortRunsNewestFirst(all)
	total := len(all)
	page, size = normalizePage(page, size)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	out := make([]Run, 0, end-start)
	for _, w := range all[start:end] {
		out = append(out, runFromWire(w))
	}
	return out, total, nil
}

// ListRunsByProject implements Repository.ListRunsByProject.
func (s *FileStore) ListRunsByProject(_ context.Context, projectID string) ([]Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	taskIDs := map[string]struct{}{}
	for _, t := range s.data.Tasks {
		if t.ProjectID == projectID {
			taskIDs[t.ID] = struct{}{}
		}
	}
	var all []runWire
	for _, r := range s.data.Runs {
		if _, hit := taskIDs[r.TaskID]; hit {
			all = append(all, r)
		}
	}
	sortRunsNewestFirst(all)
	out := make([]Run, 0, len(all))
	for _, w := range all {
		out = append(out, runFromWire(w))
	}
	return out, nil
}

// AllRuns implements Repository.AllRuns.
func (s *FileStore) AllRuns(_ context.Context) ([]Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	wire := append([]runWire(nil), s.data.Runs...)
	sortRunsNewestFirst(wire)
	out := make([]Run, 0, len(wire))
	for _, w := range wire {
		out = append(out, runFromWire(w))
	}
	return out, nil
}

// DeleteRunsByScanIdentity implements Repository.DeleteRunsByScanIdentity.
func (s *FileStore) DeleteRunsByScanIdentity(_ context.Context, instanceID, legacyScanID string) (int, error) {
	instanceID = strings.TrimSpace(instanceID)
	legacyScanID = strings.TrimSpace(legacyScanID)
	if instanceID == "" && legacyScanID == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.data.Runs[:0]
	removed := 0
	for _, r := range s.data.Runs {
		if (instanceID != "" && r.InstanceID == instanceID) ||
			(legacyScanID != "" && r.LegacyScanID == legacyScanID) {
			removed++
			continue
		}
		kept = append(kept, r)
	}
	s.data.Runs = kept
	if removed == 0 {
		return 0, nil
	}
	if err := s.save(); err != nil {
		return 0, err
	}
	return removed, nil
}

// RunsByInstanceID implements Repository.RunsByInstanceID.
func (s *FileStore) RunsByInstanceID(_ context.Context, instanceID string) ([]Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Run
	for _, w := range s.data.Runs {
		if w.InstanceID == instanceID {
			out = append(out, runFromWire(w))
		}
	}
	return out, nil
}

// InstanceIDsForProject implements Repository.InstanceIDsForProject.
func (s *FileStore) InstanceIDsForProject(_ context.Context, projectID string) ([]string, error) {
	runs, err := s.ListRunsByProject(context.Background(), projectID)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		if r.InstanceID == "" {
			continue
		}
		if _, dup := seen[r.InstanceID]; dup {
			continue
		}
		seen[r.InstanceID] = struct{}{}
		out = append(out, r.InstanceID)
	}
	return out, nil
}

// ProjectScanIDs implements Repository.ProjectScanIDs.
func (s *FileStore) ProjectScanIDs(ctx context.Context, projectID string) ([]string, error) {
	runs, err := s.ListRunsByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(runs)*2)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, r := range runs {
		add(r.InstanceID)
		add(r.LegacyScanID)
	}
	return out, nil
}

// LegacyScanIDs implements Repository.LegacyScanIDs.
func (s *FileStore) LegacyScanIDs(_ context.Context) (map[string]struct{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]struct{}{}
	for _, r := range s.data.Runs {
		if r.LegacyScanID != "" {
			out[r.LegacyScanID] = struct{}{}
		}
	}
	return out, nil
}

// ImportLegacyRun implements Repository.ImportLegacyRun.
func (s *FileStore) ImportLegacyRun(_ context.Context, taskID string, mount RunMount) (*Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.taskLocked(taskID); !ok {
		return nil, false, fmt.Errorf("%w: task %s not found", ErrNotFound, taskID)
	}
	for _, w := range s.data.Runs {
		if (w.LegacyScanID != "" && w.LegacyScanID == mount.LegacyScanID) ||
			(w.InstanceID != "" && w.InstanceID == mount.InstanceID) {
			run := runFromWire(w)
			return &run, false, nil
		}
	}
	run := Run{
		ID:           uuid.NewString(),
		TaskID:       taskID,
		LegacyScanID: mount.LegacyScanID,
		InstanceID:   mount.InstanceID,
		Mode:         mount.Mode,
		Trigger:      mount.Trigger,
		CreatedAt:    time.Now().UTC(),
	}
	s.data.Runs = append(s.data.Runs, runToWire(run))
	if err := s.save(); err != nil {
		return nil, false, err
	}
	return &run, true, nil
}

// EnsureImportTask implements Repository.EnsureImportTask.
func (s *FileStore) EnsureImportTask(_ context.Context, projectID string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projectLocked(projectID); !ok {
		return nil, fmt.Errorf("%w: project %s not found", ErrNotFound, projectID)
	}
	for _, t := range s.data.Tasks {
		if t.ProjectID == projectID && t.Context["legacy_import"] == "scans" {
			returned := t.Task
			returned.Context = t.Context
			return &returned, nil
		}
	}
	existing := map[string]struct{}{}
	for _, t := range s.data.Tasks {
		if t.ProjectID != projectID {
			continue
		}
		if slug, _ := t.Context["slug"].(string); slug != "" {
			existing[slug] = struct{}{}
		}
	}
	now := time.Now().UTC()
	t := Task{
		ID:        uuid.NewString(),
		ProjectID: projectID,
		Name:      "导入扫描",
		Context: map[string]any{
			"legacy_import": "scans",
			"slug":          uniqueSlug("imported-scans", existing),
		},
		CreatedAt: now,
	}
	s.data.Tasks = append(s.data.Tasks, taskWire{Task: t, Context: t.Context})
	if err := s.save(); err != nil {
		return nil, err
	}
	returned := t
	return &returned, nil
}

// ImportScheduleTask implements Repository.ImportScheduleTask.
func (s *FileStore) ImportScheduleTask(_ context.Context, projectID string, in TaskInput, legacyScheduleID string) (*Task, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projectLocked(projectID); !ok {
		return nil, false, fmt.Errorf("%w: project %s not found", ErrNotFound, projectID)
	}
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ProjectID == projectID && t.Context["legacy_schedule_id"] == legacyScheduleID {
			returned := t.Task
			returned.Context = t.Context
			return &returned, false, nil
		}
	}
	created, err := s.createTaskLocked(projectID, in, map[string]any{"legacy_schedule_id": legacyScheduleID})
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// createTaskLocked is CreateTask with an extra context seed. Callers hold s.mu.
func (s *FileStore) createTaskLocked(projectID string, in TaskInput, contextSeed map[string]any) (*Task, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, fmt.Errorf("%w: task name is required", ErrValidation)
	}
	existing := map[string]struct{}{}
	for _, t := range s.data.Tasks {
		if t.ProjectID != projectID {
			continue
		}
		if slug, _ := t.Context["slug"].(string); slug != "" {
			existing[slug] = struct{}{}
		}
	}
	contextMap := in.Context
	if contextMap == nil {
		contextMap = map[string]any{}
	}
	for k, v := range contextSeed {
		contextMap[k] = v
	}
	if _, has := contextMap["slug"]; !has {
		contextMap["slug"] = uniqueSlug(slugify(in.Name, ""), existing)
	}
	t := Task{
		ID:           uuid.NewString(),
		ProjectID:    projectID,
		Name:         in.Name,
		Goal:         in.Goal,
		Instruction:  in.Instruction,
		PhasePolicy:  in.PhasePolicy,
		Schedule:     in.Schedule,
		ContextPaths: normalizeContextPaths(in.ContextPaths),
		Context:      contextMap,
		CreatedAt:    time.Now().UTC(),
	}
	s.data.Tasks = append(s.data.Tasks, taskWire{Task: t, Context: contextMap})
	if err := s.save(); err != nil {
		return nil, err
	}
	returned := t
	return &returned, nil
}

// sortRunsNewestFirst orders run wires by created_at descending, ID ascending
// as the deterministic tie-break.
func sortRunsNewestFirst(runs []runWire) {
	sort.Slice(runs, func(i, j int) bool {
		if !runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].CreatedAt.After(runs[j].CreatedAt)
		}
		return runs[i].ID < runs[j].ID
	})
}

// normalizePage clamps page/size to the API convention (page ≥ 1, size ≥ 1).
func normalizePage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 50
	}
	return page, size
}

// runToWire / runFromWire convert between the wire Run and its persisted
// mount subset.
func runToWire(r Run) runWire {
	return runWire{
		ID:           r.ID,
		TaskID:       r.TaskID,
		LegacyScanID: r.LegacyScanID,
		InstanceID:   r.InstanceID,
		Mode:         r.Mode,
		Trigger:      r.Trigger,
		CreatedAt:    r.CreatedAt,
	}
}

func runFromWire(w runWire) Run {
	return Run{
		ID:           w.ID,
		TaskID:       w.TaskID,
		LegacyScanID: w.LegacyScanID,
		InstanceID:   w.InstanceID,
		Mode:         w.Mode,
		Trigger:      w.Trigger,
		CreatedAt:    w.CreatedAt,
	}
}
