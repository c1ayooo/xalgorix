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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the PostgreSQL implementation of Repository. It only owns the
// cold-path project/task/run records (REDESIGN §3.2); scan status stays in
// scan.json and is projected by the web layer.
type Store struct {
	pool *pgxpool.Pool
}

// NewPostgres wraps a pool. The caller owns pool lifecycle (platform.Open
// sets it up; Close releases it).
func NewPostgres(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Close releases the pool.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if s == nil || s.pool == nil {
		return pgconn.CommandTag{}, errors.New("platform/postgres: store unavailable")
	}
	return s.pool.Exec(ctx, sql, args...)
}

func (s *Store) query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("platform/postgres: store unavailable")
	}
	return s.pool.Query(ctx, sql, args...)
}

func (s *Store) queryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if s == nil || s.pool == nil {
		return nil
	}
	return s.pool.QueryRow(ctx, sql, args...)
}

// parseUUID normalizes an ID; a non-UUID string simply cannot exist as a PG
// row ID, so it maps to ErrNotFound instead of a driver cast error.
func parseUUID(id string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: bad id %q", ErrNotFound, id)
	}
	return parsed, nil
}

// mapConstraintErr translates unique-violation errors into ErrConflict with
// the offending constraint named.
func mapConstraintErr(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s (%s)", ErrConflict, what, pgErr.ConstraintName)
	}
	return fmt.Errorf("platform/postgres: %s: %w", what, err)
}

// ── projects ─────────────────────────────────────────────────────────────────

const projectColumns = `id, name, slug, scope, created_at, archived_at`

func scanProjectRow(row pgx.Row) (*Project, error) {
	var (
		p         Project
		scopeJSON []byte
	)
	if err := row.Scan(&p.ID, &p.Name, &p.Slug, &scopeJSON, &p.CreatedAt, &p.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: scan project: %w", err)
	}
	if err := json.Unmarshal(scopeJSON, &p.Scope); err != nil {
		return nil, fmt.Errorf("platform/postgres: decode project scope: %w", err)
	}
	p.Scope = normalizeScope(p.Scope)
	p.CreatedAt = p.CreatedAt.UTC()
	if p.ArchivedAt != nil {
		t := p.ArchivedAt.UTC()
		p.ArchivedAt = &t
	}
	return &p, nil
}

// EnsureDefaultProject implements Repository.EnsureDefaultProject (PG seed,
// ON CONFLICT DO NOTHING per the R1 decision).
func (s *Store) EnsureDefaultProject(ctx context.Context) (*Project, error) {
	if p, err := s.projectBySlug(ctx, DefaultProjectSlug); err == nil {
		return p, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	scope, err := marshalJSON(normalizeScope(Scope{}))
	if err != nil {
		return nil, err
	}
	_, err = s.exec(ctx, `
		INSERT INTO projects (id, name, slug, scope, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (slug) DO NOTHING`,
		uuid.NewString(), "Default", DefaultProjectSlug, scope, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: seed default project: %w", err)
	}
	return s.projectBySlug(ctx, DefaultProjectSlug)
}

func (s *Store) projectBySlug(ctx context.Context, slug string) (*Project, error) {
	return scanProjectRow(s.queryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE slug = $1`, slug))
}

// CreateProject implements Repository.CreateProject.
func (s *Store) CreateProject(ctx context.Context, name string, scope Scope) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: project name is required", ErrValidation)
	}
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	// A name with no ASCII slug characters (e.g. a Chinese display name — the
	// dashboard defaults to zh-CN) slugifies to empty; fall back to the fresh
	// id so the project is still creatable, matching the mock contract.
	base := slugify(name, id)
	if base == DefaultProjectSlug {
		return nil, fmt.Errorf("%w: slug %q is reserved", ErrConflict, DefaultProjectSlug)
	}
	existing, err := s.existingSlugs(ctx)
	if err != nil {
		return nil, err
	}
	scopeJSON, err := marshalJSON(normalizeScope(scope))
	if err != nil {
		return nil, err
	}
	_, err = s.exec(ctx, `
		INSERT INTO projects (id, name, slug, scope, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		id, name, uniqueSlug(base, existing), scopeJSON, time.Now().UTC())
	if err != nil {
		return nil, mapConstraintErr(err, "create project")
	}
	return s.GetProject(ctx, id)
}

func (s *Store) existingSlugs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.query(ctx, `SELECT slug FROM projects`)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list slugs: %w", err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan slug: %w", err)
		}
		out[slug] = struct{}{}
	}
	return out, rows.Err()
}

