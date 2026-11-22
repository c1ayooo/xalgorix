package platform

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Repository is the platform persistence seam (REDESIGN D6/D7). Two
// implementations exist: a PostgreSQL store (DSN configured) and a JSON
// snapshot file store (no-DSN degradation, REDESIGN §3.3).
//
// Isolation contract (D7): every list/filter method takes an explicit
// projectID and only ever returns rows of that project. Point lookups return
// rows whose ProjectID is part of the row, so handlers can re-check
// membership before serving cross-project references.
type Repository interface {
	// ── projects ────────────────────────────────────────────────────────────
	// EnsureDefaultProject returns the implicit default project, creating it
	// when missing (file mode: fixed ID "default"; PG mode: slug "default",
	// seeded ON CONFLICT DO NOTHING).
	EnsureDefaultProject(ctx context.Context) (*Project, error)
	CreateProject(ctx context.Context, name string, scope Scope) (*Project, error)
	ListProjects(ctx context.Context) ([]Project, error)
	GetProject(ctx context.Context, id string) (*Project, error)
	UpdateProject(ctx context.Context, id string, upd ProjectUpdate) (*Project, error)
	// DeleteProject removes a project and, via cascade, its tasks and runs.
	// The default project (fixed ID / slug "default") is refused with
	// ErrConflict. Active-run detection is a web-layer projection (run
	// status lives in scan.json); handlers must check it before calling.
	DeleteProject(ctx context.Context, id string) error

	// ── tasks ───────────────────────────────────────────────────────────────
	// CreateTask inserts a task under projectID. The derived slug (mock rule:
	// lowercase name → [a-z0-9-], conflict suffix -2/-3…) is unique within
	// the project and stored in tasks.context.slug for future URL use
	// (tasks have no first-class slug column in REDESIGN §3.2).
	CreateTask(ctx context.Context, projectID string, in TaskInput) (*Task, error)
	// ListTasks returns the non-archived tasks of exactly one project.
	ListTasks(ctx context.Context, projectID string) ([]Task, error)
	GetTask(ctx context.Context, id string) (*Task, error)
	UpdateTask(ctx context.Context, id string, upd TaskUpdate) (*Task, error)
	DeleteTask(ctx context.Context, id string) error

	// ── runs (mount records only) ───────────────────────────────────────────
	// CreateRun mounts a dispatch instance onto a task. A duplicate
	// instance_id returns ErrConflict.
	CreateRun(ctx context.Context, taskID string, mount RunMount) (*Run, error)
	// ListRuns returns (items, total) for one task, newest first. page/size
	// follow the API convention: page ≥ 1, size ≥ 1.
	ListRuns(ctx context.Context, taskID string, page, size int) ([]Run, int, error)
	// ListRunsByProject returns every mount record of a project (any task),
	// newest first. Used for delete-guards and projected statistics.
	ListRunsByProject(ctx context.Context, projectID string) ([]Run, error)
	// RunsByInstanceID returns the mount records pointing at an instance ID.
	RunsByInstanceID(ctx context.Context, instanceID string) ([]Run, error)
	// SetRunLegacyScanID records the scan-data identity a dispatched run is
	// executing (the scan-context id used by scan.json, ledger.json and the
	// domain journal). Runs are mounted before the scan directory exists, so
	// this is a second write rather than a CreateRun field. The call is
	// idempotent and never overwrites an existing legacy id (imported runs
	// already point at their own scan). Unknown instance ids return ErrNotFound.
	SetRunLegacyScanID(ctx context.Context, instanceID, legacyScanID string) error
	// RenameRun sets the conversation title shown in the chat sidebar.
	RenameRun(ctx context.Context, instanceID, title string) error
	// SetRunPinned pins/unpins a conversation to the top of its task group.
	SetRunPinned(ctx context.Context, instanceID string, pinned bool) error
	// SetRunArchived soft-deletes a conversation. Archived runs keep their
	// findings, journal and scan directory; only the sidebar hides them.
	SetRunArchived(ctx context.Context, instanceID string, archived bool) error
	// AllRuns returns every mount record. Used only by cold-path backfills
	// (findings projection); it is not an API list endpoint.
	AllRuns(ctx context.Context) ([]Run, error)
	// InstanceIDsForProject returns the instance IDs a project's runs are
	// mounted on — the web layer's join key into scan.json projections.
	InstanceIDsForProject(ctx context.Context, projectID string) ([]string, error)
	// ProjectScanIDs returns the union of instance_id and legacy_scan_id for
	// the project's runs. It is the project-scope join key for the InfoItem
	// cold path (info_items.scan_id), including legacy scans that have no
	// dispatch instance.
	ProjectScanIDs(ctx context.Context, projectID string) ([]string, error)
	// DeleteRunsByScanIdentity removes every mount record whose instance_id or
	// legacy_scan_id matches. Retention uses it after deleting a scan
	// directory; findings cascade with the run rows.
	DeleteRunsByScanIdentity(ctx context.Context, instanceID, legacyScanID string) (int, error)

	// ── legacy import (R1, REDESIGN §8) ─────────────────────────────────────
	// LegacyScanIDs returns the set of already-imported scan IDs so the
	// importer can skip without write attempts.
	LegacyScanIDs(ctx context.Context) (map[string]struct{}, error)
	// ImportLegacyRun idempotently mounts one existing scan.json record under
	// taskID: a run whose legacy_scan_id already exists is returned unchanged
	// with created=false, never duplicated.
	ImportLegacyRun(ctx context.Context, taskID string, mount RunMount) (*Run, bool, error)
	// EnsureImportTask returns the umbrella task that mounts imported legacy
	// scans ("导入扫描"), creating it once per project (idempotency marker in
	// tasks.context).
	EnsureImportTask(ctx context.Context, projectID string) (*Task, error)
	// ImportScheduleTask idempotently converts a legacy schedule into a task
	// keyed by context.legacy_schedule_id; returns (task, created).
	ImportScheduleTask(ctx context.Context, projectID string, in TaskInput, legacyScheduleID string) (*Task, bool, error)
	// TaskByLegacyScheduleID is the read-only reverse lookup of that mapping:
	// the task created from a legacy schedule within projectID, ErrNotFound
	// when none. The scheduler→run mount uses it to attach schedule-triggered
	// runs to the imported task; it never writes — ImportScheduleTask keeps
	// the implicit-create role.
	TaskByLegacyScheduleID(ctx context.Context, projectID, legacyScheduleID string) (*Task, error)

	// ── approvals (R2a HITL, REDESIGN §5, API_CONTRACT §3) ──────────────────
	// CreateApproval inserts a pending obligation row. The caller computes
	// deadline_at (created_at + configured timeout) — the repository is a
	// dumb record keeper.
	CreateApproval(ctx context.Context, in ApprovalInput) (*Approval, error)
	// ListApprovals returns approvals newest first. Filter semantics follow
	// API_CONTRACT §3: Status "pending"/"decided"/"all" ("" = all);
	// ProjectID/TaskID optional row filters. An empty ProjectID is the
	// deliberate cross-project operator view — the approval queue is the D7
	// exception (single cross-project data surface), and pending_total is
	// always computed across all projects via PendingSummary.
	ListApprovals(ctx context.Context, filter ApprovalFilter) ([]Approval, error)
	// GetApproval returns one approval by ID.
	GetApproval(ctx context.Context, id string) (*Approval, error)
	// DecideApproval transitions a pending row to decision.Decision
	// (approved/rejected/timeout/cancelled). A row that is not pending (or
	// missing) yields ErrNotFound / ErrConflict respectively — optimistic
	// concurrency: the first decision wins, the second gets 409.
	DecideApproval(ctx context.Context, id string, decision ApprovalDecision) (*Approval, error)
	// CountPendingByInstance reports the open slot usage of a run
	// (max_pending semantics, REDESIGN §5.3).
	CountPendingByInstance(ctx context.Context, instanceID string) (int, error)
	// CancelPendingByInstance closes every pending approval of an instance
	// (run stopped/paused/shutdown, REDESIGN §5.3) as status=cancelled with
	// decided_by="system" and the given reason. Returns the rows cancelled.
	CancelPendingByInstance(ctx context.Context, instanceID, reason string) ([]Approval, error)
	// PendingSummary returns the cross-project pending count and the nearest
	// pending deadline (OpsPulse, API_CONTRACT §3).
	PendingSummary(ctx context.Context) (ApprovalSummary, error)

	// ── approval rules (approve & remember, REDESIGN §5.2, API_CONTRACT §3.2) ─
	// CreateApprovalRule persists an operator-authored exemption consulted by
	// both gate layers before an approval row is created. Kind/match
	// coherence violations are ErrValidation.
	CreateApprovalRule(ctx context.Context, in ApprovalRuleInput) (*ApprovalRule, error)
	// ListApprovalRules returns rules newest first. Expired rows are
	// deliberately included: expiry is a match-time concern and the UI shows
	// the state (hit_count/last_hit_at are the operator's tuning evidence).
	ListApprovalRules(ctx context.Context, filter ApprovalRuleFilter) ([]ApprovalRule, error)
	// DeleteApprovalRule removes the rule; ErrNotFound when missing.
	DeleteApprovalRule(ctx context.Context, id string) error
	// RecordApprovalRuleHit increments hit_count and stamps last_hit_at. A
	// missing rule is not an error — hit bookkeeping is a best-effort audit
	// projection, the rule row is the authority.
	RecordApprovalRuleHit(ctx context.Context, id string) error

	// ── guidance (R2a HITL-intent, API_CONTRACT §3.1) ───────────────────────
	// CreateGuidance persists a human intent turn. Idempotent on
	// (task_id, request_id) when request_id is non-empty: a retry returns the
	// existing row with created=false, never a duplicate.
	CreateGuidance(ctx context.Context, in GuidanceInput) (*Guidance, bool, error)
	// ListGuidance returns the task's guidance turns newest first, up to
	// limit rows (limit ≤ 0 → 50).
	ListGuidance(ctx context.Context, taskID string, limit int) ([]Guidance, error)
	// UpdateGuidanceStatus flips a guidance row's status (queued→injected,
	// injected→consumed). Setting consumed stamps consumed_at.
	UpdateGuidanceStatus(ctx context.Context, id, status string) (*Guidance, error)

	// ── integrations: MCP servers / user skills / agent profiles ────────────
	// Operator-authored definitions consumed at scan launch. They are
	// configuration, never judgements (charter §3.5): the engine treats every
	// value as input, and a nil/empty set must leave scanning fully capable
	// (single-binary degradation, charter §6.6).
	//
	// CreateMcpServer persists one MCP server definition. A duplicate name is
	// ErrConflict; structural violations are ErrValidation. Env/Headers may
	// carry credential material — callers (web layer) mask on read and treat
	// "****"-prefixed submissions as unchanged; the repository persists
	// whatever it is given.
	CreateMcpServer(ctx context.Context, in McpServerInput) (*McpServer, error)
	// ListMcpServers returns every server newest first, enabled or not.
	ListMcpServers(ctx context.Context) ([]McpServer, error)
	GetMcpServer(ctx context.Context, id string) (*McpServer, error)
	// UpdateMcpServer applies the PATCH semantics of McpServerUpdate (nil =
	// unchanged). The refresh action uses the ToolCount/LastHealthyAt/
	// LastError fields to stamp connectivity projections.
	UpdateMcpServer(ctx context.Context, id string, upd McpServerUpdate) (*McpServer, error)
	DeleteMcpServer(ctx context.Context, id string) error

	// CreateSkill persists one user skill. A duplicate name is ErrConflict
	// (unique among user skills; the embedded corpus is a separate namespace).
	CreateSkill(ctx context.Context, in SkillInput) (*Skill, error)
	// ListSkills returns every user skill newest first, enabled or not.
	ListSkills(ctx context.Context) ([]Skill, error)
	GetSkill(ctx context.Context, id string) (*Skill, error)
	UpdateSkill(ctx context.Context, id string, upd SkillUpdate) (*Skill, error)
	DeleteSkill(ctx context.Context, id string) error

	// CreateAgentProfile persists one delegation profile. A duplicate role is
	// ErrConflict. Reserved built-in roles are rejected by the web layer (the
	// repository stays decoupled from the agent package).
	CreateAgentProfile(ctx context.Context, in AgentProfileInput) (*AgentProfile, error)
	// ListAgentProfiles returns every user profile newest first, enabled or
	// not. Built-in specialist profiles are merged by the web layer — they are
	// never stored rows.
	ListAgentProfiles(ctx context.Context) ([]AgentProfile, error)
	GetAgentProfile(ctx context.Context, id string) (*AgentProfile, error)
	UpdateAgentProfile(ctx context.Context, id string, upd AgentProfileUpdate) (*AgentProfile, error)
	DeleteAgentProfile(ctx context.Context, id string) error
}

