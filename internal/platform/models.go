// Package platform implements the R1 project/task/run model (REDESIGN D6/D7):
// Project → Task → Run with Run as a mount record over the existing hot-path
// scan artifacts (scanctx + scan.json stay the status authority, charter §5.1).
//
// PostgreSQL is the cold-path system of record when a DSN is configured; a
// file snapshot under <dataDir>/_platform/platform.json keeps the API usable
// in the no-DSN single-binary mode without reducing scan capability
// (REDESIGN §3.3, charter §6.6).
package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors. Handlers map them: ErrNotFound → 404, ErrConflict → 409,
// ErrValidation → 400.
var (
	// ErrNotFound is returned when a project, task or run does not exist.
	ErrNotFound = errors.New("platform: not found")
	// ErrConflict is returned for protected deletions (the implicit default
	// project) and uniqueness clashes (slug, runs.instance_id, runs.legacy_scan_id).
	ErrConflict = errors.New("platform: conflict")
	// ErrValidation is returned for malformed input the handlers did not
	// catch (empty name, unknown project for a task, ...).
	ErrValidation = errors.New("platform: invalid input")
)

// DefaultProjectID is the fixed ID of the implicit single project used in
// file mode (REDESIGN §3.3). It cannot be deleted.
const DefaultProjectID = "default"

// DefaultProjectSlug is the slug of the implicit default project. Both the
// file store and the PG seed use it; deletion of the project carrying it is
// refused so the platform never ends up without a project.
const DefaultProjectSlug = "default"

// Scope is the authorization boundary of a project (REDESIGN D7): the target
// list every task run must fall inside, plus the ports a target may name.
//
// Port semantics (documented in API_CONTRACT §1): scope entries name hosts
// only — an entry carrying its own ":port" is rejected at write time (ports
// are governed by allow_ports, never per-entry), and an empty allow_ports
// means ports are NOT restricted: any port on an in-scope host is allowed.
type Scope struct {
	Targets    []string `json:"targets"`
	AllowPorts []int    `json:"allow_ports,omitempty"`
}

// Project is the top-level authorization/ownership entity.
type Project struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Slug       string     `json:"slug"`
	Scope      Scope      `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	// TaskCount / ActiveRuns are projected by the web layer for list
	// responses (API_CONTRACT §1); the repository never fills them.
	TaskCount  int `json:"task_count"`
	ActiveRuns int `json:"active_runs"`
}

// PhasePolicy carries the task-level scan knobs (API_CONTRACT §2).
type PhasePolicy struct {
	Phases         []int    `json:"phases,omitempty"`
	SeverityFilter []string `json:"severity_filter,omitempty"`
	ScanMode       string   `json:"scan_mode,omitempty"`
	Intensity      string   `json:"intensity,omitempty"`
}

// TaskSchedule is the periodic-trigger attribute of a task (API_CONTRACT §2).
// Firing still belongs to the existing scheduler loop in this milestone; the
// attribute is stored for the R1 model only.
type TaskSchedule struct {
	Interval string `json:"interval"`
	Time     string `json:"time,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

