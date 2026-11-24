package scanctx

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/storage"
)

// HypothesisStatus is the lifecycle state of an attack hypothesis in the
// ledger. It is a strict superset of the in-memory planner TaskStatus, adding
// the states an evidence-driven scheduler needs to reason about what is worth
// spending budget on next.
type HypothesisStatus string

const (
	// HypothesisQueued means the hypothesis has been identified from recon but
	// not yet investigated. Queued hypotheses are the primary schedulable pool.
	HypothesisQueued HypothesisStatus = "queued"
	// HypothesisTesting means a specialist is actively probing it right now.
	HypothesisTesting HypothesisStatus = "testing"
	// HypothesisProven means it was exploited with concrete, reproducible
	// evidence (typically linked to a reported finding by ID).
	HypothesisProven HypothesisStatus = "proven"
	// HypothesisRejected means it was investigated and disproven / not
	// exploitable (a control/baseline showed the behavior is intended).
	HypothesisRejected HypothesisStatus = "rejected"
	// HypothesisBlocked means it needs a precondition (e.g. credentials, a
	// reachable endpoint, a prior chain step) that is not yet satisfied.
	HypothesisBlocked HypothesisStatus = "blocked"
	// HypothesisExhausted means the attempt/budget was spent without proof;
	// kept so the scheduler does not re-open it indefinitely.
	HypothesisExhausted HypothesisStatus = "exhausted"
)

// validHypothesisStatuses gates external (tool/LLM-supplied) status values so a
// malformed update cannot corrupt scheduling.
var validHypothesisStatuses = map[HypothesisStatus]bool{
	HypothesisQueued:    true,
	HypothesisTesting:   true,
	HypothesisProven:    true,
	HypothesisRejected:  true,
	HypothesisBlocked:   true,
	HypothesisExhausted: true,
}

// NormalizeHypothesisStatus maps a free-form string to a known status, falling
// back to queued for anything unrecognized so scheduling never sees garbage.
func NormalizeHypothesisStatus(s string) HypothesisStatus {
	st := HypothesisStatus(strings.ToLower(strings.TrimSpace(s)))
	if validHypothesisStatuses[st] {
		return st
	}
	return HypothesisQueued
}

// Terminal reports whether a status is a settled outcome that the scheduler
// should not normally re-open (proven/rejected/exhausted).
func (s HypothesisStatus) Terminal() bool {
	return s == HypothesisProven || s == HypothesisRejected || s == HypothesisExhausted
}

const (
	maxEvidencePerHypothesis = 40   // keep memory/disk bounded (DoD)
	maxEvidenceStringLen     = 4096 // request/response fields
	maxSummaryLen            = 2048
	maxHypothesisFieldLen    = 512

	// EvidenceFindingRef marks evidence that references a confirmed, reported
	// finding by its reporting ID (e.g. XALG-3). This kind is never evicted by
	// the per-hypothesis evidence cap.
	EvidenceFindingRef = "finding_ref"
)

// EvidenceNature distinguishes a raw observation from an interpretation.
// It is deliberately separate from Kind: Kind describes the evidence's role in
// the hypothesis (baseline/probe/exploit/...), while Nature describes whether
// the content is observed or inferred.
type EvidenceNature string

const (
	EvidenceObserved EvidenceNature = "observed"
	EvidenceInferred EvidenceNature = "inferred"
)

// NormalizeEvidenceNature defaults unknown or legacy values to inferred. An
// absent value must never be promoted to an observation.
func NormalizeEvidenceNature(n EvidenceNature) EvidenceNature {
	if n == EvidenceObserved {
		return EvidenceObserved
	}
	return EvidenceInferred
}

