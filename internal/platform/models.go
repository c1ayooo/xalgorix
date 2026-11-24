// Package platform implements the R1 project/task/run model (REDESIGN D6/D7):
// Project → Task → Run with Run as a mount record over the run's scan
// records. Since R6.1 the record status is projected from the durable runs
// scan set (run_scan_records — REDESIGN D12, charter §5.1 v3.0); the mount
// record itself never becomes a second status authority.
//
// PostgreSQL is the cold-path system of record when a DSN is configured; a
// file snapshot under <dataDir>/_platform/platform.json keeps the API usable
// in tests and drives the R6.0 migration importer (REDESIGN §3.3).
package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
	// Fence is the runtime (HITL) enforcement layer of the same boundary:
	// Targets/AllowPorts decide which destinations a run may NAME at dispatch
	// time, Fence decides what an approval-gated call may TOUCH while the run
	// is live (REDESIGN §5 × D7). nil / enforce=false leaves the approval gate
	// exactly as it was before the fence existed.
	Fence *Fence `json:"fence,omitempty"`
}

// Fence is the operator-configured constraint the HITL approval gate applies
// to every regulated call (command + high-risk HTTP + proxy-attributed wire
// traffic) before the operator is even asked:
//
//	command — the call's literal destinations (URL host / host:port / IPv4 /
//	          dotted hostname, minus common file suffixes) must fall inside
//	          Targets, outside DenyTargets, and match DenyCommandPatterns none.
//	http    — the request method must be listed in AllowHTTPMethods (empty =
//	          all write methods) and the URL host:port must fall inside the
//	          same host/port boundary.
//
// A call that violates the fence is never offered to the operator for a
// decision: it is closed as rejected with decided_by="fence" so the audit row
// still exists while the authorization boundary holds (charter §3.5 field
// authority: the project fence outranks an operator's per-call approval).
type Fence struct {
	// Enforce turns the runtime check on. Requires a non-empty Targets list —
	// an enforced fence with nothing inside it would block every call.
	Enforce bool `json:"enforce"`
	// DenyTargets are explicit exclusions that override Targets (third-party
	// SaaS, shared infrastructure, subdomains that must never be touched).
	// Entries are hosts or "*.domain" wildcards, never port-carrying.
	DenyTargets []string `json:"deny_targets,omitempty"`
	// AllowHTTPMethods limits which write methods an approval may carry
	// (upper-case, from the standard HTTP set). Empty = all methods.
	AllowHTTPMethods []string `json:"allow_http_methods,omitempty"`
	// DenyCommandPatterns are case-insensitive substrings that hard-stop a
	// command-class call (e.g. "msfconsole", "sudo", "--os-shell").
	DenyCommandPatterns []string `json:"deny_command_patterns,omitempty"`
}

// Fence limits. They bound the prompt/UI surface and the per-call scanning
// cost, not the repository's ability to store what it is given.
const (
	MaxFenceDenyTargets         = 64
	MaxFenceDenyCommandPatterns = 32
	MaxFencePatternLength       = 200
)