// ListProjects implements Repository.ListProjects (archived excluded).
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.query(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE archived_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list projects: %w", err)
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var (
			p         Project
			scopeJSON []byte
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &scopeJSON, &p.CreatedAt, &p.ArchivedAt); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan project: %w", err)
		}
		if err := json.Unmarshal(scopeJSON, &p.Scope); err != nil {
			return nil, fmt.Errorf("platform/postgres: decode project scope: %w", err)
		}
		p.Scope = normalizeScope(p.Scope)
		p.CreatedAt = p.CreatedAt.UTC()
		if p.ArchivedAt != nil {
			t := p.ArchivedAt.UTC()
			p.ArchivedAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject implements Repository.GetProject.
func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	return scanProjectRow(s.queryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = $1`, parsed))
}

// UpdateProject implements Repository.UpdateProject.
func (s *Store) UpdateProject(ctx context.Context, id string, upd ProjectUpdate) (*Project, error) {
	current, err := s.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	if upd.Name != nil {
		name := strings.TrimSpace(*upd.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: project name is required", ErrValidation)
		}
		current.Name = name
	}
	if upd.Scope != nil {
		if err := validateScope(*upd.Scope); err != nil {
			return nil, err
		}
		current.Scope = normalizeScope(*upd.Scope)
	}
	if upd.Archived != nil {
		if *upd.Archived {
			if current.ArchivedAt == nil {
				now := time.Now().UTC()
				current.ArchivedAt = &now
			}
		} else {
			current.ArchivedAt = nil
		}
	}
	scopeJSON, err := marshalJSON(current.Scope)
	if err != nil {
		return nil, err
	}
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	if _, err := s.exec(ctx, `
		UPDATE projects SET name = $2, scope = $3, archived_at = $4 WHERE id = $1`,
		parsed, current.Name, scopeJSON, current.ArchivedAt); err != nil {
		return nil, mapConstraintErr(err, "update project")
	}
	return s.GetProject(ctx, id)
}

// DeleteProject implements Repository.DeleteProject.
func (s *Store) DeleteProject(ctx context.Context, id string) error {
	current, err := s.GetProject(ctx, id)
	if err != nil {
		return err
	}
	if current.Slug == DefaultProjectSlug {
		return fmt.Errorf("%w: the default project cannot be deleted", ErrConflict)
	}
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	if _, err := s.exec(ctx, `DELETE FROM projects WHERE id = $1`, parsed); err != nil {
		return fmt.Errorf("platform/postgres: delete project: %w", err)
	}
	return nil
}

// ── tasks ────────────────────────────────────────────────────────────────────

// taskColumns is used only with taskObjectiveJoin (the task_objectives row
// also carries project_id), so every column is table-qualified: a bare
// project_id made every joined task query fail with SQLSTATE 42702
// (ambiguous column reference).
const taskColumns = `tasks.id, tasks.project_id, tasks.name, tasks.goal, tasks.instruction, tasks.context_paths, tasks.context, tasks.phase_policy, tasks.schedule, tasks.created_at, tasks.updated_at, tasks.archived_at`

// taskObjectiveJoin attaches the 1:1 objective extension row. LEFT JOIN keeps
// every task visible, including the ones without a structured objective.
const taskObjectiveJoin = ` LEFT JOIN task_objectives o ON o.task_id = tasks.id`

// taskObjectiveColumn is the extra projected column of the join.
const taskObjectiveColumn = `, o.objective`

func scanTaskRow(row pgx.Row) (*Task, error) {
	var (
		t                Task
		contextPathsJSON []byte
		contextJSON      []byte
		policyJSON       []byte
		scheduleJSON     []byte
		objectiveJSON    []byte
	)
	if err := row.Scan(&t.ID, &t.ProjectID, &t.Name, &t.Goal, &t.Instruction, &contextPathsJSON, &contextJSON,
		&policyJSON, &scheduleJSON, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt, &objectiveJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: scan task: %w", err)
	}
	hydrateTaskJSON(&t, contextPathsJSON, contextJSON, policyJSON, scheduleJSON)
	hydrateTaskObjective(&t, objectiveJSON)
	return &t, nil
}

// hydrateTaskObjective decodes the joined objective extension row. An absent
// row (or an undecodable payload) means "not structured" - never a partial
// objective.
func hydrateTaskObjective(t *Task, objectiveJSON []byte) {
	if len(objectiveJSON) == 0 {
		return
	}
	var obj Objective
	if err := json.Unmarshal(objectiveJSON, &obj); err != nil {
		return
	}
	t.Objective = &obj
}

func hydrateTaskJSON(t *Task, contextPathsJSON, contextJSON, policyJSON, scheduleJSON []byte) {
	t.Context = map[string]any{}
	if len(contextPathsJSON) > 0 {
		_ = json.Unmarshal(contextPathsJSON, &t.ContextPaths)
	}
	if len(contextJSON) > 0 {
		_ = json.Unmarshal(contextJSON, &t.Context)
	}
	t.CreatedAt = t.CreatedAt.UTC()
	if t.UpdatedAt != nil {
		ts := t.UpdatedAt.UTC()
		t.UpdatedAt = &ts
	}
	if t.ArchivedAt != nil {
		ts := t.ArchivedAt.UTC()
		t.ArchivedAt = &ts
	}
}

// CreateTask implements Repository.CreateTask (per-project context slug with
// -2/-3… conflict suffixes, mock rule).
func (s *Store) CreateTask(ctx context.Context, projectID string, in TaskInput) (*Task, error) {
	parsedProject, err := parseUUID(projectID)
	if err != nil {
		return nil, err
	}
	var archivedAt *time.Time
	if err := s.queryRow(ctx,
		`SELECT archived_at FROM projects WHERE id = $1`, parsedProject).Scan(&archivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: project %s not found", ErrNotFound, projectID)
		}
		return nil, fmt.Errorf("platform/postgres: load project: %w", err)
	}
	if archivedAt != nil {
		return nil, fmt.Errorf("%w: project %s is archived", ErrConflict, projectID)
	}
	return s.createTaskInternal(ctx, parsedProject, in, nil, false)
}

// createTaskInternal inserts a task with an optional context seed (legacy
// schedule id, import markers). Callers must have validated the project.
// With idempotent=true the insert carries ON CONFLICT DO NOTHING: a
// concurrent creator's row (uq_tasks_legacy_import / uq_tasks_legacy_schedule)
// makes this insert a no-op and the caller re-reads by its context key.
func (s *Store) createTaskInternal(ctx context.Context, projectID uuid.UUID, in TaskInput, contextSeed map[string]any, idempotent bool) (*Task, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, fmt.Errorf("%w: task name is required", ErrValidation)
	}
	objective, err := NormalizeObjective(in.Objective)
	if err != nil {
		return nil, err
	}
	in.Objective = objective
	siblingSlugs, err := s.siblingSlugs(ctx, projectID)
	if err != nil {
		return nil, err
	}
	// id is generated here (not at the INSERT below) so the ASCII-slug
	// fallback can reuse it: a non-ASCII task name falls back to the task id
	// instead of an empty slug, matching the mock contract.
	id := uuid.NewString()
	contextMap := map[string]any{}
	for k, v := range in.Context {
		contextMap[k] = v
	}
	for k, v := range contextSeed {
		contextMap[k] = v
	}
	if _, has := contextMap["slug"]; !has {
		contextMap["slug"] = uniqueSlug(slugify(in.Name, id), siblingSlugs)
	}
	contextJSON, err := marshalJSON(contextMap)
	if err != nil {
		return nil, err
	}
	contextPathsJSON, err := marshalJSON(normalizeContextPaths(in.ContextPaths))
	if err != nil {
		return nil, err
	}
	policyJSON, err := optionalJSON(in.PhasePolicy)
	if err != nil {
		return nil, err
	}
	scheduleJSON, err := optionalJSON(in.Schedule)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	stmt := `
		INSERT INTO tasks (id, project_id, name, goal, instruction, context_paths, context, phase_policy, schedule, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	if idempotent {
		stmt += ` ON CONFLICT DO NOTHING`
	}
	tag, err := s.exec(ctx, stmt,
		id, projectID, in.Name, in.Goal, in.Instruction, contextPathsJSON,
		contextJSON, policyJSON, scheduleJSON, now)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: create task: %w", err)
	}
	if idempotent && tag.RowsAffected() == 0 {
		return nil, nil // a concurrent creator won; caller re-reads by context key
	}
	createdID, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	if err := s.writeTaskObjective(ctx, createdID, projectID.String(), in.Objective); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) siblingSlugs(ctx context.Context, projectID uuid.UUID) (map[string]struct{}, error) {
	rows, err := s.query(ctx,
		`SELECT context->>'slug' FROM tasks WHERE project_id = $1 AND context->>'slug' IS NOT NULL`, projectID)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list task slugs: %w", err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan task slug: %w", err)
		}
		out[slug] = struct{}{}
	}
	return out, rows.Err()
}

func optionalJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return marshalJSON(v)
}

// ListTasks implements Repository.ListTasks (archived excluded).
func (s *Store) ListTasks(ctx context.Context, projectID string) ([]Task, error) {
	parsed, err := parseUUID(projectID)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx,
		`SELECT `+taskColumns+taskObjectiveColumn+` FROM tasks`+taskObjectiveJoin+`
		 WHERE tasks.project_id = $1 AND tasks.archived_at IS NULL
		 ORDER BY tasks.created_at, tasks.id`, parsed)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list tasks: %w", err)
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var (
			t                Task
			contextPathsJSON []byte
			contextJSON      []byte
			policyJSON       []byte
			scheduleJSON     []byte
			objectiveJSON    []byte
		)
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Name, &t.Goal, &t.Instruction, &contextPathsJSON, &contextJSON,
			&policyJSON, &scheduleJSON, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt, &objectiveJSON); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan task: %w", err)
		}
		hydrateTaskJSON(&t, contextPathsJSON, contextJSON, policyJSON, scheduleJSON)
		hydrateTaskObjective(&t, objectiveJSON)
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTask implements Repository.GetTask.
func (s *Store) GetTask(ctx context.Context, id string) (*Task, error) {
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	return scanTaskRow(s.queryRow(ctx,
		`SELECT `+taskColumns+taskObjectiveColumn+` FROM tasks`+taskObjectiveJoin+`
		 WHERE tasks.id = $1`, parsed))
}