// FindingsStore is the optional PostgreSQL-only findings projection seam. The
// file store deliberately does not implement it: without a DSN the web layer
// falls back to a full paginated scan.json projection (charter §6.6).
type FindingsStore interface {
	// ReplaceRunFindings atomically replaces one run's projected findings.
	// An empty slice clears stale rows while keeping the mount record.
	ReplaceRunFindings(ctx context.Context, runID string, scanStartedAt time.Time, findings []RunFinding) error
	// ListTaskFindings returns one newest row per dedup_key for a task.
	ListTaskFindings(ctx context.Context, taskID string) ([]RunFinding, error)
	// CountTaskFindings counts distinct dedup keys for a task.
	CountTaskFindings(ctx context.Context, taskID string) (int, error)
}

// slugPattern keeps the mock-backend slug shape: lowercase, [a-z0-9-], no
// leading/trailing dashes (webui/mock-backend.mjs project slug rule).
var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// slugify derives a slug from name following the mock-backend rule; an
// all-non-ASCII name falls back to fallbackID so the result is never empty.
func slugify(name, fallbackID string) string {
	slug := slugPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return fallbackID
	}
	return slug
}

// uniqueSlug appends the -2/-3… conflict suffix until slug is free among
// existing. Suffixes start at 2, matching the mock rule.
func uniqueSlug(base string, existing map[string]struct{}) string {
	if _, taken := existing[base]; !taken {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if _, taken := existing[candidate]; !taken {
			return candidate
		}
	}
}