// FenceHTTPMethods is the closed vocabulary AllowHTTPMethods draws from.
var FenceHTTPMethods = []string{
	"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE",
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

// ── objective (structured task goal) ─────────────────────────────────────────
//
// Objective is the platform-level structured goal: what the operator wants
// achieved and what "achieved" means. It is configuration, never a second
// judgement authority — the evaluator in internal/objective only observes
// findings the report pipeline already accepted (charter §3.5 / §9).
//
// An absent objective means "not structured": the UI must say so instead of
// inventing attainment (REDESIGN §3.1 "验收说明").

// Objective kinds.
const (
	ObjectiveKindEngagement   = "engagement"
	ObjectiveKindCapability   = "capability"
	ObjectiveKindFindingClass = "finding_class"
	ObjectiveKindCoverage     = "coverage"
)

// Criterion kinds.
const (
	CriterionKindFindingClass = "finding_class"
	CriterionKindCapability   = "capability"
	CriterionKindCoverage     = "coverage"
)

// Verification modes a criterion accepts.
const (
	CriterionVerifyDeterministic = "deterministic"
	CriterionVerifyVerifier      = "verifier"
	CriterionVerifyManual        = "manual"
)

// MaxObjectiveCriteria bounds the criteria list so one task cannot turn into
// an unbounded checklist (and so the prompt section stays readable).
const MaxObjectiveCriteria = 12

// MaxObjectiveConstraints bounds the operator-stated constraint lines that
// NormalizeObjective keeps (advisory narrowing rules rendered into the
// objective prompt section — an uncapped list would bloat every run prompt).
const MaxObjectiveConstraints = 8

// Objective is the structured goal of a task.
type Objective struct {
	Kind      string      `json:"kind"`
	Statement string      `json:"statement,omitempty"`
	Criteria  []Criterion `json:"criteria,omitempty"`
	// Constraints are operator-stated operational boundaries (may/may-not
	// rules extracted from the goal text, e.g. "no brute forcing"). They only
	// ever NARROW what the engine may do — they never widen scope or relax a
	// methodology phase — and render into the objective prompt section.
	Constraints []string `json:"constraints,omitempty"`
}

// Criterion is one decidable acceptance line of an objective.
type Criterion struct {
	// ID is assigned by the server (C1, C2, …) so evidence references are
	// stable across reads; clients may send it but never choose it.
	ID         string `json:"id,omitempty"`
	Kind       string `json:"kind"`
	VulnClass  string `json:"vuln_class,omitempty"`
	Capability string `json:"capability,omitempty"`
	VerifyBy   string `json:"verify_by"`
	Required   bool   `json:"required,omitempty"`
}

// NormalizeObjective validates the objective and returns a normalized copy:
// trimmed fields, canonical kind values and server-assigned criterion IDs.
// A nil objective is valid and normalizes to nil ("not structured").
func NormalizeObjective(obj *Objective) (*Objective, error) {
	if obj == nil {
		return nil, nil
	}
	out := &Objective{
		Kind:      strings.TrimSpace(obj.Kind),
		Statement: strings.TrimSpace(obj.Statement),
	}
	for _, c := range obj.Constraints {
		if s := strings.TrimSpace(c); s != "" {
			out.Constraints = append(out.Constraints, s)
		}
		if len(out.Constraints) == MaxObjectiveConstraints {
			break // advisory lines: cap silently, never reject the objective
		}
	}
	switch out.Kind {
	case ObjectiveKindEngagement, ObjectiveKindCapability, ObjectiveKindFindingClass, ObjectiveKindCoverage:
	case "":
		return nil, fmt.Errorf("%w: objective kind is required", ErrValidation)
	default:
		return nil, fmt.Errorf("%w: objective kind must be engagement, capability, finding_class or coverage", ErrValidation)
	}
	if out.Statement == "" {
		return nil, fmt.Errorf("%w: objective statement is required", ErrValidation)
	}
	if len(obj.Criteria) > MaxObjectiveCriteria {
		return nil, fmt.Errorf("%w: objective supports at most %d criteria", ErrValidation, MaxObjectiveCriteria)
	}
	for i := range obj.Criteria {
		c := obj.Criteria[i]
		out.Criteria = append(out.Criteria, Criterion{
			ID:         fmt.Sprintf("C%d", i+1),
			Kind:       strings.TrimSpace(c.Kind),
			VulnClass:  strings.TrimSpace(c.VulnClass),
			Capability: strings.TrimSpace(c.Capability),
			VerifyBy:   strings.TrimSpace(c.VerifyBy),
			Required:   c.Required,
		})
	}
	for i, c := range out.Criteria {
		switch c.Kind {
		case CriterionKindFindingClass:
			if c.VulnClass == "" {
				return nil, fmt.Errorf("%w: criterion %s requires vuln_class", ErrValidation, c.ID)
			}
		case CriterionKindCapability:
			if c.Capability == "" {
				return nil, fmt.Errorf("%w: criterion %s requires capability", ErrValidation, c.ID)
			}
		case CriterionKindCoverage:
		case "":
			return nil, fmt.Errorf("%w: criterion %s requires kind", ErrValidation, c.ID)
		default:
			return nil, fmt.Errorf("%w: criterion %s kind must be finding_class, capability or coverage", ErrValidation, c.ID)
		}
		switch c.VerifyBy {
		case CriterionVerifyDeterministic, CriterionVerifyVerifier, CriterionVerifyManual:
		case "":
			return nil, fmt.Errorf("%w: criterion %s requires verify_by", ErrValidation, c.ID)
		default:
			return nil, fmt.Errorf("%w: criterion %s verify_by must be deterministic, verifier or manual", ErrValidation, c.ID)
		}
		_ = i
	}
	return out, nil
}

// Task is a Goal + Context host whose runs accumulate into cold-path
// projections (REDESIGN §3.1). Context is free-form jsonb: the legacy
// importer parks no-contract fields (run day, report branding, webhook,
// model) and idempotency keys (legacy_schedule_id) there per §3.2. It is
// never serialized to the wire (API_CONTRACT Task has no such field).
type Task struct {
	ID           string        `json:"id"`
	ProjectID    string        `json:"project_id"`
	Name         string        `json:"name"`
	Goal         string        `json:"goal,omitempty"`
	Instruction  string        `json:"instruction,omitempty"`
	PhasePolicy  *PhasePolicy  `json:"phase_policy,omitempty"`
	Schedule     *TaskSchedule `json:"schedule,omitempty"`
	ContextPaths []string      `json:"context_paths,omitempty"`
	// AuditMode is the task-level HITL/audit switch:
	//   inherit — follow the process default (XALGORIX_APPROVAL_MODE);
	//   off     — observe-only for this task's runs (no approval rows);
	//   enforce — every regulated call (command + high-risk HTTP through the
	//             MITM proxy) creates an approval row and blocks until the
	//             operator decides.
	// The effective value is snapshotted onto the Run's ScanContext at session
	// start, so changing it affects the next run, not an in-flight one.
	AuditMode string `json:"audit_mode"`
	// Objective is the structured goal: absent = "not structured". It never
	// replaces Goal (free text) and never grants authority over findings.
	Objective *Objective `json:"objective,omitempty"`
	// LLMChain is the task's ordered model chain — "<provider>:<profileId>"
	// keys, highest priority first (4zreco task_llm_profiles port). The run
	// starts on ActiveLLM; the chain is operator intent, so it never overrides
	// an explicit per-run provider_profile. Persisted in the task context
	// (llm_chain / llm_active) and hydrated by the repository.
	LLMChain  []string `json:"llm_chain,omitempty"`
	ActiveLLM string   `json:"active_llm,omitempty"`
	// Proxy pins the run's outbound proxy: "" = follow the global pool,
	// "direct" = explicit direct egress, "N" = 1-based entry of the global
	// pool at run start. Persisted in the task context ("proxy"). A direct
	// pin is rejected when the process runs proxy-required.
	Proxy      string         `json:"proxy,omitempty"`
	Context    map[string]any `json:"-"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  *time.Time     `json:"updated_at,omitempty"`
	ArchivedAt *time.Time     `json:"archived_at,omitempty"`
	// RunCount / LastRunAt / LastRunStatus / LastRunCompletion / FindingCount
	// are cold-path projections assembled by the web layer from runs +
	// scan.json (API_CONTRACT §2); the repository never fills them. RunCount
	// and FindingCount are always present (mock-backend parity); the last-run
	// fields stay omitempty until the task has run.
	RunCount      int    `json:"run_count"`
	LastRunAt     string `json:"last_run_at,omitempty"`
	LastRunStatus string `json:"last_run_status,omitempty"`
	// LastRunCompletion is the last run's honest terminal assessment:
	// "full" (the run walked its graph frontier to an accepted finish) or
	// "partial" (bounded/physical stop — the intent was not achieved). There
	// is no "failed" reading: a settled run is either complete or unmet.
	LastRunCompletion string `json:"last_run_completion,omitempty"`
	FindingCount      int    `json:"finding_count"`
	// ContentChangedSinceRun answers "may the operator start this task
	// again?". It compares the runnable-content revision (goal/instruction/
	// objective/phase policy/context files/model chain) with the revision the
	// latest dispatch consumed. false = that exact content already ran, so the
	// UI must not offer a plain run; editing the content flips it back to true.
	// Nil means "not projected" (write responses), never "unchanged".
	ContentChangedSinceRun *bool `json:"content_changed_since_run,omitempty"`
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
	Title        string // conversation title shown in the chat sidebar; empty falls back to the task name
}

// Run is the wire shape of a mount record plus the projected fields the web
// layer fills from findAllScanSummaries. Only the mount fields plus identity
// are persisted.
type Run struct {
	ID           string `json:"id"`
	TaskID       string `json:"task_id"`
	LegacyScanID string `json:"legacy_scan_id,omitempty"`
	InstanceID   string `json:"instance_id,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Trigger      string `json:"trigger,omitempty"`
	// Conversation metadata (chat sidebar). Persisted, unlike the projected
	// fields below: a run is the conversation host, so its title/pin/archive
	// state must survive restarts.
	Title     string    `json:"title,omitempty"`
	Pinned    bool      `json:"pinned"`
	Archived  bool      `json:"archived"`
	CreatedAt time.Time `json:"created_at,omitempty"`

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

// ── approval rules (approve & remember, REDESIGN §5.2, API_CONTRACT §3.2) ────

// ApprovalRuleMatch carries a rule's matching coordinates. Command rules use
// {program, contains}; HTTP rules use {method, host, path_prefix, contains}.
// The matching semantics live in internal/approval (Policy.MatchCommandRule /
// MatchHTTPRule) so both gate layers reuse them verbatim.
type ApprovalRuleMatch struct {
	// kind=command: program basename ("curl" matches /usr/bin/curl too).
	Program string `json:"program,omitempty"`
	// kind=http
	Method     string `json:"method,omitempty"`      // exact, case-insensitive
	Host       string `json:"host,omitempty"`        // exact or subdomain suffix
	PathPrefix string `json:"path_prefix,omitempty"` // "" = any path
	// Shared narrowing substring — command text (command rules) or URL+body
	// (HTTP rules), case-insensitive. "" = unrestricted.
	Contains string `json:"contains,omitempty"`
}

// Approval rule decisions (scope observer, v4.6). "allow" is the historical
// "approve & remember" exemption; "deny" is the inverse — the scope observer
// auto-rejects a call matching the rule (bulk data deletion, uploads, web/OSS
// control-plane writes on the bound project). Empty is normalized to "allow"
// so every pre-existing rule keeps its meaning.
const (
	ApprovalRuleAllow = "allow"
	ApprovalRuleDeny  = "deny"
)

// NormalizeApprovalRuleDecision maps the wire/DB value onto allow|deny.
func NormalizeApprovalRuleDecision(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), ApprovalRuleDeny) {
		return ApprovalRuleDeny
	}
	return ApprovalRuleAllow
}