// UpdateTask implements Repository.UpdateTask.
func (s *Store) UpdateTask(ctx context.Context, id string, upd TaskUpdate) (*Task, error) {
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if upd.Name != nil {
		name := strings.TrimSpace(*upd.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: task name is required", ErrValidation)
		}
		current.Name = name
	}
	if upd.Goal != nil {
		current.Goal = *upd.Goal
	}
	if upd.Instruction != nil {
		current.Instruction = *upd.Instruction
	}
	if upd.PhasePolicy != nil {
		current.PhasePolicy = upd.PhasePolicy
	}
	if upd.Schedule != nil {
		current.Schedule = upd.Schedule
	}
	if upd.ContextPaths != nil {
		current.ContextPaths = upd.ContextPaths
	}
	if upd.ClearObjective {
		current.Objective = nil
	}
	if upd.Objective != nil {
		objective, err := NormalizeObjective(upd.Objective)
		if err != nil {
			return nil, err
		}
		current.Objective = objective
	}
	if upd.Context != nil {
		if current.Context == nil {
			current.Context = map[string]any{}
		}
		for k, v := range upd.Context {
			current.Context[k] = v
		}
	}
	if upd.Archived != nil {
		if *upd.Archived {
			if current.ArchivedAt == nil {
				now := time.Now().UTC()
				current.ArchivedAt = &now
			}
		} else {
			current.ArchivedAt = nil
		}
	}
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	contextJSON, err := marshalJSON(current.Context)
	if err != nil {
		return nil, err
	}
	contextPathsJSON, err := marshalJSON(normalizeContextPaths(current.ContextPaths))
	if err != nil {
		return nil, err
	}
	policyJSON, err := optionalJSON(current.PhasePolicy)
	if err != nil {
		return nil, err
	}
	scheduleJSON, err := optionalJSON(current.Schedule)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	current.UpdatedAt = &now
	if _, err := s.exec(ctx, `
		UPDATE tasks SET name = $2, goal = $3, instruction = $4, context_paths = $5,
		                 context = $6, phase_policy = $7, schedule = $8,
		                 updated_at = $9, archived_at = $10
		WHERE id = $1`,
		parsed, current.Name, current.Goal, current.Instruction, contextPathsJSON,
		contextJSON, policyJSON, scheduleJSON, now, current.ArchivedAt); err != nil {
		return nil, fmt.Errorf("platform/postgres: update task: %w", err)
	}
	if err := s.writeTaskObjective(ctx, parsed, current.ProjectID, current.Objective); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, id)
}

