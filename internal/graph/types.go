// Package graph is the authoritative exploration + asset graph store.
//
// Charter v3.0 / REDESIGN D12 (R7): the exploration graph is the single
// authority for intent / belief / fact / finding / goal / hint / digest
// nodes and their lineage edges. PlanTask, PlanView, /plan and /units are
// read views over intent nodes (migration 0020). The asset graph is the
// authority for asset identity; InfoItem remains the observation provenance.
//
// Production uses the PostgreSQL store (Open). NewMemoryStore exists for
// tests and the legacy/offline path, never as a second production authority.
//
// R8 (docs/INFO_GRAPH_CONTRACT.md): intents carry an optional contract
// (preconditions / yields / confirmation); concluding an intent is a single
// transaction (ConcludeIntent for workers, ConcludePlanned for the plan
// surface); ValidateStore exposes read-only runtime conformance.
package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ConcludeIntent errors. Callers distinguish "someone else already settled
// this intent" from a transient store failure and must never rewrite state on
// the first two.
var (
	ErrConflict             = errors.New("graph: intent state conflict")
	ErrLeaseExpired         = errors.New("graph: intent lease expired")
	ErrConfirmationRequired = errors.New("graph: intent confirmation required")
	ErrPreconditionUnmet    = errors.New("graph: intent precondition unmet")
	ErrNoProduct            = errors.New("graph: done intent requires a produced node")
	ErrYieldUnmet           = errors.New("graph: intent declared a yield kind it did not produce")
)

// Kind is one exploration node type.
type Kind string

const (
	KindGoal    Kind = "goal"
	KindIntent  Kind = "intent"
	KindBelief  Kind = "belief"
	KindFact    Kind = "fact"
	KindFinding Kind = "finding"
	KindHint    Kind = "hint"
	KindDigest  Kind = "digest"
)

// State is the per-kind lifecycle state. The DB CHECK constraint in
// migration 0018 is the production validator; ValidateState mirrors it so
// the in-memory store and test fixtures fail the same way.
type State string

const (
	// Intent lifecycle. A done intent always carries a yields receipt.

	StateOpen      State = "open"
	StateRunning   State = "running"
	StatePaused    State = "paused"
	StateDone      State = "done"
	StateBlocked   State = "blocked"
	StateExhausted State = "exhausted"
	StateStopped   State = "stopped"
	StateDeleted   State = "deleted"

	// Belief lifecycle (evidence detail stays in the ledger).

	StateQueued   State = "queued"
	StateTesting  State = "testing"
	StateProven   State = "proven"
	StateRejected State = "rejected"

	// Fact / finding lifecycle. Origin is a raw receipt, not a confirmation.

	StateConfirmed State = "confirmed"
	StateDismissed State = "dismissed"
	StateOrigin    State = "origin"

	// Goal criterion lifecycle.

	StateMet       State = "met"
	StateAbandoned State = "abandoned"

	// Hint / digest lifecycle.

	StateActive     State = "active"
	StateConsumed   State = "consumed"
	StateSuperseded State = "superseded"
)

// Rel is one lineage edge type.
type Rel string

const (
	RelSpawns      Rel = "spawns"
	RelDerivedFrom Rel = "derived_from"
	RelYields      Rel = "yields"
	RelProves      Rel = "proves"
	RelSupports    Rel = "supports"
	RelCovers      Rel = "covers"
	RelDependsOn   Rel = "depends_on"
)