// ApprovalRule is one operator-authored rule: the persistent half of the
// "approve & remember" loop and the scope observer's decision table. Both gate
// layers consult the rules before creating an approval row, so the same intent
// is asked at most once. CreatedFromApprovalID traces the decision the rule was
// remembered from — the approval row itself may be retention-deleted while the
// rule survives (ON DELETE SET NULL); empty for hand-authored rules.
type ApprovalRule struct {
	ID                    string            `json:"id"`
	ProjectID             string            `json:"project_id"`
	Kind                  string            `json:"kind"`               // ApprovalKindCommand / ApprovalKindHTTP
	Decision              string            `json:"decision,omitempty"` // allow|deny (empty = allow)
	Match                 ApprovalRuleMatch `json:"match"`
	Reason                string            `json:"reason,omitempty"`
	CreatedBy             string            `json:"created_by,omitempty"` // "operator:<user>"
	CreatedFromApprovalID string            `json:"created_from_approval_id,omitempty"`
	CreatedAt             time.Time         `json:"created_at"`
	ExpiresAt             *time.Time        `json:"expires_at,omitempty"`
	HitCount              int               `json:"hit_count"`
	LastHitAt             *time.Time        `json:"last_hit_at,omitempty"`

	// Projected by the web layer (never stored):
	ProjectName string `json:"project_name,omitempty"`
}