// Task is a Goal + Context host whose runs accumulate into cold-path
// projections (REDESIGN §3.1). Context is free-form jsonb: the legacy
// importer parks no-contract fields (run day, report branding, webhook,
// model) and idempotency keys (legacy_schedule_id) there per §3.2. It is
// never serialized to the wire (API_CONTRACT Task has no such field).
type Task struct {
	ID           string         `json:"id"`
	ProjectID    string         `json:"project_id"`
	Name         string         `json:"name"`
	Goal         string         `json:"goal,omitempty"`
	Instruction  string         `json:"instruction,omitempty"`
	PhasePolicy  *PhasePolicy   `json:"phase_policy,omitempty"`
	Schedule     *TaskSchedule  `json:"schedule,omitempty"`
	ContextPaths []string       `json:"context_paths,omitempty"`
	Context      map[string]any `json:"-"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    *time.Time     `json:"updated_at,omitempty"`
	ArchivedAt   *time.Time     `json:"archived_at,omitempty"`
	// RunCount / LastRunAt / LastRunStatus / FindingCount are cold-path
	// projections assembled by the web layer from runs + scan.json
	// (API_CONTRACT §2); the repository never fills them. RunCount and
	// FindingCount are always present (mock-backend parity); the last-run
	// fields stay omitempty until the task has run.
	RunCount      int    `json:"run_count"`
	LastRunAt     string `json:"last_run_at,omitempty"`
	LastRunStatus string `json:"last_run_status,omitempty"`
	FindingCount  int    `json:"finding_count"`
}

// RunMount is the persisted portion of a Run: a pure mount record that ties
// a dispatch instance (or a legacy scan record) to its task. Status, timing,
// vuln counts and tokens live in scan.json and are projected at read time —
// the mount record must never become a second status authority (charter §5.1,
// REDESIGN §3.1).
type RunMount struct {
	LegacyScanID string // existing scan.json ID for imported runs; empty for dispatches
	InstanceID   string // /api/scan-style instance ID; also the scan.json instance_id
	Mode         string // scan_mode the run was launched with
	Trigger      string // manual | schedule | rerun; empty for legacy imports
}

// Run is the wire shape of a mount record plus the projected fields the web
// layer fills from findAllScanSummaries. Only the mount fields plus identity
// are persisted.
type Run struct {
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	LegacyScanID string    `json:"legacy_scan_id,omitempty"`
	InstanceID   string    `json:"instance_id,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	Trigger      string    `json:"trigger,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`

	// Projected at read time (never stored):
	Status      string `json:"status,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	VulnCount   int    `json:"vuln_count"`
	TotalTokens int    `json:"total_tokens"`
}

// Mount returns the persisted subset of the run.
func (r Run) Mount() RunMount {
	return RunMount{LegacyScanID: r.LegacyScanID, InstanceID: r.InstanceID, Mode: r.Mode, Trigger: r.Trigger}
}

// RunFinding is one projected scan.json finding attached to a Run. DedupKey is
// computed by the web layer with the existing (target, endpoint, title,
// severity) rule; Finding is the FlatFinding JSON payload served by the API.
// These rows are a cold-path replica only: report acceptance and verification
// remain authoritative in the scan pipeline (charter §3.5).
type RunFinding struct {
	DedupKey      string
	ScanStartedAt time.Time
	Finding       json.RawMessage
}

// ── approvals (R2a HITL, REDESIGN §5 / D5, API_CONTRACT §3) ─────────────────

// Approval lifecycle statuses (REDESIGN §5.3 state machine). Timeout is
// written server-side (timeout = rejection); Cancelled closes every pending
// row of a run when the run stops.
const (
	ApprovalPending   = "pending"
	ApprovalApproved  = "approved"
	ApprovalRejected  = "rejected"
	ApprovalTimeout   = "timeout"
	ApprovalCancelled = "cancelled"
)

// Approval kinds (REDESIGN §5.1): command = terminal/python/browser dangerous
// operations not on the read-only recon allowlist; http = write-method
// requests with a high-risk feature.
const (
	ApprovalKindCommand = "command"
	ApprovalKindHTTP    = "http"
)

