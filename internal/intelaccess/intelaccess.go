// Package intelaccess is the reachability projection over recorded
// intelligence (docs/INTEL_HANDOFF_SPEC.md, M2).
//
// Intelligence items are observations: immutable, content-bearing, aggregated
// at task level. intel_access answers a different question — which doors a
// result can open, in which scan, and whether a human has blessed it. The
// projection never copies content: it holds identities, canonical targets and
// state only. Its only writer is the conclusion transaction (DeriveScan via
// the graph hook); the only other writes are the operator's triage endpoint
// (Confirm/Revoke/AddGrants) — tools and workers have no direct write path.
//
// Identity is scan-scoped (SPEC D8): reachability authorizes THIS scan's
// dispatch, while the fingerprint stays the task-level identity triage uses.
package intelaccess

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// ErrNotFound is returned when a (scan, fingerprint) access row is required
// but absent.
var ErrNotFound = errors.New("intelaccess: access not found")

// Kind vocabulary. Mirrors the SPEC §4 CHECK: sensitive_data is a disclosure,
// not a door, so it never becomes an access row.
const (
	KindCredential = "credential"
	KindSession    = "session"
	KindPath       = "path"
)

// State vocabulary. Only confirmed/active participate in reachability
// decisions; proposed is an unendorsed fact, expired/revoked are inert.
const (
	StateProposed  = "proposed"
	StateConfirmed = "confirmed"
	StateActive    = "active"
	StateExpired   = "expired"
	StateRevoked   = "revoked"
)

// Access is one derived reachability row plus its canonical targets.
type Access struct {
	ID             int64      `json:"id"`
	ProjectID      string     `json:"project_id,omitempty"`
	ScanID         string     `json:"scan_id"`
	Fingerprint    string     `json:"fingerprint"`
	Kind           string     `json:"kind"`
	Subject        string     `json:"subject,omitempty"`
	State          string     `json:"state"`
	SourceIntentID int64      `json:"source_intent_id,omitempty"`
	ConfirmedBy    string     `json:"confirmed_by,omitempty"`
	ConfirmedAt    *time.Time `json:"confirmed_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	UseCount       int        `json:"use_count"`
	FailCount      int        `json:"fail_count"`
	RevokeReason   string     `json:"revoke_reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Targets        []string   `json:"grants,omitempty"`
}

// Usable reports whether this access may participate in a reachability
// decision right now (invariant: proposed/expired/revoked never do; expiry is
// evaluated here, not by a background sweeper).
func (a Access) Usable(now time.Time) bool {
	if a.State != StateConfirmed && a.State != StateActive {
		return false
	}
	return a.ExpiresAt == nil || a.ExpiresAt.After(now)
}

// ConfirmInput is the operator endorsement of one item within one scan.
type ConfirmInput struct {
	ProjectID   string
	ScanID      string
	Fingerprint string
	Kind        string
	Subject     string
	Targets     []string
	By          string
	ExpiresAt   *time.Time
}

// Store is the projection's read/write surface. PGStore is the production
// authority; MemoryStore serves unit tests and offline harnesses.
type Store interface {
	// DeriveScan projects every item of one scan (idempotent per
	// (scan_id, fingerprint); an existing state is never downgraded, so a
	// re-derivation cannot resurrect a dismissed item).
	DeriveScan(ctx context.Context, scanID string, items []intelligence.Item) (int, error)
	// DeriveScanForIntent is DeriveScan plus attribution: a NEWLY derived row
	// records the concluding intent as its source, which is what the exit gate
	// checks. Attribution is first-wins (a re-derivation never rewrites it).
	DeriveScanForIntent(ctx context.Context, scanID string, intentID int64, items []intelligence.Item) (int, error)
	// Get returns one row; expiry is reported in the row, not enforced here.
	Get(ctx context.Context, scanID, fingerprint string) (Access, bool, error)
	// ListUsable returns confirmed/active, unexpired rows, optionally
	// narrowed to one exact target, newest endorsement first, capped.
	ListUsable(ctx context.Context, scanID, target string, limit int) ([]Access, error)
	// Lookup resolves one fingerprint across a set of scans (task-level views
	// aggregate runs, so the operator's row may live in any of them).
	Lookup(ctx context.Context, scanIDs []string, fingerprint string) (Access, bool, error)
	// Confirm upserts the operator endorsement (creating the row if the item
	// was never derived, which happens when triage precedes the next
	// conclusion).
	Confirm(ctx context.Context, in ConfirmInput) (Access, error)
	// Revoke marks the row inert with an explicit reason.
	Revoke(ctx context.Context, scanID, fingerprint, reason string) error
	// AddGrants appends operator-supplied canonical targets. Derivation only
	// ever inserts, so manual grants survive re-derivation.
	AddGrants(ctx context.Context, scanID, fingerprint string, targets []string) (Access, error)
	// FindGrant returns the usable access row whose grants cover target
	// (exact match or a CIDR grant containing the target's address). It is the
	// single grant matcher: the judge, the seed and the approval endpoint all
	// go through it.
	FindGrant(ctx context.Context, scanID, target string) (Access, bool, error)
	// IssueCapabilities issues (or re-issues) one-shot permissions for one
	// intent and a set of canonical targets; all rows share a batch id.
	IssueCapabilities(ctx context.Context, in IssueInput) ([]Capability, error)
	// ConsumeCapability spends one use of a live capability for the exact
	// target and bumps the originating access counters in the same step.
	ConsumeCapability(ctx context.Context, scanID string, intentID int64, target string) (Capability, bool, error)
	// ListCapabilities returns one intent's issued permissions, newest first.
	ListCapabilities(ctx context.Context, scanID string, intentID int64) ([]Capability, error)
	// ListAccess returns every access row of one run, newest first (read
	// projection for the panel/report; no state decision is made here).
	ListAccess(ctx context.Context, scanID string) ([]Access, error)
	// ListScanCapabilities returns the run's issued permissions, newest first.
	ListScanCapabilities(ctx context.Context, scanID string, limit int) ([]Capability, error)
	// RecordUse appends one audit projection row (best-effort: a write failure
	// never blocks the decision that produced it).
	RecordUse(ctx context.Context, row UseRecord) error
	// ListUses returns the newest audit rows of one run, capped.
	ListUses(ctx context.Context, scanID string, limit int) ([]UseRecord, error)
	// RecordFailure increments the access failure counter; reaching threshold
	// revokes it with an explicit reason. revoked reports the transition.
	RecordFailure(ctx context.Context, scanID, fingerprint string, threshold int) (Access, bool, error)
}

var (
	storeMu sync.RWMutex
	store   Store
)

// SetStore registers the process-wide projection store (nil resets it). Same
// discipline as the other backends: registered once at startup, read through
// StoreOrNil.
func SetStore(s Store) {
	storeMu.Lock()
	defer storeMu.Unlock()
	store = s
}

// StoreOrNil returns the registered store.
func StoreOrNil() Store {
	storeMu.RLock()
	defer storeMu.RUnlock()
	return store
}