// DecisionOrAllow returns the rule's effective decision.
func (r ApprovalRule) DecisionOrAllow() string {
	return NormalizeApprovalRuleDecision(r.Decision)
}

// ApprovalRuleInput is the create request (REST POST /api/approval-rules and
// the approval card's "approve & remember" path).
type ApprovalRuleInput struct {
	ProjectID             string
	Kind                  string
	Decision              string
	Match                 ApprovalRuleMatch
	Reason                string
	CreatedBy             string
	CreatedFromApprovalID string
	ExpiresAt             *time.Time
}

// ApprovalRuleFilter selects rules for ListApprovalRules. ProjectID empty is
// the cross-project operator view (same D7 exception as the approval queue);
// Kind optional.
type ApprovalRuleFilter struct {
	ProjectID string
	Kind      string
}

// validateApprovalRule checks kind/match coherence (shared by both stores).
func validateApprovalRule(in ApprovalRuleInput) error {
	if in.Kind != ApprovalKindCommand && in.Kind != ApprovalKindHTTP {
		return fmt.Errorf("%w: approval rule kind must be command or http", ErrValidation)
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		return fmt.Errorf("%w: approval rule project_id is required", ErrValidation)
	}
	switch in.Kind {
	case ApprovalKindCommand:
		if strings.TrimSpace(in.Match.Program) == "" {
			return fmt.Errorf("%w: command rule requires match.program", ErrValidation)
		}
	case ApprovalKindHTTP:
		if strings.TrimSpace(in.Match.Method) == "" || strings.TrimSpace(in.Match.Host) == "" {
			return fmt.Errorf("%w: http rule requires match.method and match.host", ErrValidation)
		}
	}
	return nil
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
	CreatedBy  string     `json:"created_by,omitempty"`
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
	CreatedBy  string
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
// Task audit modes: the task-level HITL switch.
const (
	TaskAuditInherit = "inherit"
	TaskAuditOff     = "off"
	TaskAuditEnforce = "enforce"
)

// NormalizeTaskAuditMode clamps a wire value onto the closed audit vocabulary.
// Empty means inherit. An unrecognized non-empty value returns "" so callers
// can reject it instead of silently changing the gate's posture.
func NormalizeTaskAuditMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "inherit", "default", "global":
		return TaskAuditInherit
	case "off", "false", "0", "observe", "observe-only":
		return TaskAuditOff
	case "enforce", "on", "true", "1", "hitl":
		return TaskAuditEnforce
	default:
		return ""
	}
}