// ApprovalPayload is the kind-specific display material of an approval row
// (API_CONTRACT §3). Secret-class header values are masked by the web layer
// before persistence (masks.go redaction policy — the API never returns
// credential material) and the body excerpt is truncated to 2 KiB.
type ApprovalPayload struct {
	// kind=command
	Command string `json:"command,omitempty"`
	Workdir string `json:"workdir,omitempty"`
	// kind=http
	Method      string            `json:"method,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	BodyExcerpt string            `json:"body_excerpt,omitempty"`
	// Reason names the rule/feature that triggered the assessment
	// ("read-only recon allowlist", "admin panel path", ...).
	Reason string `json:"reason,omitempty"`
}

// ApprovalFact is one slice entry (charter §4: the approval row is a consumer
// of the context slice). R2a rows carry no facts yet — the slice matures with
// the M4 activation entries; the shape is fixed now so R2b consumers do not
// break the wire contract.
type ApprovalFact struct {
	Content    string  `json:"content"`
	Kind       string  `json:"kind"`
	Confidence float64 `json:"confidence,omitempty"`
}

// ApprovalSlice is the context-slice projection attached to an approval row
// (API_CONTRACT §3).
type ApprovalSlice struct {
	Subject            string         `json:"subject,omitempty"`
	Purpose            string         `json:"purpose,omitempty"`
	Facts              []ApprovalFact `json:"facts,omitempty"`
	ContradictionCount int            `json:"contradiction_count,omitempty"`
}

// Approval is one HITL obligation. ProjectName / TaskName are web-layer
// projections for the cross-project OpsPulse view (D7 exception) and are
// never persisted by the repository.
type Approval struct {
	ID         string          `json:"id"`
	ProjectID  string          `json:"project_id"`
	TaskID     string          `json:"task_id,omitempty"`
	RunID      string          `json:"run_id,omitempty"`
	InstanceID string          `json:"instance_id,omitempty"`
	Kind       string          `json:"kind"`
	Payload    ApprovalPayload `json:"payload"`
	Slice      *ApprovalSlice  `json:"slice,omitempty"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	DeadlineAt time.Time       `json:"deadline_at"`
	DecidedAt  *time.Time      `json:"decided_at,omitempty"`
	DecidedBy  string          `json:"decided_by,omitempty"`
	Reason     string          `json:"reason,omitempty"`

	// Projected by the web layer (never stored):
	ProjectName string `json:"project_name,omitempty"`
	TaskName    string `json:"task_name,omitempty"`
}

// ApprovalInput is the gate service's creation request. DeadlineAt is computed
// by the caller (created_at + configured timeout) so the repository stays a
// dumb record keeper.
type ApprovalInput struct {
	ProjectID  string
	TaskID     string
	RunID      string
	InstanceID string
	Kind       string
	Payload    ApprovalPayload
	Slice      *ApprovalSlice
	DeadlineAt time.Time
}

// decidedByOperator marks operator decisions (REST decision endpoints); the
// gate service writes "timeout" / "system" itself.
const decidedByOperator = "operator"

// ApprovalDecision is the persist request for a pending approval. Decision
// must be one of approved/rejected/timeout/cancelled; anything else is
// ErrValidation. Only pending rows transition — a second decision returns
// ErrConflict (the REST surface maps it to 409, API_CONTRACT §3).
type ApprovalDecision struct {
	Decision  string
	DecidedBy string
	Reason    string
}

// ApprovalFilter selects rows for ListApprovals. Status uses the API
// vocabulary: "pending", "decided" (anything non-pending), "all" (or empty).
// ProjectID/TaskID are optional row-level filters; ProjectID empty means the
// cross-project operator view (D7 exception: the approval queue is the one
// operator-facing surface that spans projects).
type ApprovalFilter struct {
	ProjectID string
	TaskID    string
	Status    string
	Page      int
	Size      int
}

// ApprovalSummary is GET /api/approvals/summary payload material: the
// cross-project pending count plus the nearest pending deadline (OpsPulse,
// API_CONTRACT §3).
type ApprovalSummary struct {
	Total           int
	NearestDeadline *time.Time
}

// ── guidance (R2a HITL-intent, API_CONTRACT §3.1) ───────────────────────────

// Guidance lifecycle statuses: injected = handed to a running agent's next
// conversation round (the injected turn carries a stable `gi:<id>` marker);
// queued = no active run, merged into the next session's briefing (stays
// injected — the merged briefing has no per-row marker carrier, so those rows
// are never auto-flipped); consumed = the engine consumed it: the web-layer
// domain-journal observer flips the row when a hypothesis/evidence record's
// text cites the marker. UpdateGuidanceStatus remains the only status write
// path (guidance_observer.go is its sole consumed caller).
const (
	GuidanceInjected = "injected"
	GuidanceQueued   = "queued"
	GuidanceConsumed = "consumed"
)