// Node is one authoritative graph node.
type Node struct {
	ID         int64  `json:"id"`
	ScanID     string `json:"scan_id"`
	InstanceID string `json:"instance_id,omitempty"`
	Kind       Kind   `json:"kind"`
	State      State  `json:"state"`
	// UnitKey is the stable run-scoped business key (PlanTask id, hypothesis
	// id, evidence id, finding id, criterion id, guidance id). Projections
	// upsert by it; empty means the row predates 0023 or is lineage-only.
	UnitKey        string     `json:"unit_key,omitempty"`
	Priority       int        `json:"priority,omitempty"`
	Origin         string     `json:"origin,omitempty"`
	Owner          string     `json:"owner,omitempty"`
	LeaseUntil     *time.Time `json:"lease_until,omitempty"`
	BlockedReason  string     `json:"blocked_reason,omitempty"`
	DeleteReason   string     `json:"delete_reason,omitempty"`
	Payload        string     `json:"payload,omitempty"`
	ContentVersion int64      `json:"content_version"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// PayloadJSON decodes the node payload. Invalid payloads return an empty
// object rather than failing a panel read.
func (n Node) PayloadJSON() map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(n.Payload) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(n.Payload), &out); err != nil {
		return map[string]any{}
	}
	return out
}

// NodeInput creates one node. State defaults per kind when empty.
type NodeInput struct {
	ScanID     string
	InstanceID string
	Kind       Kind
	State      State
	UnitKey    string
	Priority   int
	Origin     string
	Owner      string
	LeaseUntil *time.Time
	Payload    string
}

// Normalize fills defaults and validates the node identity.
func (in *NodeInput) Normalize() error {
	if strings.TrimSpace(in.ScanID) == "" {
		return fmt.Errorf("graph: scan_id is required")
	}
	if !ValidKind(in.Kind) {
		return fmt.Errorf("graph: invalid kind %q", in.Kind)
	}
	if in.State == "" {
		in.State = DefaultState(in.Kind)
	}
	if !ValidState(in.Kind, in.State) {
		return fmt.Errorf("graph: invalid state %q for kind %q", in.State, in.Kind)
	}
	if strings.TrimSpace(in.Payload) == "" {
		in.Payload = "{}"
	} else if !json.Valid([]byte(in.Payload)) {
		return fmt.Errorf("graph: payload is not valid JSON")
	}
	return nil
}

// EdgeInput creates one lineage edge.
type EdgeInput struct {
	ScanID string
	SrcID  int64
	DstID  int64
	Rel    Rel
}

// Validate checks the edge identity.
func (e EdgeInput) Validate() error {
	if strings.TrimSpace(e.ScanID) == "" {
		return fmt.Errorf("graph: scan_id is required")
	}
	if e.SrcID <= 0 || e.DstID <= 0 {
		return fmt.Errorf("graph: edge endpoints are required")
	}
	if !ValidRel(e.Rel) {
		return fmt.Errorf("graph: invalid edge rel %q", e.Rel)
	}
	return nil
}

// PlanTask is the legacy PlanTask shape derived from an intent node
// (migration 0020 v_plan_tasks). Status stays pending/active/completed/
// skipped so existing finish gates keep their contract.
type PlanTask struct {
	NodeID     int64  `json:"node_id"`
	TaskID     string `json:"id"`
	InstanceID string `json:"instance_id,omitempty"`
	Title      string `json:"title"`
	Phase      int    `json:"phase"`
	VulnClass  string `json:"vuln_class,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	// Target is the dispatch target the intent names (host:port or URL). It is
	// distinct from Endpoint: endpoint is the path/route inside that target.
	// Dedupe keys use Target when present, Endpoint otherwise.
	Target       string     `json:"target,omitempty"`
	Status       string     `json:"status"`
	GraphState   string     `json:"graph_state,omitempty"`
	Disposition  string     `json:"disposition,omitempty"`
	Origin       string     `json:"origin,omitempty"`
	DependsOn    []string   `json:"depends_on,omitempty"`
	Notes        string     `json:"notes,omitempty"`
	Prerequisite bool       `json:"prerequisite,omitempty"`
	WholeTarget  bool       `json:"whole_target,omitempty"`
	ObjectiveRef string     `json:"objective_ref,omitempty"`
	Owner        string     `json:"owner,omitempty"`
	LeaseUntil   *time.Time `json:"lease_until,omitempty"`
	Priority     int        `json:"priority,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Edge is one lineage edge.
type Edge struct {
	SrcID int64 `json:"src_id"`
	DstID int64 `json:"dst_id"`
	Rel   Rel   `json:"rel"`
}

// Unit is the unified intent+belief dispatch view (migration 0020 v_units).
type Unit struct {
	Kind       string     `json:"kind"`
	NodeID     int64      `json:"node_id"`
	UnitID     string     `json:"id"`
	Subject    string     `json:"subject"`
	State      string     `json:"state"`
	Owner      string     `json:"owner,omitempty"`
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
	VulnClass  string     `json:"vuln_class,omitempty"`
	Phase      int        `json:"phase,omitempty"`
	Expired    bool       `json:"expired,omitempty"`
}

// Counts is the graph header projection used by /units and planner overview.
type Counts struct {
	OpenIntents      int `json:"tasks_open"`
	RunningIntents   int `json:"tasks_claimed"`
	PausedIntents    int `json:"tasks_paused"`
	DoneIntents      int `json:"tasks_done"`
	BlockedIntents   int `json:"tasks_blocked"`
	QueuedBeliefs    int `json:"hypotheses_queued"`
	TestingBeliefs   int `json:"hypotheses_testing"`
	ProvenBeliefs    int `json:"hypotheses_proven"`
	ConfirmedFacts   int `json:"facts_confirmed"`
	ConfirmedFinding int `json:"findings_confirmed"`
	OpenGoals        int `json:"goals_open"`
	MetGoals         int `json:"goals_met"`
}

// Repository is the graph authority seam. Production uses PGStore; tests
// and the legacy/offline path use MemoryStore.
type Repository interface {
	AddNode(ctx context.Context, in NodeInput) (*Node, error)
	UpsertNode(ctx context.Context, in NodeInput) (*Node, error)
	AddEdge(ctx context.Context, in EdgeInput) error
	UpdateNode(ctx context.Context, scanID string, id int64, payload string, priority int) error
	ReplaceDependencyEdges(ctx context.Context, scanID string, dstID int64, srcIDs []int64) error
	GetNode(ctx context.Context, scanID string, id int64) (*Node, error)
	ListNodes(ctx context.Context, scanID string, kind Kind, states ...State) ([]Node, error)
	ListEdges(ctx context.Context, scanID string) ([]Edge, error)
	ClaimNextIntent(ctx context.Context, scanID, owner string, ttl time.Duration) (*Node, error)
	RenewLease(ctx context.Context, scanID string, id int64, owner string, ttl time.Duration) (bool, error)
	ReclaimExpired(ctx context.Context, scanID string) (int64, error)
	ResetRunning(ctx context.Context, scanID string) (int64, error)
	Transition(ctx context.Context, scanID string, id int64, kind Kind, from, to State) (bool, error)
	// ConcludeIntent settles a claimed intent and its produced nodes in one
	// transaction: owner/lease/confirmation/precondition checks, idempotent
	// produced upserts, yields edges, then the terminal transition. It is the
	// only sanctioned way for a worker to reach StateDone.
	ConcludeIntent(ctx context.Context, scanID string, id int64, owner string, to State, produced []NodeInput) (*Node, error)
	// ConcludePlanned settles an intent from the plan surface (planner /
	// build_plan / update_plan) that has no worker claim. It applies the same
	// confirmation, precondition, receipt and yields rules without requiring a
	// lease. Only plan_graph may call it.
	ConcludePlanned(ctx context.Context, scanID string, id int64, to State, produced []NodeInput) (*Node, error)
	// SettleIntent moves a worker-owned running intent to a terminal state
	// (blocked/exhausted/stopped) or back to open (worker missing) in one
	// statement, writing the reason atomically and fencing the update on the
	// current owner. False means the caller no longer owns a running intent
	// (another worker reclaimed the expired lease); the caller MUST NOT retry
	// with a plain Transition. done is refused: only ConcludeIntent can produce
	// a done intent.
	SettleIntent(ctx context.Context, scanID string, id int64, owner string, to State, reason string) (bool, error)
	// SetBlockedReason records why an intent is blocked (operator/debug
	// visibility). It only touches rows already in state=blocked. Worker
	// settlement uses SettleIntent so the reason and state can never diverge.
	SetBlockedReason(ctx context.Context, scanID string, id int64, reason string) error
	Counts(ctx context.Context, scanID string) (Counts, error)
	PlanTasks(ctx context.Context, scanID string) ([]PlanTask, error)
	Units(ctx context.Context, scanID string) ([]Unit, error)
	Close() error
}

// ValidKind reports whether k is a known node kind.
func ValidKind(k Kind) bool {
	switch k {
	case KindGoal, KindIntent, KindBelief, KindFact, KindFinding, KindHint, KindDigest:
		return true
	default:
		return false
	}
}

// ValidRel reports whether r is a known edge relation.
func ValidRel(r Rel) bool {
	switch r {
	case RelSpawns, RelDerivedFrom, RelYields, RelProves, RelSupports, RelCovers, RelDependsOn:
		return true
	default:
		return false
	}
}

// DefaultState is the initial state for one kind.
func DefaultState(k Kind) State {
	switch k {
	case KindIntent:
		return StateOpen
	case KindBelief:
		return StateQueued
	case KindFact:
		return StateConfirmed
	case KindFinding:
		return StateConfirmed
	case KindGoal:
		return StateOpen
	case KindDigest:
		return StateActive
	case KindHint:
		return StateActive
	default:
		return StateOpen
	}
}

// ValidState mirrors the migration 0018 CHECK constraint.
func ValidState(k Kind, s State) bool {
	switch k {
	case KindIntent:
		switch s {
		case StateOpen, StateRunning, StatePaused, StateDone,
			StateBlocked, StateExhausted, StateStopped, StateDeleted:
			return true
		}
	case KindBelief:
		switch s {
		case StateQueued, StateTesting, StateProven, StateRejected,
			StateBlocked, StateExhausted:
			return true
		}
	case KindFact:
		switch s {
		case StateConfirmed, StateDismissed, StateOrigin:
			return true
		}
	case KindFinding:
		switch s {
		case StateConfirmed, StateDismissed:
			return true
		}
	case KindGoal:
		switch s {
		case StateOpen, StateMet, StateAbandoned:
			return true
		}
	case KindHint:
		switch s {
		case StateActive, StateConsumed:
			return true
		}
	case KindDigest:
		switch s {
		case StateActive, StateSuperseded:
			return true
		}
	}
	return false
}

// TerminalState reports whether a node of kind k in state s is terminal for
// dependency purposes. Paused is deliberately NOT terminal (the work will
// resume). Deleted is terminal for dispatch.
func TerminalState(k Kind, s State) bool {
	switch k {
	case KindIntent:
		switch s {
		case StateDone, StateBlocked, StateExhausted, StateStopped, StateDeleted:
			return true
		}
	case KindBelief:
		switch s {
		case StateProven, StateRejected, StateBlocked, StateExhausted:
			return true
		}
	}
	return false
}