// DeleteTask implements Repository.DeleteTask (runs cascade).
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	tag, err := s.exec(ctx, `DELETE FROM tasks WHERE id = $1`, parsed)
	if err != nil {
		return fmt.Errorf("platform/postgres: delete task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── runs (mount records) ─────────────────────────────────────────────────────

const runColumns = `id, task_id, legacy_scan_id, instance_id, mode, "trigger", title, pinned, archived, created_at`

func scanRunRow(row pgx.Row) (*Run, error) {
	var r Run
	if err := row.Scan(&r.ID, &r.TaskID, &r.LegacyScanID, &r.InstanceID, &r.Mode, &r.Trigger, &r.Title, &r.Pinned, &r.Archived, &r.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: scan run: %w", err)
	}
	r.CreatedAt = r.CreatedAt.UTC()
	return &r, nil
}

func scanRunRows(rows pgx.Rows) ([]Run, error) {
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.TaskID, &r.LegacyScanID, &r.InstanceID, &r.Mode, &r.Trigger, &r.Title, &r.Pinned, &r.Archived, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan run: %w", err)
		}
		r.CreatedAt = r.CreatedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateRun implements Repository.CreateRun (duplicate instance_id → conflict).
func (s *Store) CreateRun(ctx context.Context, taskID string, mount RunMount) (*Run, error) {
	parsedTask, err := parseUUID(taskID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(mount.InstanceID) == "" {
		return nil, fmt.Errorf("%w: run instance_id is required", ErrValidation)
	}
	id := uuid.NewString()
	_, err = s.exec(ctx, `
		INSERT INTO runs (id, task_id, legacy_scan_id, instance_id, mode, "trigger", title, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, parsedTask, mount.LegacyScanID, mount.InstanceID, mount.Mode, mount.Trigger,
		strings.TrimSpace(mount.Title), time.Now().UTC())
	if err != nil {
		return nil, mapConstraintErr(err, "mount run")
	}
	return s.getRun(ctx, id)
}

func (s *Store) getRun(ctx context.Context, id string) (*Run, error) {
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	return scanRunRow(s.queryRow(ctx,
		`SELECT `+runColumns+` FROM runs WHERE id = $1`, parsed))
}

// ListRuns implements Repository.ListRuns.
func (s *Store) ListRuns(ctx context.Context, taskID string, page, size int) ([]Run, int, error) {
	parsed, err := parseUUID(taskID)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.queryRow(ctx,
		`SELECT count(*) FROM runs WHERE task_id = $1`, parsed).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("platform/postgres: count runs: %w", err)
	}
	page, size = normalizePage(page, size)
	rows, err := s.query(ctx,
		`SELECT `+runColumns+` FROM runs WHERE task_id = $1
		 ORDER BY created_at DESC, id ASC LIMIT $2 OFFSET $3`,
		parsed, size, (page-1)*size)
	if err != nil {
		return nil, 0, fmt.Errorf("platform/postgres: list runs: %w", err)
	}
	items, err := scanRunRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListRunsByProject implements Repository.ListRunsByProject.
func (s *Store) ListRunsByProject(ctx context.Context, projectID string) ([]Run, error) {
	parsed, err := parseUUID(projectID)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `
		SELECT r.id, r.task_id, r.legacy_scan_id, r.instance_id, r.mode, r."trigger",
		       r.title, r.pinned, r.archived, r.created_at
		FROM runs r JOIN tasks t ON t.id = r.task_id
		WHERE t.project_id = $1
		ORDER BY r.created_at DESC, r.id ASC`, parsed)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list project runs: %w", err)
	}
	return scanRunRows(rows)
}

// AllRuns implements Repository.AllRuns.
func (s *Store) AllRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.query(ctx,
		`SELECT `+runColumns+` FROM runs ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list all runs: %w", err)
	}
	return scanRunRows(rows)
}

// DeleteRunsByScanIdentity implements Repository.DeleteRunsByScanIdentity.
// run_findings rows cascade through their run_id foreign key.
func (s *Store) DeleteRunsByScanIdentity(ctx context.Context, instanceID, legacyScanID string) (int, error) {
	instanceID = strings.TrimSpace(instanceID)
	legacyScanID = strings.TrimSpace(legacyScanID)
	if instanceID == "" && legacyScanID == "" {
		return 0, nil
	}
	tag, err := s.exec(ctx, `
		DELETE FROM runs
		WHERE ($1 <> '' AND instance_id = $1)
		   OR ($2 <> '' AND legacy_scan_id = $2)`, instanceID, legacyScanID)
	if err != nil {
		return 0, fmt.Errorf("platform/postgres: delete runs by scan identity: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RunsByInstanceID implements Repository.RunsByInstanceID.
func (s *Store) RunsByInstanceID(ctx context.Context, instanceID string) ([]Run, error) {
	rows, err := s.query(ctx,
		`SELECT `+runColumns+` FROM runs WHERE instance_id = $1 ORDER BY created_at DESC, id ASC`, instanceID)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: runs by instance: %w", err)
	}
	return scanRunRows(rows)
}

// SetRunLegacyScanID implements Repository.SetRunLegacyScanID. It only fills an
// empty legacy id: an imported run already points at its own scan and must keep
// it, and a re-dispatch of the same instance must not steal another scan's id.
func (s *Store) SetRunLegacyScanID(ctx context.Context, instanceID, legacyScanID string) error {
	instanceID = strings.TrimSpace(instanceID)
	legacyScanID = strings.TrimSpace(legacyScanID)
	if instanceID == "" || legacyScanID == "" {
		return fmt.Errorf("%w: instance_id and legacy_scan_id are required", ErrValidation)
	}
	tag, err := s.exec(ctx, `
		UPDATE runs SET legacy_scan_id = $2
		WHERE instance_id = $1 AND (legacy_scan_id IS NULL OR legacy_scan_id = '')`,
		instanceID, legacyScanID)
	if err != nil {
		return fmt.Errorf("platform/postgres: set run legacy scan id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the run does not exist, or it already carries a scan id.
		// Distinguish the two so a genuine wiring bug is not masked.
		var existing string
		err := s.queryRow(ctx, `SELECT legacy_scan_id FROM runs WHERE instance_id = $1`, instanceID).Scan(&existing)
		if err != nil {
			return fmt.Errorf("%w: run %s not found", ErrNotFound, instanceID)
		}
		return nil
	}
	return nil
}

// RenameRun implements Repository.RenameRun.
func (s *Store) RenameRun(ctx context.Context, instanceID, title string) error {
	return s.updateRunFlag(ctx, "title", instanceID, strings.TrimSpace(title))
}

// SetRunPinned implements Repository.SetRunPinned.
func (s *Store) SetRunPinned(ctx context.Context, instanceID string, pinned bool) error {
	return s.updateRunFlag(ctx, "pinned", instanceID, pinned)
}

// SetRunArchived implements Repository.SetRunArchived.
func (s *Store) SetRunArchived(ctx context.Context, instanceID string, archived bool) error {
	return s.updateRunFlag(ctx, "archived", instanceID, archived)
}

// updateRunFlag writes one conversation-metadata column on the run that owns
// instanceID. Column names are call-site constants (never user input), which is
// why they can be interpolated; values stay parameterised.
func (s *Store) updateRunFlag(ctx context.Context, column, instanceID string, value any) error {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return fmt.Errorf("%w: instance_id is required", ErrValidation)
	}
	tag, err := s.exec(ctx,
		`UPDATE runs SET `+column+` = $2 WHERE instance_id = $1`, instanceID, value)
	if err != nil {
		return fmt.Errorf("platform/postgres: update run %s: %w", column, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: run %s not found", ErrNotFound, instanceID)
	}
	return nil
}

// ProjectScanIDs implements Repository.ProjectScanIDs.
func (s *Store) ProjectScanIDs(ctx context.Context, projectID string) ([]string, error) {
	parsed, err := parseUUID(projectID)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `
		SELECT DISTINCT id FROM (
			SELECT r.instance_id AS id
			FROM runs r JOIN tasks t ON t.id = r.task_id
			WHERE t.project_id = $1 AND r.instance_id <> ''
			UNION
			SELECT r.legacy_scan_id AS id
			FROM runs r JOIN tasks t ON t.id = r.task_id
			WHERE t.project_id = $1 AND r.legacy_scan_id IS NOT NULL AND r.legacy_scan_id <> ''
		) ids
		ORDER BY id`, parsed)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: project scan ids: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("platform/postgres: project scan id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// InstanceIDsForProject implements Repository.InstanceIDsForProject.
func (s *Store) InstanceIDsForProject(ctx context.Context, projectID string) ([]string, error) {
	parsed, err := parseUUID(projectID)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `
		SELECT DISTINCT r.instance_id
		FROM runs r JOIN tasks t ON t.id = r.task_id
		WHERE t.project_id = $1 AND r.instance_id <> ''`, parsed)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: instance ids for project: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan instance id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ── legacy import ────────────────────────────────────────────────────────────

// LegacyScanIDs implements Repository.LegacyScanIDs.
func (s *Store) LegacyScanIDs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.query(ctx,
		`SELECT legacy_scan_id FROM runs
		 WHERE legacy_scan_id IS NOT NULL AND legacy_scan_id <> ''`)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: legacy scan ids: %w", err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("platform/postgres: scan legacy id: %w", err)
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

// ImportLegacyRun implements Repository.ImportLegacyRun (idempotent on either
// legacy_scan_id or instance_id; concurrent inserts fall back to a re-read).
func (s *Store) ImportLegacyRun(ctx context.Context, taskID string, mount RunMount) (*Run, bool, error) {
	parsedTask, err := parseUUID(taskID)
	if err != nil {
		return nil, false, err
	}
	if mount.LegacyScanID == "" {
		return nil, false, fmt.Errorf("%w: legacy_scan_id is required for import", ErrValidation)
	}
	if existing, err := s.runByIdentity(ctx, mount.LegacyScanID, mount.InstanceID); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	id := uuid.NewString()
	_, err = s.exec(ctx, `
		INSERT INTO runs (id, task_id, legacy_scan_id, instance_id, mode, "trigger", title, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, parsedTask, mount.LegacyScanID, mount.InstanceID, mount.Mode, mount.Trigger,
		strings.TrimSpace(mount.Title), time.Now().UTC())
	if err != nil {
		if existing, selErr := s.runByIdentity(ctx, mount.LegacyScanID, mount.InstanceID); selErr == nil {
			// A concurrent importer won the race — same idempotent outcome.
			return existing, false, nil
		}
		return nil, false, mapConstraintErr(err, "import legacy run")
	}
	run, err := s.getRun(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return run, true, nil
}

func (s *Store) runByIdentity(ctx context.Context, legacyScanID, instanceID string) (*Run, error) {
	return scanRunRow(s.queryRow(ctx, `
		SELECT `+runColumns+` FROM runs
		WHERE ($1 <> '' AND legacy_scan_id = $1)
		   OR ($2 <> '' AND instance_id = $2)
		ORDER BY created_at ASC, id ASC
		LIMIT 1`, legacyScanID, instanceID))
}

// EnsureImportTask implements Repository.EnsureImportTask.
func (s *Store) EnsureImportTask(ctx context.Context, projectID string) (*Task, error) {
	parsedProject, err := parseUUID(projectID)
	if err != nil {
		return nil, err
	}
	if t, err := s.taskByContextKey(ctx, parsedProject, "legacy_import", "scans"); err == nil {
		return t, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	siblingSlugs, err := s.siblingSlugs(ctx, parsedProject)
	if err != nil {
		return nil, err
	}
	created, err := s.createTaskInternal(ctx, parsedProject, TaskInput{Name: "导入扫描"}, map[string]any{
		"legacy_import": "scans",
		"slug":          uniqueSlug("imported-scans", siblingSlugs),
	}, true)
	if err != nil {
		// Two processes can race their first startup: the partial unique
		// index uq_tasks_legacy_import turned the loser's insert into a
		// no-op (or 23505 pre-ON-CONFLICT) — re-read returns the winner's
		// row, same idempotency contract as ImportLegacyRun.
		if existing, selErr := s.taskByContextKey(ctx, parsedProject, "legacy_import", "scans"); selErr == nil {
			return existing, nil
		}
		return nil, err
	}
	if created == nil {
		return s.taskByContextKey(ctx, parsedProject, "legacy_import", "scans")
	}
	return created, nil
}

// ImportScheduleTask implements Repository.ImportScheduleTask.
func (s *Store) ImportScheduleTask(ctx context.Context, projectID string, in TaskInput, legacyScheduleID string) (*Task, bool, error) {
	parsedProject, err := parseUUID(projectID)
	if err != nil {
		return nil, false, err
	}
	if t, err := s.taskByContextKey(ctx, parsedProject, "legacy_schedule_id", legacyScheduleID); err == nil {
		return t, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	created, err := s.createTaskInternal(ctx, parsedProject, in, map[string]any{"legacy_schedule_id": legacyScheduleID}, true)
	if err != nil {
		// Same first-start race as EnsureImportTask: the
		// uq_tasks_legacy_schedule index resolves it by re-read.
		if existing, selErr := s.taskByContextKey(ctx, parsedProject, "legacy_schedule_id", legacyScheduleID); selErr == nil {
			return existing, false, nil
		}
		return nil, false, err
	}
	if created == nil {
		existing, err := s.taskByContextKey(ctx, parsedProject, "legacy_schedule_id", legacyScheduleID)
		if err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	return created, true, nil
}

func (s *Store) taskByContextKey(ctx context.Context, projectID uuid.UUID, key, value string) (*Task, error) {
	return scanTaskRow(s.queryRow(ctx,
		`SELECT `+taskColumns+taskObjectiveColumn+` FROM tasks`+taskObjectiveJoin+`
		 WHERE tasks.project_id = $1 AND tasks.context->>$2 = $3
		 ORDER BY tasks.created_at, tasks.id LIMIT 1`, projectID, key, value))
}

// TaskByLegacyScheduleID implements Repository.TaskByLegacyScheduleID: one
// delegated context-key SELECT, miss → ErrNotFound (pgx.ErrNoRows mapped by
// scanTaskRow).
func (s *Store) TaskByLegacyScheduleID(ctx context.Context, projectID, legacyScheduleID string) (*Task, error) {
	parsed, err := parseUUID(projectID)
	if err != nil {
		return nil, err
	}
	return s.taskByContextKey(ctx, parsed, "legacy_schedule_id", legacyScheduleID)
}

// writeTaskObjective stores (or clears) the structured objective of a task.
// The objective is a 1:1 extension row; a nil objective deletes it, which is
// the honest "not structured" state.
func (s *Store) writeTaskObjective(ctx context.Context, taskID uuid.UUID, projectID string, obj *Objective) error {
	if obj == nil {
		if _, err := s.exec(ctx, `DELETE FROM task_objectives WHERE task_id = $1`, taskID); err != nil {
			return fmt.Errorf("platform/postgres: clear task objective: %w", err)
		}
		return nil
	}
	parsedProject, err := parseUUID(projectID)
	if err != nil {
		return err
	}
	payload, err := marshalJSON(obj)
	if err != nil {
		return err
	}
	if _, err := s.exec(ctx, `
		INSERT INTO task_objectives (task_id, project_id, objective, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (task_id) DO UPDATE
		SET project_id = EXCLUDED.project_id,
		    objective = EXCLUDED.objective,
		    updated_at = now()`,
		taskID, parsedProject, payload); err != nil {
		return fmt.Errorf("platform/postgres: write task objective: %w", err)
	}
	return nil
}