// Guidance is one human-in-the-loop intent turn. RequestID is the client's
// idempotency key: persisted, but never serialized to the API wire (the mock
// backend strips it too).
type Guidance struct {
	ID         string     `json:"id"`
	TaskID     string     `json:"task_id"`
	RunID      string     `json:"run_id,omitempty"`
	InstanceID string     `json:"instance_id,omitempty"`
	Message    string     `json:"message"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`

	// Idempotency key — persisted, never on the wire:
	RequestID string `json:"-"`
}

// GuidanceInput is the POST /api/tasks/{id}/guidance creation request.
type GuidanceInput struct {
	TaskID     string
	RunID      string
	InstanceID string
	Message    string
	RequestID  string
}

// ProjectUpdate is the PATCH body for a project (API_CONTRACT §1). Nil
// pointer fields are left unchanged; the Archived three-state bool
// distinguishes "leave as-is" from explicit archive/unarchive.
type ProjectUpdate struct {
	Name     *string
	Scope    *Scope
	Archived *bool
}

// TaskInput is the POST body for task creation (API_CONTRACT §2).
type TaskInput struct {
	Name         string
	Goal         string
	Instruction  string
	PhasePolicy  *PhasePolicy
	Schedule     *TaskSchedule
	ContextPaths []string
	Context      map[string]any
}

// TaskUpdate is the PATCH body for a task (API_CONTRACT §2).
type TaskUpdate struct {
	Name         *string
	Goal         *string
	Instruction  *string
	PhasePolicy  *PhasePolicy // non-nil replaces the policy; nil leaves it
	Schedule     *TaskSchedule
	ContextPaths []string
	Context      map[string]any
	Archived     *bool
}

// normalizeContextPaths trims, drops empties and de-duplicates while keeping
// the caller's order. Task context paths are copied to each Run, so a stable
// normalized value avoids duplicate parsing work.
func normalizeContextPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

// normalizeScope keeps scopes JSON-stable (no nil arrays) so file snapshots
// and PG jsonb round-trip identically.
func normalizeScope(s Scope) Scope {
	if s.Targets == nil {
		s.Targets = []string{}
	}
	return s
}

// validateScope rejects scope entries that carry their own port ("host:port",
// "scheme://host:port"). Ports are controlled exclusively by allow_ports —
// allowing a per-entry port would make the port policy fail-open (a run could
// reach an unlisted port before allow_ports is ever consulted), so this is a
// deliberate fail-closed 400 at write time. Bare IPv6 literals (two or more
// colons, no brackets) are not port-carrying and pass; a bracketed "[v6]"
// host with a trailing ":port" is caught.
func validateScope(s Scope) error {
	for _, entry := range s.Targets {
		if scopeEntryHasExplicitPort(entry) {
			return fmt.Errorf("%w: scope target %q carries a port; list hosts only and control ports via allow_ports", ErrValidation, entry)
		}
	}
	return nil
}

// scopeEntryHasExplicitPort reports whether a scope entry names a port.
// Heuristic after stripping scheme/path: exactly one ":" means host:port;
// a bracketed IPv6 with a trailing ":port" is caught via the "]" rule.
func scopeEntryHasExplicitPort(entry string) bool {
	t := strings.TrimSpace(entry)
	if t == "" {
		return false
	}
	if idx := strings.Index(t, "://"); idx >= 0 {
		t = t[idx+3:]
	}
	if idx := strings.IndexAny(t, "/?#"); idx >= 0 {
		t = t[:idx]
	}
	if end := strings.LastIndex(t, "]"); end >= 0 {
		return strings.Contains(t[end:], ":")
	}
	return strings.Count(t, ":") == 1
}

// marshalJSON compactly encodes v; repositories store JSONB columns with it.
func marshalJSON(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return data, nil
}