// Evidence is a single append-only observation attached to a hypothesis.
// Specialists accumulate observations (baselines, probes, exploit attempts,
// artifacts); confirmed findings are referenced by the authoritative reporting
// ID rather than duplicating the full proof payload here.
type Evidence struct {
	ID string `json:"id"`
	// Kind is a coarse category: "baseline"/"control", "probe", "exploit",
	// "artifact", or EvidenceFindingRef. Free-form but lower-cased on store.
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	// Request/Response are optional raw control/PoC evidence, bounded in size.
	Request  string `json:"request,omitempty"`
	Response string `json:"response,omitempty"`
	// FindingID links this evidence to a reported reporting.Vulnerability
	// (e.g. "XALG-3") when Kind == EvidenceFindingRef.
	FindingID string `json:"finding_id,omitempty"`
	// AgentID records which agent (coordinator or a specialist delegation ID)
	// contributed the evidence.
	AgentID string `json:"agent_id,omitempty"`
	// Source names the engine verifier that wrote this evidence (for example
	// "verify_sqli"). Only engine code sets it; evidence the model records
	// itself (add_hypothesis_evidence) never carries one, so proof gates can
	// tell an engine confirmation from model-written text.
	Source string `json:"source,omitempty"`
	// Nature separates raw observation from inference. Missing/legacy values
	// normalize to inferred.
	Nature EvidenceNature `json:"nature,omitempty"`
	// Supports lists Evidence IDs in the same hypothesis that substantiate an
	// inferred item. It is empty for observed items.
	Supports []string `json:"supports,omitempty"`
	// InfoItemID links this projection to the global InfoItem cold path.
	InfoItemID string `json:"info_item_id,omitempty"`
	// IntentID links this observation to the intent whose execution produced
	// it (R3 Information-to-Decision Trace). Empty for engine-derived
	// evidence that no single intent owns.
	IntentID string `json:"intent_id,omitempty"`
	// Confidence is the quality of THIS observation in [0,1].
	Confidence float64   `json:"confidence,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Hypothesis is one attack hypothesis: a (vulnerability class × target surface)
// claim with its lifecycle status, accumulated evidence, and scheduling
// metadata. It is the durable, scan-shared unit that drives specialist
// scheduling — a superset of the in-memory planner Task.
type Hypothesis struct {
	ID       string `json:"id"`
	DedupKey string `json:"dedup_key"`
	Title    string `json:"title"`

	// Identity — what and where.
	VulnClass string `json:"vuln_class"`
	Target    string `json:"target,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Parameter string `json:"parameter,omitempty"`
	// Role is the auth context this hypothesis applies to (anonymous, user,
	// admin, or a named role) — central to authorization/IDOR testing.
	Role string `json:"role,omitempty"`
	// DataFlow captures a source->sink path for whitebox/source-driven work.
	DataFlow string `json:"data_flow,omitempty"`

	// Preconditions / privileges required before this is testable.
	Preconditions     []string `json:"preconditions,omitempty"`
	RequiredPrivilege string   `json:"required_privilege,omitempty"`
	// Baseline is the control/negative result used to distinguish a real
	// vulnerability from intended behavior.
	Baseline string `json:"baseline,omitempty"`
	// Falsification states what observation would refute this claim. R2
	// quality field (JSON only; ledger snapshots are JSON documents, so this is
	// backward compatible and needs no migration).
	Falsification string `json:"falsification,omitempty"`
	// NextObservation is the single observation most likely to discriminate
	// this hypothesis from its alternatives.
	NextObservation string `json:"next_observation,omitempty"`

	Status HypothesisStatus `json:"status"`
	// Confidence is the current belief (in [0,1]) that this is exploitable.
	Confidence float64 `json:"confidence"`
	// VOI is an optional information-decision-value estimate in [0,1].
	// VOIEstimated distinguishes a real zero from "not estimated".
	VOI          float64 `json:"voi,omitempty"`
	VOIEstimated bool    `json:"voi_estimated,omitempty"`
	// AssignedTo is the delegation/agent ID that currently owns the work.
	AssignedTo string `json:"assigned_to,omitempty"`
	// LeaseUntil is when a claim at HypothesisTesting expires. An expired
	// lease returns the hypothesis to queued (deterministic reclaim, audited
	// as OpReclaimHypothesis) so a stalled or crashed owner cannot strand
	// work forever. Zero means "no lease recorded" (legacy rows): never
	// reclaimed automatically.
	LeaseUntil time.Time `json:"lease_until,omitempty"`
	// NextAction is the next-best concrete step for whoever picks this up.
	NextAction string `json:"next_action,omitempty"`
	// Attempts counts exploit attempts spent (for exhaustion/economics).
	Attempts int `json:"attempts"`

	Evidence []Evidence `json:"evidence,omitempty"`

	// Origin records who created it: "recon", "auto", "llm", or a specialist.
	Origin    string    `json:"origin,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// clone returns a deep copy so callers cannot mutate stored state without going
// through the store's locked methods.
func (h *Hypothesis) clone() Hypothesis {
	cp := *h
	if h.Preconditions != nil {
		cp.Preconditions = append([]string(nil), h.Preconditions...)
	}
	if h.Evidence != nil {
		cp.Evidence = append([]Evidence(nil), h.Evidence...)
		for i := range cp.Evidence {
			if h.Evidence[i].Supports != nil {
				cp.Evidence[i].Supports = append([]string(nil), h.Evidence[i].Supports...)
			}
		}
	}
	return cp
}

// LedgerStore is the durable, concurrency-safe hypothesis/evidence graph for a
// single scan. It is shared by the coordinator and every specialist (they hold
// the same *ScanContext), persists to <scanDir>/ledger.json via atomic writes,
// and survives process restart/resume.
//
// It mirrors NoteStore's locking discipline: mutations serialize on mu and the
// durable file write happens INSIDE the same critical section, so snapshots
// reach disk in exactly the order they were taken. Writing outside mu (a
// separate writeMu) is not enough: two mutations could marshal ordered
// snapshots A then B, yet reach the file out of order, leaving a stale
// snapshot as the final on-disk state. Readers taking mu.RLock pause briefly
// during the fsync, which NoteStore accepted for the same durability reason.
type LedgerStore struct {
	mu      sync.RWMutex
	hyps    map[string]*Hypothesis
	order   []string          // insertion order for stable rendering
	byDedup map[string]string // dedupKey -> hypothesis ID
	seq     int               // hypothesis ID sequence
	evSeq   int               // evidence ID sequence

	scanID      string
	instanceID  string // run identity (D11) — stamps snapshots and drives continuity load
	persistPath string
	journal     *domainjournal.Journal
	voiWeight   float64
	leaseTTL    time.Duration // hypothesis claim lease (0 = defaultHypothesisLease)

	injectionMu   sync.Mutex
	injectionSeen map[string]struct{} // dedupe key for injection observations

	// deviations is a bounded in-memory projection of AppendDeviation records
	// for the main agent's decision inbox. The domain journal remains the
	// audit authority; this projection is not persisted or replayed.
	deviations []Deviation
	// infoReads / actionAttempts are bounded R3 trace projections. The journal
	// is the audit authority; these are read-only joins for tracing.
	infoReads      []InfoRead
	actionAttempts []ActionAttempt
	actionSeq      int
}

// Deviation is one executor-recorded departure from its assigned intent.
type Deviation struct {
	IntentID string
	AgentID  string
	Text     string
	At       time.Time
}

// maxDeviationProjection bounds the in-memory deviation list.
const maxDeviationProjection = 500

// Deviations returns a copy of the in-memory deviation projection.
func (ls *LedgerStore) Deviations() []Deviation {
	if ls == nil {
		return nil
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return append([]Deviation(nil), ls.deviations...)
}

// defaultHypothesisLease bounds how long a claimed (testing) hypothesis may
// sit without activity before reclaim returns it to the queue.
const defaultHypothesisLease = 15 * time.Minute

// SetLeaseTTL overrides the hypothesis claim lease duration (<=0 resets to the
// default).
func (ls *LedgerStore) SetLeaseTTL(d time.Duration) {
	ls.mu.Lock()
	ls.leaseTTL = d
	ls.mu.Unlock()
}

func (ls *LedgerStore) effectiveLeaseLocked() time.Duration {
	if ls.leaseTTL > 0 {
		return ls.leaseTTL
	}
	return defaultHypothesisLease
}

// NewLedgerStore creates an empty ledger with no persistence configured.
func NewLedgerStore() *LedgerStore {
	return &LedgerStore{
		hyps:    make(map[string]*Hypothesis),
		byDedup: make(map[string]string),
	}
}

// SetPersistPath configures disk persistence. An empty dir disables it.
//
// Path policy mirrors NoteStore: the path is dir + "/ledger.json" with no
// user-supplied component, and in production dir is the per-scan ScanDir (an
// Allow_List descendant by construction), so no sandbox.CheckResolve is needed
// here (and cannot be called — sandbox imports scanctx).
func (ls *LedgerStore) SetPersistPath(dir string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if dir != "" {
		ls.persistPath = filepath.Join(dir, "ledger.json")
	} else {
		ls.persistPath = ""
	}
}

// SetScanID configures the scan identity used for stable InfoItem IDs. It is
// separate from the persistence directory so callers never have to infer a
// scan ID from a filesystem path.
func (ls *LedgerStore) SetScanID(scanID string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.scanID = truncate(strings.TrimSpace(scanID), maxHypothesisFieldLen)
}

// SetInstanceID stamps the run identity (D11) on the ledger. With the PG
// backend registered, snapshots carry it and a re-dispatched session's first
// LoadFromDisk inherits the previous sibling session's snapshot via the
// instance continuity read instead of starting empty. The domain journal is
// stamped too (R6.1): run_journal rows carry the same identity for run-level
// aggregation; the file mode ignores the metadata.
func (ls *LedgerStore) SetInstanceID(instanceID string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.instanceID = strings.TrimSpace(instanceID)
	if ls.journal != nil {
		ls.journal.SetInstanceID(ls.instanceID)
	}
}

// Upsert inserts a new hypothesis or merges into an existing one that shares
// the same dedup key. It returns a copy of the stored hypothesis.
//
// Dedup: if in.DedupKey is empty it is derived from (VulnClass, Endpoint,
// Parameter, Role). Non-empty scalar fields on `in` overwrite blanks on the
// existing record; identity fields are never blanked once set. Status and
// Confidence are otherwise only advanced by the dedicated methods, so Upsert
// never regresses a proven hypothesis back to queued; the one exception is a
// model-recorded lead merging into an engine seed, which may only raise them
// (see mergeFrom).
func (ls *LedgerStore) Upsert(in Hypothesis) Hypothesis {
	out, err := ls.UpsertAs(in, engineActor("ledger"))
	if err != nil {
		log.Printf("[ledger] Warning: upsert not committed: %v", err)
	}
	return out
}

// UpsertAs records the mutation with an explicit actor and returns an error if
// the durable audit append or ledger snapshot fails.
func (ls *LedgerStore) UpsertAs(in Hypothesis, actor domainjournal.Actor) (Hypothesis, error) {
	now := time.Now().UTC()
	in.sanitize()
	key := in.DedupKey
	if key == "" {
		key = dedupKey(in.VulnClass, in.Endpoint, in.Parameter, in.Role)
	}

	ls.mu.Lock()
	defer ls.mu.Unlock()

	var existing *Hypothesis
	if id, ok := ls.byDedup[key]; ok {
		existing = ls.hyps[id]
	}
	var (
		working *Hypothesis
		before  *domainjournal.State
		newID   string
	)
	if existing != nil {
		before = existing.auditState()
		cp := existing.clone()
		cp.mergeFrom(in)
		cp.UpdatedAt = now
		working = &cp
	} else {
		ls.seq++
		newID = fmt.Sprintf("H-%d", ls.seq)
		h := in
		h.ID = newID
		h.DedupKey = key
		if h.Status == "" {
			h.Status = HypothesisQueued
		}
		if h.CreatedAt.IsZero() {
			h.CreatedAt = now
		}
		h.UpdatedAt = now
		working = &h
	}

	op := domainjournal.OpSeedHypothesis
	if IsModelOrigin(working.Origin) {
		op = domainjournal.OpRecordHypothesis
	}
	if err := ls.appendAuditLocked(domainjournal.Record{
		Actor:        actor,
		Operation:    op,
		HypothesisID: working.ID,
		Summary:      fmt.Sprintf("%s %s", op, formatHypothesisBrief(working)),
		Before:       before,
		After:        working.auditState(),
	}); err != nil {
		if existing == nil {
			ls.seq--
		}
		return Hypothesis{}, err
	}

	if existing != nil {
		*existing = *working
	} else {
		ls.hyps[newID] = working
		ls.order = append(ls.order, newID)
		ls.byDedup[key] = newID
	}
	out := working.clone()
	if err := ls.persistLocked(); err != nil {
		return out, err
	}
	return out, nil
}

// AddEvidence appends an observation to a hypothesis and lightly nudges the
// hypothesis's confidence toward the evidence's own confidence. Returns false
// if the hypothesis does not exist.
func (ls *LedgerStore) AddEvidence(hypID string, ev Evidence) bool {
	return ls.AddEvidenceAs(hypID, ev, engineActor("ledger")) == nil
}

// AddEvidenceAs appends evidence and durably audits it before committing the
// in-memory mutation.
func (ls *LedgerStore) AddEvidenceAs(hypID string, ev Evidence, actor domainjournal.Actor) error {
	now := time.Now().UTC()
	ev.sanitize()
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = now
	}

	ls.mu.Lock()
	defer ls.mu.Unlock()
	h := ls.hyps[hypID]
	if h == nil {
		return fmt.Errorf("scanctx: unknown hypothesis %q", hypID)
	}
	before := h.auditState()
	cp := h.clone()
	nextSeq := ls.evSeq + 1
	ev.ID = fmt.Sprintf("EV-%d", nextSeq)
	// Keep an explicit InfoItem/Evidence link supplied by the caller (R3
	// trace anchor). Only engine-created evidence with no link gets the
	// deterministic stable ID.
	if ls.scanID != "" && strings.TrimSpace(ev.InfoItemID) == "" {
		ev.InfoItemID = infoitem.StableEvidenceID(ls.scanID, ev.ID)
	}
	// Supports may only point at evidence already stored on this hypothesis.
	// Unknown references are ignored here; the model-facing tool validates and
	// reports them before calling the store.
	existing := make(map[string]bool, len(cp.Evidence))
	for _, prior := range cp.Evidence {
		existing[prior.ID] = true
	}
	ev.Supports = validSupportIDs(ev.Supports, existing)
	cp.Evidence = append(cp.Evidence, ev)
	capEvidence(&cp)
	// Evidence quality raises belief but never lowers it here; rejection is an
	// explicit status transition, not a side effect of adding an observation.
	if ev.Confidence > cp.Confidence {
		cp.Confidence = clampConfidence(ev.Confidence)
	}
	cp.UpdatedAt = now

	committed := ev
	for _, stored := range cp.Evidence {
		if stored.ID == ev.ID {
			committed = stored
			break
		}
	}
	item, relations := ls.evidenceInfoItemLocked(&cp, committed)
	if err := ls.appendAuditLocked(domainjournal.Record{
		Actor:        actor,
		Operation:    domainjournal.OpAddEvidence,
		HypothesisID: hypID,
		EvidenceID:   ev.ID,
		Summary:      fmt.Sprintf("evidence %s [%s/%s]", ev.ID, ev.Kind, ev.Nature),
		Before:       before,
		After:        cp.auditState(),
		InfoItem:     item,
		Relations:    relations,
	}); err != nil {
		return err
	}

	*h = cp
	ls.evSeq = nextSeq
	return ls.persistLocked()
}

// SetStatus transitions a hypothesis's lifecycle state and optionally records
// the next-best action. Returns false if the hypothesis does not exist.
func (ls *LedgerStore) SetStatus(hypID string, status HypothesisStatus, nextAction string) bool {
	return ls.SetStatusAs(hypID, status, nextAction, engineActor("ledger")) == nil
}

// SetStatusAs transitions lifecycle state with an explicit audit actor.
func (ls *LedgerStore) SetStatusAs(hypID string, status HypothesisStatus, nextAction string, actor domainjournal.Actor) error {
	if !validHypothesisStatuses[status] {
		status = NormalizeHypothesisStatus(string(status))
	}
	_, err := ls.mutateHypothesisAs(hypID, actor, domainjournal.OpSetStatus, "set status "+string(status), func(h *Hypothesis) {
		h.Status = status
		if strings.TrimSpace(nextAction) != "" {
			h.NextAction = truncate(nextAction, maxSummaryLen)
		}
		if status == HypothesisProven && h.Confidence < 1.0 {
			h.Confidence = 1.0
		}
		if status == HypothesisRejected {
			h.Confidence = 0
		}
	})
	return err
}

// MarkAssigned records lane ownership WITHOUT claiming: the hypothesis stays
// queued (still schedulable by anyone) but carries the assigned lane so a
// specialist's finish gate can see assigned-but-never-claimed work, and the
// coordinator can audit lane coverage. Used by the engine's specialist-wave
// lane seeding; the owning agent still claims via Claim/Assign when it starts
// the work. Returns false if the hypothesis does not exist.
func (ls *LedgerStore) MarkAssigned(hypID, agentID string) bool {
	return ls.MarkAssignedAs(hypID, agentID, engineActor("ledger")) == nil
}

// MarkAssignedAs records lane ownership with an explicit actor.
func (ls *LedgerStore) MarkAssignedAs(hypID, agentID string, actor domainjournal.Actor) error {
	owner := truncate(strings.TrimSpace(agentID), maxHypothesisFieldLen)
	_, err := ls.mutateHypothesisAs(hypID, actor, domainjournal.OpAssignHypothesis, "mark assigned "+owner, func(h *Hypothesis) {
		h.AssignedTo = owner
	})
	return err
}

// Assign records the owning agent/delegation ID and moves a queued hypothesis
// into "testing". Returns false if the hypothesis does not exist.
func (ls *LedgerStore) Assign(hypID, agentID string) bool {
	return ls.AssignAs(hypID, agentID, engineActor("ledger")) == nil
}

// AssignAs records ownership and queued→testing with an explicit actor.
func (ls *LedgerStore) AssignAs(hypID, agentID string, actor domainjournal.Actor) error {
	owner := truncate(strings.TrimSpace(agentID), maxHypothesisFieldLen)
	_, err := ls.mutateHypothesisAs(hypID, actor, domainjournal.OpAssignHypothesis, "assign "+owner, func(h *Hypothesis) {
		h.AssignedTo = owner
		if h.Status == HypothesisQueued {
			h.Status = HypothesisTesting
		}
	})
	return err
}

// ClaimNext atomically selects the highest-confidence queued hypothesis that
// matches vulnClass, assigns it to agentID, and moves it to testing. Selection
// and assignment happen under one lock so concurrent specialists cannot both
// observe the same queued item and overwrite one another's ownership.
// An empty vulnClass matches every class.
func (ls *LedgerStore) ClaimNext(vulnClass, agentID string) (Hypothesis, bool) {
	got, claimed, err := ls.ClaimNextAs(vulnClass, agentID, modelActor(agentID))
	if err != nil {
		log.Printf("[ledger] Warning: claim not committed: %v", err)
		return Hypothesis{}, false
	}
	return got, claimed
}

// ClaimNextAs atomically claims with an explicit actor and audits the claim
// before committing ownership.
func (ls *LedgerStore) ClaimNextAs(vulnClass, agentID string, actor domainjournal.Actor) (Hypothesis, bool, error) {
	return ls.claimNextAs(vulnClass, agentID, false, actor)
}

// ClaimNextUnassignedAs is the idle-worker borrowing path: it only considers
// queued hypotheses that carry no lane owner (or are already owned by this
// agent). MarkAssigned lane seeding stays a soft hint — a lane with nothing
// claimable must not strand another lane's untaken work, which was one cause of
// "two workers, only one is doing anything".
func (ls *LedgerStore) ClaimNextUnassignedAs(agentID string, actor domainjournal.Actor) (Hypothesis, bool, error) {
	return ls.claimNextAs("", agentID, true, actor)
}

func (ls *LedgerStore) claimNextAs(vulnClass, agentID string, unassignedOnly bool, actor domainjournal.Actor) (Hypothesis, bool, error) {
	classFilter := strings.ToLower(strings.TrimSpace(vulnClass))
	owner := truncate(strings.TrimSpace(agentID), maxHypothesisFieldLen)

	ls.mu.Lock()
	defer ls.mu.Unlock()
	// Deterministic reclaim before selection: an expired claim returns to
	// queued so a stalled owner cannot strand the unit (charter: claim is a
	// lease, not ownership forever). Audited as OpReclaimHypothesis.
	now := time.Now().UTC()
	if err := ls.reclaimExpiredLocked(now, actor); err != nil {
		return Hypothesis{}, false, err
	}
	var picked *Hypothesis
	for _, id := range ls.order {
		h := ls.hyps[id]
		if h == nil || h.Status != HypothesisQueued {
			continue
		}
		if !h.ClaimEligible() {
			continue // R2: coverage seeds are prompts, not schedulable beliefs
		}
		if classFilter != "" && h.VulnClass != classFilter {
			continue
		}
		if unassignedOnly && h.AssignedTo != "" && h.AssignedTo != owner {
			continue
		}
		// Strictly better score replaces the candidate; equal scores keep the
		// earlier insertion, matching Schedulable's stable ordering.
		if betterHypothesis(h, picked, ls.voiWeight) {
			picked = h
		}
	}
	if picked == nil {
		return Hypothesis{}, false, nil
	}
	before := picked.auditState()
	cp := picked.clone()
	cp.AssignedTo = owner
	cp.Status = HypothesisTesting
	cp.LeaseUntil = now.Add(ls.effectiveLeaseLocked())
	cp.UpdatedAt = now
	if err := ls.appendAuditLocked(domainjournal.Record{
		Actor:        actor,
		Operation:    domainjournal.OpClaimHypothesis,
		HypothesisID: cp.ID,
		Summary:      "claim " + formatHypothesisBrief(&cp),
		Before:       before,
		After:        cp.auditState(),
	}); err != nil {
		return Hypothesis{}, false, err
	}
	*picked = cp
	out := picked.clone()
	if err := ls.persistLocked(); err != nil {
		return out, true, err
	}
	return out, true, nil
}

// reclaimExpiredLocked returns every testing hypothesis whose lease elapsed to
// the queued pool, clearing the owner. Callers must hold ls.mu. Each reclaim
// is audited before it commits.
func (ls *LedgerStore) reclaimExpiredLocked(now time.Time, actor domainjournal.Actor) error {
	for _, id := range ls.order {
		h := ls.hyps[id]
		if h == nil || h.Status != HypothesisTesting || h.LeaseUntil.IsZero() || now.Before(h.LeaseUntil) {
			continue
		}
		before := h.auditState()
		cp := h.clone()
		cp.Status = HypothesisQueued
		cp.AssignedTo = ""
		cp.LeaseUntil = time.Time{}
		cp.UpdatedAt = now
		if err := ls.appendAuditLocked(domainjournal.Record{
			Actor:        actor,
			Operation:    domainjournal.OpReclaimHypothesis,
			HypothesisID: cp.ID,
			Summary:      "lease expired; returned to queue",
			Before:       before,
			After:        cp.auditState(),
		}); err != nil {
			return err
		}
		*h = cp
	}
	return nil
}

// RecordAttempt increments the exploit-attempt counter (economics/exhaustion).
func (ls *LedgerStore) RecordAttempt(hypID string) bool {
	return ls.RecordAttemptAs(hypID, engineActor("ledger")) == nil
}

// RecordAttemptAs increments attempts with an explicit actor.
func (ls *LedgerStore) RecordAttemptAs(hypID string, actor domainjournal.Actor) error {
	_, err := ls.mutateHypothesisAs(hypID, actor, domainjournal.OpRecordAttempt, "record attempt", func(h *Hypothesis) {
		h.Attempts++
		// Activity renews the claim lease: an owner that keeps working never
		// loses the unit; only silence lets reclaim take it back.
		if h.Status == HypothesisTesting {
			h.LeaseUntil = time.Now().UTC().Add(ls.effectiveLeaseLocked())
		}
	})
	return err
}

// Get returns a copy of a hypothesis by ID.
func (ls *LedgerStore) Get(id string) (Hypothesis, bool) {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	h, ok := ls.hyps[id]
	if !ok {
		return Hypothesis{}, false
	}
	return h.clone(), true
}

// All returns copies of every hypothesis in insertion order.
func (ls *LedgerStore) All() []Hypothesis {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	out := make([]Hypothesis, 0, len(ls.order))
	for _, id := range ls.order {
		if h := ls.hyps[id]; h != nil {
			out = append(out, h.clone())
		}
	}
	return out
}

// Schedulable returns the non-terminal hypotheses that are candidates for a
// specialist to pick up (queued or blocked), highest-confidence first, then by
// insertion order. "testing" is excluded because it is already owned.
func (ls *LedgerStore) Schedulable(limit int) []Hypothesis {
	ls.mu.RLock()
	weight := ls.voiWeight
	cands := make([]Hypothesis, 0, len(ls.order))
	for _, id := range ls.order {
		h := ls.hyps[id]
		if h == nil {
			continue
		}
		if h.Status == HypothesisQueued || h.Status == HypothesisBlocked {
			if !h.ClaimEligible() {
				continue // R2: sources of coverage prompts are not schedulable
			}
			cands = append(cands, h.clone())
		}
	}
	ls.mu.RUnlock()

	sort.SliceStable(cands, func(i, j int) bool {
		si := hypothesisRankScore(&cands[i], weight)
		sj := hypothesisRankScore(&cands[j], weight)
		if si != sj {
			return si > sj
		}
		// Queued before blocked when score ties (blocked needs a
		// precondition that may not be met yet).
		if cands[i].Status != cands[j].Status {
			return cands[i].Status == HypothesisQueued
		}
		return false
	})
	if limit > 0 && len(cands) > limit {
		cands = cands[:limit]
	}
	return cands
}

// Counts returns the number of hypotheses in each status.
func (ls *LedgerStore) Counts() map[HypothesisStatus]int {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	out := make(map[HypothesisStatus]int)
	for _, id := range ls.order {
		if h := ls.hyps[id]; h != nil {
			out[h.Status]++
		}
	}
	return out
}

// Len returns the number of hypotheses.
func (ls *LedgerStore) Len() int {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return len(ls.hyps)
}

// EvidenceCount returns the total number of evidence records across every
// hypothesis. It is the cheap "did anything actually get recorded" probe the
// finish gate uses; per-hypothesis caps do not affect the count's meaning.
func (ls *LedgerStore) EvidenceCount() int {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	n := 0
	for _, id := range ls.order {
		if h := ls.hyps[id]; h != nil {
			n += len(h.Evidence)
		}
	}
	return n
}

// Reset clears all hypotheses (used when a scan is explicitly reset).
func (ls *LedgerStore) Reset() {
	if err := ls.ResetAs(domainjournal.Actor{Kind: domainjournal.ActorSystem, Source: "ledger.reset"}); err != nil {
		log.Printf("[ledger] Warning: reset not committed: %v", err)
	}
}

// ResetAs audits the reset before clearing in-memory state.
func (ls *LedgerStore) ResetAs(actor domainjournal.Actor) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	count := len(ls.hyps)
	if err := ls.appendAuditLocked(domainjournal.Record{
		Actor:     actor,
		Operation: domainjournal.OpResetLedger,
		Summary:   fmt.Sprintf("reset ledger (%d hypotheses)", count),
	}); err != nil {
		return err
	}
	ls.hyps = make(map[string]*Hypothesis)
	ls.byDedup = make(map[string]string)
	ls.order = nil
	ls.seq = 0
	ls.evSeq = 0
	return ls.persistLocked()
}

// Merge folds another ledger's hypotheses into this one (child -> parent
// rollup), deduplicating by dedup key. Evidence from the source is appended to
// the matching parent hypothesis; new hypotheses are inserted. Returns the
// number of source hypotheses that resulted in an insert or an evidence merge.
// Mirrors reporting.MergeVulnsToContext for the ledger.
func (ls *LedgerStore) Merge(other *LedgerStore) int {
	if other == nil || other == ls {
		return 0
	}
	src := other.All()
	merged := 0
	var mergedItems []infoitem.Item
	var mergedRelations []infoitem.Relation
	for _, s := range src {
		key := s.DedupKey
		if key == "" {
			key = dedupKey(s.VulnClass, s.Endpoint, s.Parameter, s.Role)
		}
		ls.mu.Lock()
		if id, ok := ls.byDedup[key]; ok && ls.hyps[id] != nil {
			dst := ls.hyps[id]
			dst.mergeFrom(s)
			// Advance status toward the more-settled/proven of the two.
			if statusRank(s.Status) > statusRank(dst.Status) {
				dst.Status = s.Status
			}
			if s.Confidence > dst.Confidence {
				dst.Confidence = s.Confidence
			}
			remap := make(map[string]string, len(s.Evidence))
			for _, ev := range s.Evidence {
				ls.evSeq++
				remap[ev.ID] = fmt.Sprintf("EV-%d", ls.evSeq)
			}
			for _, ev := range s.Evidence {
				ev.ID = remap[ev.ID]
				ev.Supports = remapSupportIDs(ev.Supports, remap)
				ev.Nature = NormalizeEvidenceNature(ev.Nature)
				if ls.scanID != "" {
					ev.InfoItemID = infoitem.StableEvidenceID(ls.scanID, ev.ID)
				}
				dst.Evidence = append(dst.Evidence, ev)
			}
			capEvidence(dst)
			dst.UpdatedAt = time.Now().UTC()
			present := make(map[string]bool, len(dst.Evidence))
			for _, ev := range dst.Evidence {
				present[ev.ID] = true
			}
			for _, ev := range s.Evidence {
				newID := remap[ev.ID]
				if !present[newID] {
					continue
				}
				ev.ID = newID
				ev.Supports = remapSupportIDs(ev.Supports, remap)
				if item, rels := ls.evidenceInfoItemLocked(dst, ev); item != nil {
					mergedItems = append(mergedItems, *item)
					mergedRelations = append(mergedRelations, rels...)
				}
			}
		} else {
			ls.seq++
			id := fmt.Sprintf("H-%d", ls.seq)
			h := s
			h.ID = id
			h.DedupKey = key
			// Re-ID evidence into this store's sequence and remap supports so an
			// inferred item never points at a foreign/old evidence ID.
			remap := make(map[string]string, len(h.Evidence))
			for i := range h.Evidence {
				ls.evSeq++
				oldID := h.Evidence[i].ID
				h.Evidence[i].ID = fmt.Sprintf("EV-%d", ls.evSeq)
				remap[oldID] = h.Evidence[i].ID
			}
			for i := range h.Evidence {
				h.Evidence[i].Supports = remapSupportIDs(h.Evidence[i].Supports, remap)
				h.Evidence[i].Nature = NormalizeEvidenceNature(h.Evidence[i].Nature)
				if ls.scanID != "" {
					h.Evidence[i].InfoItemID = infoitem.StableEvidenceID(ls.scanID, h.Evidence[i].ID)
				}
			}
			for i := range h.Evidence {
				if item, rels := ls.evidenceInfoItemLocked(&h, h.Evidence[i]); item != nil {
					mergedItems = append(mergedItems, *item)
					mergedRelations = append(mergedRelations, rels...)
				}
			}
			ls.hyps[id] = &h
			ls.order = append(ls.order, id)
			ls.byDedup[key] = id
		}
		merged++
		ls.mu.Unlock()
	}
	ls.mu.Lock()
	if merged > 0 {
		if err := ls.appendAuditLocked(domainjournal.Record{
			Actor:     engineActor("ledger.merge"),
			Operation: domainjournal.OpMergeLedger,
			Summary:   fmt.Sprintf("merge ledger (%d hypotheses, %d evidence items)", merged, len(mergedItems)),
			InfoItems: mergedItems,
			Relations: mergedRelations,
		}); err != nil {
			log.Printf("[ledger] Warning: merge audit failed: %v", err)
		}
	}
	if err := ls.persistLocked(); err != nil {
		log.Printf("[ledger] Warning: merged ledger snapshot failed: %v", err)
	}
	ls.mu.Unlock()
	return merged
}

// FormatForContext renders a compact, LLM-friendly view of the open work and
// proven findings for injection into agent context. Empty when the ledger is
// empty.
func (ls *LedgerStore) FormatForContext() string {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	if len(ls.order) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("=== HYPOTHESIS LEDGER (shared, durable) ===\n")
	counts := map[HypothesisStatus]int{}
	for _, id := range ls.order {
		if h := ls.hyps[id]; h != nil {
			counts[h.Status]++
		}
	}
	b.WriteString(fmt.Sprintf("queued=%d testing=%d proven=%d rejected=%d blocked=%d exhausted=%d\n",
		counts[HypothesisQueued], counts[HypothesisTesting], counts[HypothesisProven],
		counts[HypothesisRejected], counts[HypothesisBlocked], counts[HypothesisExhausted]))
	shown := 0
	for _, id := range ls.order {
		h := ls.hyps[id]
		if h == nil || h.Status == HypothesisRejected || h.Status == HypothesisExhausted {
			continue
		}
		if shown >= 25 {
			b.WriteString("… (more hypotheses; use read_ledger for the full list)\n")
			break
		}
		loc := h.Endpoint
		if h.Parameter != "" {
			loc += " [" + h.Parameter + "]"
		}
		if h.Role != "" {
			loc += " as " + h.Role
		}
		b.WriteString(fmt.Sprintf("• %s [%s] %s conf=%.2f %s", h.ID, h.Status, h.VulnClass, h.Confidence, strings.TrimSpace(loc)))
		if h.NextAction != "" {
			b.WriteString(" → " + h.NextAction)
		}
		b.WriteString("\n")
		shown++
	}
	b.WriteString("=== END LEDGER ===")
	return b.String()
}

// LoadFromDisk merges the durable ledger into this (empty or partial) store.
// Snapshot source order with the PG backend registered (charter v3.0 §6.3):
//
//  1. this scan's own snapshot;
//  2. the newest sibling session's snapshot for the stamped instance — the
//     D11 continuity read: a re-dispatched session has no snapshot of its own
//     yet and inherits the previous session's chain instead of starting empty
//     (the structural replacement for the ledger.json continuity copy);
//  3. the legacy ledger.json file — straggler resilience only (crash
//     leftovers between backend registration and import); never a write
//     target while a backend is registered.
//
// Without a backend the behavior is byte-identical to v2.1 file mode.
// Existing in-memory hypotheses (by dedup key) are preserved; only unseen
// hypotheses from the snapshot are inserted, so a resume never clobbers live
// state. Returns the number of hypotheses loaded.
func (ls *LedgerStore) LoadFromDisk() int {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	if b := currentLedgerBackend(); b != nil {
		if data, _, ok, err := b.LoadLedgerSnapshot(ls.scanID); err != nil {
			log.Printf("[ledger] Warning: PG snapshot load failed for %s: %v", ls.scanID, err)
		} else if ok {
			return ls.mergeSnapshotLocked(data, fmt.Sprintf("pg snapshot %s", ls.scanID))
		}
		if ls.instanceID != "" {
			if data, _, source, ok, err := b.LoadLatestLedgerSnapshotForInstance(ls.instanceID, ls.scanID); err != nil {
				log.Printf("[ledger] Warning: PG continuity load failed for %s/%s: %v", ls.instanceID, ls.scanID, err)
			} else if ok {
				return ls.mergeSnapshotLocked(data, fmt.Sprintf("pg continuity %s (from sibling %s)", ls.instanceID, source))
			}
		}
	}

	if ls.persistPath == "" {
		return 0
	}
	data, err := os.ReadFile(ls.persistPath)
	if err != nil {
		return 0
	}
	return ls.mergeSnapshotLocked(data, ls.persistPath)
}

// mergeSnapshotLocked decodes one ledger snapshot and merges unseen
// hypotheses into the store. Caller holds ls.mu.
func (ls *LedgerStore) mergeSnapshotLocked(data []byte, source string) int {
	var snap ledgerSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		log.Printf("[ledger] Warning: failed to parse snapshot from %s: %v", source, err)
		return 0
	}
	count := 0
	for _, h := range snap.Hypotheses {
		if h == nil {
			continue
		}
		// Ledgers written before endpoint keys kept their path case store a
		// lowercased key; re-derive it so new records for the same route merge.
		key := h.DedupKey
		if derived := dedupKey(h.VulnClass, h.Endpoint, h.Parameter, h.Role); key == "" || strings.EqualFold(key, derived) {
			key = derived
		}
		h.DedupKey = key
		if _, exists := ls.byDedup[key]; exists {
			continue // do not clobber live state
		}
		if h.ID == "" {
			ls.seq++
			h.ID = fmt.Sprintf("H-%d", ls.seq)
		}
		for i := range h.Evidence {
			h.Evidence[i].Nature = NormalizeEvidenceNature(h.Evidence[i].Nature)
			if h.Evidence[i].InfoItemID == "" && ls.scanID != "" {
				h.Evidence[i].InfoItemID = infoitem.StableEvidenceID(ls.scanID, h.Evidence[i].ID)
			}
		}
		ls.hyps[h.ID] = h
		ls.order = append(ls.order, h.ID)
		ls.byDedup[key] = h.ID
		count++
	}
	// Advance sequences past anything loaded so new IDs never collide.
	if snap.Seq > ls.seq {
		ls.seq = snap.Seq
	}
	if snap.EvSeq > ls.evSeq {
		ls.evSeq = snap.EvSeq
	}
	if count > 0 {
		log.Printf("[ledger] Loaded %d hypotheses from: %s", count, source)
	}
	return count
}

// ledgerSnapshot is the on-disk JSON shape.
type ledgerSnapshot struct {
	Seq        int           `json:"seq"`
	EvSeq      int           `json:"ev_seq"`
	DomainSeq  uint64        `json:"domain_seq,omitempty"`
	Order      []string      `json:"order"`
	Hypotheses []*Hypothesis `json:"hypotheses"`
}

// marshalLocked serializes the store. Must be called with ls.mu held. Returns
// (nil, "") when persistence is disabled.
func (ls *LedgerStore) marshalLocked() ([]byte, string) {
	if ls.persistPath == "" && currentLedgerBackend() == nil {
		return nil, ""
	}
	snap := ledgerSnapshot{
		Seq:   ls.seq,
		EvSeq: ls.evSeq,
		Order: append([]string(nil), ls.order...),
	}
	if ls.journal != nil {
		snap.DomainSeq = ls.journal.LastSeq()
	}
	for _, id := range ls.order {
		if h := ls.hyps[id]; h != nil {
			snap.Hypotheses = append(snap.Hypotheses, h)
		}
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		log.Printf("[ledger] Warning: failed to marshal ledger: %v", err)
		return nil, ""
	}
	return data, ls.persistPath
}

// writeFile persists serialized data atomically. Must be called with ls.mu
// held, as part of the same critical section as the mutation it accompanies:
// that ordering guarantee is what keeps an older snapshot from overwriting a
// newer one (see the LedgerStore doc comment). Uses storage.WriteAtomic so a
// crash mid-write can never leave a torn ledger.json. A failed write logs a
// warning and leaves the in-memory state authoritative for this process —
// the next successful mutation re-persists the full snapshot.
func (ls *LedgerStore) writeFile(data []byte, path string) error {
	if data == nil || path == "" {
		return nil
	}
	if err := storage.WriteAtomic(path, data); err != nil {
		log.Printf("[ledger] Warning: failed to save ledger to %s: %v", path, err)
		return err
	}
	return nil
}

// --- helpers ---

// statusRank orders statuses by "settledness" for merge conflict resolution:
// proven wins over everything; otherwise more-progressed states win.
func statusRank(s HypothesisStatus) int {
	switch s {
	case HypothesisProven:
		return 5
	case HypothesisTesting:
		return 3
	case HypothesisBlocked:
		return 2
	case HypothesisExhausted:
		return 2
	case HypothesisRejected:
		return 1
	case HypothesisQueued:
		return 0
	default:
		return 0
	}
}

// mergeFrom copies non-empty scalar identity/metadata fields from src into h
// without blanking existing values. It does not touch ID, DedupKey, status,
// confidence, attempts, or timestamps (those are managed by the store).
func (h *Hypothesis) mergeFrom(src Hypothesis) {
	if h.Title == "" && src.Title != "" {
		h.Title = src.Title
	}
	if h.VulnClass == "" && src.VulnClass != "" {
		h.VulnClass = src.VulnClass
	}
	if h.Target == "" && src.Target != "" {
		h.Target = src.Target
	}
	if h.Endpoint == "" && src.Endpoint != "" {
		h.Endpoint = src.Endpoint
	}
	if h.Parameter == "" && src.Parameter != "" {
		h.Parameter = src.Parameter
	}
	if h.Role == "" && src.Role != "" {
		h.Role = src.Role
	}
	if h.DataFlow == "" && src.DataFlow != "" {
		h.DataFlow = src.DataFlow
	}
	if h.RequiredPrivilege == "" && src.RequiredPrivilege != "" {
		h.RequiredPrivilege = src.RequiredPrivilege
	}
	if h.Baseline == "" && src.Baseline != "" {
		h.Baseline = src.Baseline
	}
	if h.Falsification == "" && src.Falsification != "" {
		h.Falsification = src.Falsification
	}
	if h.NextObservation == "" && src.NextObservation != "" {
		h.NextObservation = src.NextObservation
	}
	if src.NextAction != "" {
		h.NextAction = src.NextAction
	}
	if src.VOIEstimated && (!h.VOIEstimated || src.VOI > h.VOI) {
		h.VOI = src.VOI
		h.VOIEstimated = true
	}
	if len(src.Preconditions) > 0 {
		h.Preconditions = mergeStringSet(h.Preconditions, src.Preconditions)
	}
	if h.Origin == "" && src.Origin != "" {
		h.Origin = src.Origin
	}
	// A lead the model records itself can land on an engine-seeded placeholder
	// with the same class/endpoint/parameter/role. The model's record takes the
	// placeholder over: it adopts the model origin and the model's confidence
	// and may advance queued → testing. It only ever raises — a settled status
	// or a higher confidence is never regressed (dedicated methods own those).
	if IsModelOrigin(src.Origin) {
		if !IsModelOrigin(h.Origin) {
			h.Origin = src.Origin
		}
		if src.Confidence > h.Confidence {
			h.Confidence = src.Confidence
		}
		if h.Status == HypothesisQueued && src.Status == HypothesisTesting {
			h.Status = HypothesisTesting
		}
	}
}

// IsModelOrigin reports whether an Origin marks a hypothesis the model recorded
// itself (record_hypothesis from the coordinator, "coordinator", or from a
// delegated specialist, "agent_<id>"), as opposed to engine-generated seeds,
// verifier records or imports.
func IsModelOrigin(origin string) bool {
	return origin == "coordinator" || strings.HasPrefix(origin, "agent_")
}

// coverageSeedOrigins are the engine's coverage-matrix seed origins. R2: a row
// with one of these origins is a Coverage opportunity, not a Formal Hypothesis,
// so it never enters the schedulable/claim pool. A model recording the same
// dedup key upgrades the row's origin via mergeFrom, which is the only way a
// seed becomes claimable.
var coverageSeedOrigins = map[string]bool{
	"auto":               true,
	"auto-plan":          true,
	"auto-plan-surface":  true,
	"auto-surface-param": true,
	"engine":             true,
	"recon":              true,
}

// IsCoverageSeedOrigin reports whether an origin is an engine coverage seed.
func IsCoverageSeedOrigin(origin string) bool {
	return coverageSeedOrigins[strings.ToLower(strings.TrimSpace(origin))]
}

// ClaimEligible reports whether this row is a Formal Hypothesis the scheduler
// may hand out. It is the single claim-entry quality gate:
//
//   - engine coverage seeds are never claimable (Coverage opportunity ≠ Belief);
//   - the row must carry a concrete claim/identity (a title, endpoint, target
//     or source→sink path). A bare class row is not scheduling work.
//
// Preconditions/falsification quality is enforced where a model CREATES a
// hypothesis (record_hypothesis), not by retroactively rejecting historical
// rows; legacy ledgers stay claimable and auditable. Stored state and history
// are never rewritten by this predicate.
func (h *Hypothesis) ClaimEligible() bool {
	if h == nil {
		return false
	}
	origin := strings.ToLower(strings.TrimSpace(h.Origin))
	if IsCoverageSeedOrigin(origin) {
		return false
	}
	return strings.TrimSpace(h.Title) != "" ||
		strings.TrimSpace(h.Endpoint) != "" ||
		strings.TrimSpace(h.Target) != "" ||
		strings.TrimSpace(h.DataFlow) != ""
}

// sanitize bounds and normalizes external hypothesis input.
func (h *Hypothesis) sanitize() {
	h.Title = truncate(strings.TrimSpace(h.Title), maxSummaryLen)
	h.VulnClass = strings.ToLower(truncate(strings.TrimSpace(h.VulnClass), maxHypothesisFieldLen))
	h.Target = truncate(strings.TrimSpace(h.Target), maxHypothesisFieldLen)
	h.Endpoint = truncate(strings.TrimSpace(h.Endpoint), maxHypothesisFieldLen)
	h.Parameter = truncate(strings.TrimSpace(h.Parameter), maxHypothesisFieldLen)
	h.Role = truncate(strings.TrimSpace(h.Role), maxHypothesisFieldLen)
	h.DataFlow = truncate(strings.TrimSpace(h.DataFlow), maxSummaryLen)
	h.RequiredPrivilege = truncate(strings.TrimSpace(h.RequiredPrivilege), maxHypothesisFieldLen)
	h.Baseline = truncate(strings.TrimSpace(h.Baseline), maxSummaryLen)
	h.Falsification = truncate(strings.TrimSpace(h.Falsification), maxSummaryLen)
	h.NextObservation = truncate(strings.TrimSpace(h.NextObservation), maxSummaryLen)
	h.NextAction = truncate(strings.TrimSpace(h.NextAction), maxSummaryLen)
	h.Origin = truncate(strings.TrimSpace(h.Origin), maxHypothesisFieldLen)
	h.Confidence = clampConfidence(h.Confidence)
	h.VOI = clampConfidence(h.VOI)
	if !h.VOIEstimated {
		h.VOI = 0
	}
	if h.Status != "" {
		h.Status = NormalizeHypothesisStatus(string(h.Status))
	}
	if len(h.Preconditions) > 0 {
		cleaned := make([]string, 0, len(h.Preconditions))
		for _, p := range h.Preconditions {
			if p = truncate(strings.TrimSpace(p), maxHypothesisFieldLen); p != "" {
				cleaned = append(cleaned, p)
			}
		}
		h.Preconditions = cleaned
	}
}

func (ev *Evidence) sanitize() {
	ev.Kind = strings.ToLower(truncate(strings.TrimSpace(ev.Kind), maxHypothesisFieldLen))
	if ev.Kind == "" {
		ev.Kind = "probe"
	}
	ev.Summary = truncate(strings.TrimSpace(ev.Summary), maxSummaryLen)
	ev.Request = truncate(ev.Request, maxEvidenceStringLen)
	ev.Response = truncate(ev.Response, maxEvidenceStringLen)
	ev.FindingID = truncate(strings.TrimSpace(ev.FindingID), maxHypothesisFieldLen)
	ev.AgentID = truncate(strings.TrimSpace(ev.AgentID), maxHypothesisFieldLen)
	ev.Source = truncate(strings.TrimSpace(ev.Source), maxHypothesisFieldLen)
	ev.Nature = NormalizeEvidenceNature(ev.Nature)
	ev.InfoItemID = truncate(strings.TrimSpace(ev.InfoItemID), maxHypothesisFieldLen)
	ev.IntentID = truncate(strings.TrimSpace(ev.IntentID), maxHypothesisFieldLen)
	ev.Confidence = clampConfidence(ev.Confidence)
	if len(ev.Supports) > 0 {
		seen := make(map[string]bool, len(ev.Supports))
		cleaned := make([]string, 0, len(ev.Supports))
		for _, id := range ev.Supports {
			id = truncate(strings.TrimSpace(id), maxHypothesisFieldLen)
			if id != "" && !seen[id] {
				seen[id] = true
				cleaned = append(cleaned, id)
			}
		}
		ev.Supports = cleaned
	}
}

// EngineExploit reports whether this is exploit evidence written by an engine
// verifier (see Evidence.Source), as opposed to the model's own text.
func (ev Evidence) EngineExploit() bool {
	return ev.Source != "" && strings.EqualFold(ev.Kind, "exploit")
}

// HasEngineExploit reports whether an engine verifier recorded exploit
// evidence on this hypothesis. Such a hypothesis is never auto-settled as
// exhausted: the engine saw the vulnerability, so only the model (report or
// reject) or a human may conclude it.
func (h Hypothesis) HasEngineExploit() bool {
	for _, ev := range h.Evidence {
		if ev.EngineExploit() {
			return true
		}
	}
	return false
}

// capEvidence bounds evidence per hypothesis, evicting the oldest non-finding
// observations first so confirmed-finding references are always retained.
func capEvidence(h *Hypothesis) {
	if len(h.Evidence) <= maxEvidencePerHypothesis {
		return
	}
	over := len(h.Evidence) - maxEvidencePerHypothesis
	kept := make([]Evidence, 0, maxEvidencePerHypothesis)
	// First pass: drop oldest non-finding evidence.
	for _, ev := range h.Evidence {
		if over > 0 && ev.Kind != EvidenceFindingRef {
			over--
			continue
		}
		kept = append(kept, ev)
	}
	// If still over (all remaining were finding refs), trim oldest.
	if len(kept) > maxEvidencePerHypothesis {
		kept = kept[len(kept)-maxEvidencePerHypothesis:]
	}
	h.Evidence = kept
	pruneSupportIDs(h)
}

// validSupportIDs keeps only references to currently present evidence.
func validSupportIDs(supports []string, existing map[string]bool) []string {
	if len(supports) == 0 {
		return nil
	}
	out := make([]string, 0, len(supports))
	for _, id := range supports {
		if existing[id] {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func remapSupportIDs(supports []string, remap map[string]string) []string {
	if len(supports) == 0 {
		return nil
	}
	out := make([]string, 0, len(supports))
	for _, id := range supports {
		if mapped, ok := remap[id]; ok {
			out = append(out, mapped)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pruneSupportIDs removes references to evidence evicted by the per-hypothesis
// cap. An inferred item with no remaining supports becomes explicitly
// unsubstantiated rather than retaining a dangling reference.
func pruneSupportIDs(h *Hypothesis) {
	present := make(map[string]bool, len(h.Evidence))
	for _, ev := range h.Evidence {
		present[ev.ID] = true
	}
	for i := range h.Evidence {
		if len(h.Evidence[i].Supports) == 0 {
			continue
		}
		h.Evidence[i].Supports = validSupportIDs(h.Evidence[i].Supports, present)
	}
}

// dedupKey builds a normalized identity for a hypothesis.
func dedupKey(vulnClass, endpoint, parameter, role string) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(vulnClass)),
		normalizeEndpoint(endpoint),
		strings.ToLower(strings.TrimSpace(parameter)),
		strings.ToLower(strings.TrimSpace(role)),
	}, "|")
}

// normalizeEndpoint drops the query/fragment, trims a trailing slash and
// lowercases only a URL's scheme and host, so "/a/?x=1" and "/a/" share a key.
// The path keeps its case: most servers route paths case-sensitively, and a
// case variant of a route (/Api/ next to /api/) is separate surface — an
// authorization bypass through a case-variant mount is exactly that, and
// merging the two records loses its evidence.
func normalizeEndpoint(endpoint string) string {
	e := strings.TrimSpace(endpoint)
	if i := strings.IndexAny(e, "?#"); i >= 0 {
		e = e[:i]
	}
	e = lowerSchemeHost(e)
	if len(e) > 1 {
		e = strings.TrimRight(e, "/")
	}
	return e
}

// lowerSchemeHost lowercases the "scheme://host[:port]" prefix of an absolute
// URL and leaves the path as written. Paths without a scheme are unchanged.
func lowerSchemeHost(e string) string {
	i := strings.Index(e, "://")
	if i < 0 {
		return e
	}
	rest := e[i+3:]
	j := strings.IndexByte(rest, '/')
	if j < 0 {
		return strings.ToLower(e)
	}
	return strings.ToLower(e[:i+3+j]) + rest[j:]
}

func clampConfidence(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}

func mergeStringSet(existing, additions []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, e := range existing {
		seen[e] = true
	}
	for _, a := range additions {
		if a = strings.TrimSpace(a); a != "" && !seen[a] {
			existing = append(existing, a)
			seen[a] = true
		}
	}
	return existing
}