type TaskInput struct {
	Name         string
	Goal         string
	Instruction  string
	PhasePolicy  *PhasePolicy
	Schedule     *TaskSchedule
	ContextPaths []string
	AuditMode    string
	// Objective is the structured goal; nil = not structured.
	Objective *Objective
	Context   map[string]any
	// LLMChain / ActiveLLM seed the task's model chain (order = priority).
	LLMChain  []string
	ActiveLLM string
	// Proxy pins the run's outbound proxy ("" inherit, "direct", "N").
	Proxy string
}

// TaskUpdate is the PATCH body for a task (API_CONTRACT §2).
type TaskUpdate struct {
	Name         *string
	Goal         *string
	Instruction  *string
	PhasePolicy  *PhasePolicy // non-nil replaces the policy; nil leaves it
	Schedule     *TaskSchedule
	ContextPaths []string
	// AuditMode non-nil replaces the task's HITL switch (inherit/off/enforce).
	AuditMode *string
	// Objective non-nil replaces the structured goal; nil leaves it as is.
	Objective *Objective
	// ClearObjective removes the structured goal (an explicit "objective": null
	// in the wire body). Without it an objective can never be deleted.
	ClearObjective bool
	Context        map[string]any
	Archived       *bool
	// Proxy non-nil replaces the task's outbound proxy pin ("" = inherit).
	Proxy *string
	// LLMChain non-nil replaces the ordered model chain (empty slice clears it);
	// ActiveLLM non-nil replaces the chain head used by the next run.
	LLMChain  *[]string
	ActiveLLM *string
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
	if f := s.Fence; f != nil {
		normalized := Fence{
			Enforce:             f.Enforce,
			DenyTargets:         normalizeStringList(f.DenyTargets, false),
			AllowHTTPMethods:    normalizeStringList(f.AllowHTTPMethods, true),
			DenyCommandPatterns: normalizeStringList(f.DenyCommandPatterns, false),
		}
		s.Fence = &normalized
	}
	return s
}

// normalizeStringList trims, drops empties and de-duplicates while keeping the
// caller's order (case-insensitive dedupe; upper=true folds the value). Empty
// results stay nil so the JSON shape does not grow a horde of empty arrays.
func normalizeStringList(values []string, upper bool) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if upper {
			v = strings.ToUpper(v)
		}
		key := strings.ToLower(v)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ValidateScope exposes scope validation to the web layer so a candidate scope
// can be checked (project page "校验" action) without persisting it.
func ValidateScope(s Scope) error {
	return validateScope(normalizeScope(s))
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
	return validateFence(s)
}

// validateFence rejects fence shapes the runtime check cannot honour. Every
// rule here is a fail-closed write-time 400 (same posture as the scope entry
// port rule): a fence the engine would have to interpret loosely is worse than
// no fence, because the operator would believe the boundary holds.
func validateFence(s Scope) error {
	f := s.Fence
	if f == nil {
		return nil
	}
	if f.Enforce && len(s.Targets) == 0 {
		return fmt.Errorf("%w: fence cannot be enforced while the scope has no targets", ErrValidation)
	}
	if len(f.DenyTargets) > MaxFenceDenyTargets {
		return fmt.Errorf("%w: at most %d fence deny_targets", ErrValidation, MaxFenceDenyTargets)
	}
	for _, entry := range f.DenyTargets {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			return fmt.Errorf("%w: fence deny_targets contains an empty entry", ErrValidation)
		}
		if scopeEntryHasExplicitPort(trimmed) {
			return fmt.Errorf("%w: fence deny target %q carries a port; list hosts only and control ports via allow_ports", ErrValidation, trimmed)
		}
		if strings.ContainsAny(trimmed, " /?#") {
			return fmt.Errorf("%w: fence deny target %q is not a host or *.domain wildcard", ErrValidation, trimmed)
		}
	}
	allowed := make(map[string]struct{}, len(FenceHTTPMethods))
	for _, m := range FenceHTTPMethods {
		allowed[m] = struct{}{}
	}
	for _, raw := range f.AllowHTTPMethods {
		m := strings.ToUpper(strings.TrimSpace(raw))
		if m == "" {
			return fmt.Errorf("%w: fence allow_http_methods contains an empty entry", ErrValidation)
		}
		if _, ok := allowed[m]; !ok {
			return fmt.Errorf("%w: fence allow_http_methods entry %q is not a standard HTTP method", ErrValidation, raw)
		}
	}
	if len(f.DenyCommandPatterns) > MaxFenceDenyCommandPatterns {
		return fmt.Errorf("%w: at most %d fence deny_command_patterns", ErrValidation, MaxFenceDenyCommandPatterns)
	}
	for _, pattern := range f.DenyCommandPatterns {
		p := strings.TrimSpace(pattern)
		if p == "" {
			return fmt.Errorf("%w: fence deny_command_patterns contains an empty entry", ErrValidation)
		}
		if len(p) > MaxFencePatternLength {
			return fmt.Errorf("%w: fence deny pattern %q exceeds %d bytes", ErrValidation, p, MaxFencePatternLength)
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

// ── integrations: MCP servers / user skills / agent profiles ────────────────
//
// Operator-authored configuration entities consumed by the engine at scan
// launch. They are definitions, never judgements: an MCP server row does not
// vouch for its tools' output (field-level authority, charter §3.5), a user
// skill is prompt input (a Hint, not evidence), and an agent profile bounds
// delegation — it never bypasses the verifier or the finish gate.

// MCP transports.
const (
	McpTransportStdio = "stdio"
	McpTransportHTTP  = "http"
)

// DefaultMcpTimeoutSeconds is the per-call default when TimeoutSeconds is 0.
const DefaultMcpTimeoutSeconds = 120

// McpServer is one operator-registered MCP server definition. Env/Headers may
// carry credential material: the web layer masks them on every read (the API
// never returns secrets) and treats "****"-prefixed submissions as "unchanged"
// — the repository persists whatever it is given, so callers must unmask
// before write and mask after read.
type McpServer struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Transport       string            `json:"transport"` // McpTransportStdio / McpTransportHTTP
	Command         string            `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	URL             string            `json:"url,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Enabled         bool              `json:"enabled"`
	RequireApproval bool              `json:"require_approval"`
	TimeoutSeconds  int               `json:"timeout_seconds"`
	// Refresh projections (web layer only — set via UpdateMcpServer by the
	// refresh action; the repository never fills them on its own).
	ToolCount     int        `json:"tool_count"`
	LastHealthyAt *time.Time `json:"last_healthy_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`

	CreatedBy string     `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// McpServerInput is the POST body material for MCP server creation.
type McpServerInput struct {
	Name            string
	Transport       string
	Command         string
	Args            []string
	Env             map[string]string
	URL             string
	Headers         map[string]string
	Enabled         bool
	RequireApproval bool
	TimeoutSeconds  int
	CreatedBy       string
}

// McpServerUpdate is the PATCH body material. Nil pointers / nil slices / nil
// maps leave the field unchanged; non-nil replaces (an empty slice/map is an
// explicit clear).
type McpServerUpdate struct {
	Name            *string
	Command         *string
	Args            []string
	Env             map[string]string
	URL             *string
	Headers         map[string]string
	Enabled         *bool
	RequireApproval *bool
	TimeoutSeconds  *int
	// Refresh projections:
	ToolCount     *int
	LastHealthyAt *time.Time
	LastError     *string
}

// normalizeMcpServer JSON-stabilizes slices/maps and clamps the timeout so
// file snapshots and PG jsonb round-trip identically.
func normalizeMcpServer(s McpServer) McpServer {
	if s.Args == nil {
		s.Args = []string{}
	}
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	if s.Headers == nil {
		s.Headers = map[string]string{}
	}
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = DefaultMcpTimeoutSeconds
	}
	return s
}

// validateMcpServer checks structural coherence (shared by both stores).
// URL-level operator-machine rejection lives in the web layer and the engine's
// connect path (scopeguard) — the repository only sees shape.
func validateMcpServer(name, transport, command, url string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: MCP server name is required", ErrValidation)
	}
	if len([]rune(name)) > 64 {
		return fmt.Errorf("%w: MCP server name must be at most 64 characters", ErrValidation)
	}
	switch transport {
	case McpTransportStdio:
		if strings.TrimSpace(command) == "" {
			return fmt.Errorf("%w: stdio MCP server requires command", ErrValidation)
		}
	case McpTransportHTTP:
		if strings.TrimSpace(url) == "" {
			return fmt.Errorf("%w: http MCP server requires url", ErrValidation)
		}
	default:
		return fmt.Errorf("%w: MCP transport must be stdio or http", ErrValidation)
	}
	return nil
}

// SkillCategoryUser is the fixed category of user-authored skills; the
// embedded corpus keeps its own categories and is never stored here.
const SkillCategoryUser = "user"

// MaxSkillBodyBytes bounds one user skill body so neither the snapshot file
// nor a prompt-sized read explodes.
const MaxSkillBodyBytes = 128 * 1024

// Skill is one user-authored skill document (SKILL.md conventions: kebab
// name, short description, optional tags/intent, markdown body). User skills
// merge into read_skill/list_skills/search_skills alongside the embedded
// corpus with source=user.
type Skill struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Category    string     `json:"category"`
	Description string     `json:"description"`
	Tags        []string   `json:"tags,omitempty"`
	Intent      string     `json:"intent,omitempty"`
	Body        string     `json:"body"`
	Enabled     bool       `json:"enabled"`
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

// SkillInput is the POST body material for skill creation.
type SkillInput struct {
	Name        string
	Description string
	Tags        []string
	Intent      string
	Body        string
	Enabled     bool
	CreatedBy   string
}

// SkillUpdate is the PATCH body material; nil leaves unchanged.
type SkillUpdate struct {
	Name        *string
	Description *string
	Tags        []string
	Intent      *string
	Body        *string
	Enabled     *bool
}

// skillNamePattern is the kebab-case contract the skills tools document
// (read_skill name=analyzing-active-directory-acl-abuse).
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// normalizeSkill JSON-stabilizes slices.
func normalizeSkill(s Skill) Skill {
	if s.Tags == nil {
		s.Tags = []string{}
	}
	if s.Category == "" {
		s.Category = SkillCategoryUser
	}
	return s
}

// validateSkill checks the shared skill shape.
func validateSkill(name, description, body string) error {
	if !skillNamePattern.MatchString(strings.TrimSpace(name)) {
		return fmt.Errorf("%w: skill name must be kebab-case (lowercase letters/digits separated by single hyphens)", ErrValidation)
	}
	if len(name) > 96 {
		return fmt.Errorf("%w: skill name must be at most 96 characters", ErrValidation)
	}
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("%w: skill description is required", ErrValidation)
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("%w: skill body is required", ErrValidation)
	}
	if len(body) > MaxSkillBodyBytes {
		return fmt.Errorf("%w: skill body exceeds %d bytes", ErrValidation, MaxSkillBodyBytes)
	}
	return nil
}

// AgentProfile is one operator-authored delegation profile merged alongside
// the built-in specialist profiles (ledger hooks): the coordinator may
// auto-delegate into it and the model may spawn it by role name. Role is the
// spawn vocabulary; reserved built-in roles are rejected by the web layer
// (the repository stays decoupled from the agent package).
type AgentProfile struct {
	ID               string   `json:"id"`
	Role             string   `json:"role"`
	Focus            string   `json:"focus"`
	VulnClasses      []string `json:"vuln_classes"`
	EvidenceContract string   `json:"evidence_contract"`
	StoppingRule     string   `json:"stopping_rule"`
	ToolScopeHidden  []string `json:"tool_scope_hidden,omitempty"`
	Enabled          bool     `json:"enabled"`
	// Builtin marks the code-defined specialist rows the web layer merges
	// into GET responses. It is never persisted true: stored rows are always
	// operator-authored (builtin mutations are rejected at the handler).
	Builtin   bool       `json:"builtin"`
	CreatedBy string     `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// AgentProfileInput is the POST body material for profile creation.
type AgentProfileInput struct {
	Role             string
	Focus            string
	VulnClasses      []string
	EvidenceContract string
	StoppingRule     string
	ToolScopeHidden  []string
	Enabled          bool
	CreatedBy        string
}

// AgentProfileUpdate is the PATCH body material; nil slices leave unchanged.
type AgentProfileUpdate struct {
	Role             *string
	Focus            *string
	VulnClasses      []string
	EvidenceContract *string
	StoppingRule     *string
	ToolScopeHidden  []string
	Enabled          *bool
}

// agentRolePattern matches the built-in role vocabulary shape
// (recon-discovery, authz-logic, …).
var agentRolePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// normalizeAgentProfile JSON-stabilizes slices.
func normalizeAgentProfile(p AgentProfile) AgentProfile {
	if p.VulnClasses == nil {
		p.VulnClasses = []string{}
	}
	if p.ToolScopeHidden == nil {
		p.ToolScopeHidden = []string{}
	}
	return p
}

// validateAgentProfile checks the shared profile shape. The delegation
// contract triple (focus / evidence_contract / stopping_rule) is required —
// an agent without an explicit stopping rule is exactly the "run more agents"
// failure mode the built-in profiles encode against.
func validateAgentProfile(role, focus, evidenceContract, stoppingRule string) error {
	if !agentRolePattern.MatchString(strings.TrimSpace(role)) {
		return fmt.Errorf("%w: agent role must be kebab-case (lowercase letters/digits separated by single hyphens)", ErrValidation)
	}
	if len(role) > 64 {
		return fmt.Errorf("%w: agent role must be at most 64 characters", ErrValidation)
	}
	if strings.TrimSpace(focus) == "" {
		return fmt.Errorf("%w: agent focus is required", ErrValidation)
	}
	if strings.TrimSpace(evidenceContract) == "" {
		return fmt.Errorf("%w: agent evidence_contract is required", ErrValidation)
	}
	if strings.TrimSpace(stoppingRule) == "" {
		return fmt.Errorf("%w: agent stopping_rule is required", ErrValidation)
	}
	return nil
}

// ── reports (manual report delivery) ────────────────────────────────────────

// Report lifecycle states. Only queued/generating are active; the partial
// unique index in migration 0028 allows at most one active report per task.
const (
	ReportQueued     = "queued"
	ReportGenerating = "generating"
	ReportReady      = "ready"
	ReportFailed     = "failed"
	ReportCancelled  = "cancelled"
)

// Report is the durable metadata row of one manually generated report
// revision. The rendered PDF/MD/HTML are byte artifacts on disk (D12); this
// row is the status/version authority and the artifact index pointer.
type Report struct {
	ID            string     `json:"id"`
	ProjectID     string     `json:"project_id,omitempty"`
	TaskID        string     `json:"task_id"`
	Revision      int        `json:"revision"`
	RunIDs        []string   `json:"run_ids,omitempty"`
	ScanIDs       []string   `json:"scan_ids,omitempty"`
	Status        string     `json:"status"`
	Template      string     `json:"template"`
	Language      string     `json:"language"`
	Mode          string     `json:"mode"`
	Model         string     `json:"model,omitempty"`
	PromptVersion string     `json:"prompt_version,omitempty"`
	Formats       []string   `json:"formats,omitempty"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// ReportArtifact is one rendered file belonging to a report. Kind is
// "total" or "sub"; sub artifacts also carry the scan id and target.
type ReportArtifact struct {
	ReportID string `json:"report_id"`
	Kind     string `json:"kind"`
	ScanID   string `json:"scan_id,omitempty"`
	Target   string `json:"target,omitempty"`
	Format   string `json:"format"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256,omitempty"`
}

// ReportMount is the creation input for CreateReport.
type ReportMount struct {
	ProjectID     string
	TaskID        string
	RunIDs        []string
	ScanIDs       []string
	Formats       []string
	Template      string
	Language      string
	Mode          string
	Model         string
	PromptVersion string
}

// NormalizeTaskLLMChain trims, drops malformed and duplicate entries, and
// falls back to the first entry when active names a profile outside the chain
// (or names none). Order is priority: index 0 is the chain head. An empty chain
// clears both values.
func NormalizeTaskLLMChain(chain []string, active string) ([]string, string) {
	out := make([]string, 0, len(chain))
	seen := make(map[string]struct{}, len(chain))
	for _, raw := range chain {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		provider, profile, ok := strings.Cut(key, ":")
		if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(profile) == "" {
			continue // not a "<provider>:<profileId>" key
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil, ""
	}
	active = strings.TrimSpace(active)
	if _, ok := seen[active]; !ok {
		active = out[0]
	}
	return out, active
}

// hydrateTaskLLMChain exposes the context-stored model chain on the Task. The
// chain lives in the task context (llm_chain / llm_active) so no schema change
// is needed; Context itself stays off the wire.
func hydrateTaskLLMChain(t *Task) {
	if t == nil || t.Context == nil {
		return
	}
	t.LLMChain = nil
	t.ActiveLLM = ""
	switch raw := t.Context["llm_chain"].(type) {
	case []any:
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				t.LLMChain = append(t.LLMChain, s)
			}
		}
	case []string:
		t.LLMChain = append([]string(nil), raw...)
	}
	if v, ok := t.Context["llm_active"].(string); ok {
		t.ActiveLLM = v
	}
}

// NormalizeTaskProxy canonicalizes the task proxy pin: "" (inherit), "direct",
// or a positive 1-based pool index. Anything else collapses to inherit; the
// handler validates the index against the live pool at save time.
func NormalizeTaskProxy(raw string) string {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case "", "inherit", "global", "default", "follow":
		return ""
	case "direct", "none", "off":
		return "direct"
	default:
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
}

// hydrateTaskProxy exposes the context-stored proxy pin on the Task.
func hydrateTaskProxy(t *Task) {
	if t == nil || t.Context == nil {
		return
	}
	t.Proxy = ""
	if v, ok := t.Context["proxy"].(string); ok {
		t.Proxy = NormalizeTaskProxy(v)
	}
}
