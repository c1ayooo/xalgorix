// Package domainjournal provides a durable, append-only audit stream for
// domain-state changes. It is intentionally separate from telemetry: telemetry
// may be dropped under backpressure, while domain records are part of the audit
// contract and must be fsynced before the corresponding mutation is reported as
// successful.
package domainjournal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/storage"
)

const (
	// FileName is the per-scan journal file name.
	FileName = "domain_journal.jsonl"
	// MaxRecordBytes bounds one audit record to prevent a single tool payload
	// from turning the journal into an unbounded write.
	MaxRecordBytes = 1 << 20
)

// Observer receives a fast, non-blocking notification after a journal append
// becomes durable. Observers must not perform blocking I/O on the append path;
// the cold path uses a buffered channel and the reconciler is the fallback.
type Observer func(path, scanID string, seq uint64)

var (
	observerMu     sync.RWMutex
	nextObserverID uint64
	observers      = make(map[uint64]Observer)
)

// RegisterObserver subscribes to durable journal appends. The returned function
// unregisters the observer and is safe to call more than once.
func RegisterObserver(fn Observer) func() {
	if fn == nil {
		return func() {}
	}
	observerMu.Lock()
	nextObserverID++
	id := nextObserverID
	observers[id] = fn
	observerMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			observerMu.Lock()
			delete(observers, id)
			observerMu.Unlock()
		})
	}
}

func notifyObservers(path, scanID string, seq uint64) {
	observerMu.RLock()
	snapshot := make([]Observer, 0, len(observers))
	for _, fn := range observers {
		snapshot = append(snapshot, fn)
	}
	observerMu.RUnlock()
	for _, fn := range snapshot {
		func() {
			defer func() { _ = recover() }()
			fn(path, scanID, seq)
		}()
	}
}

// ActorKind identifies who caused a mutation. Engine-owned automatic actions
// must not be mislabeled as model actions.
type ActorKind string

const (
	ActorAgent  ActorKind = "agent"
	ActorEngine ActorKind = "engine"
	ActorHuman  ActorKind = "human"
	ActorSystem ActorKind = "system"
)

func (a ActorKind) Valid() bool {
	switch a {
	case ActorAgent, ActorEngine, ActorHuman, ActorSystem:
		return true
	default:
		return false
	}
}

// Actor records the origin of a mutation.
type Actor struct {
	Kind   ActorKind `json:"kind"`
	ID     string    `json:"id,omitempty"`
	Source string    `json:"source,omitempty"`
}

func (a Actor) Validate() error {
	if !a.Kind.Valid() {
		return fmt.Errorf("domainjournal: invalid actor kind %q", a.Kind)
	}
	return nil
}

// Operation names a domain-state transition. The vocabulary is intentionally
// small and extensible; unknown operations are rejected so a typo cannot
// silently create an unprojectable audit event.
type Operation string

const (
	OpSeedHypothesis   Operation = "seed_hypothesis"
	OpRecordHypothesis Operation = "record_hypothesis"
	OpClaimHypothesis  Operation = "claim_hypothesis"
	OpAssignHypothesis Operation = "assign_hypothesis"
	OpSetStatus        Operation = "set_status"
	OpAddEvidence      Operation = "add_evidence"
	OpRecordAttempt    Operation = "record_attempt"
	OpSetVOI           Operation = "set_voi"
	// OpReclaimHypothesis records a lease expiry returning a claimed
	// hypothesis to the schedulable pool (deterministic claim protocol).
	OpReclaimHypothesis Operation = "reclaim_hypothesis"
	OpMergeLedger       Operation = "merge_ledger"
	OpResetLedger       Operation = "reset_ledger"
	OpInfoItemEnriched  Operation = "info_item_enriched"
	OpInfoItemProjected Operation = "info_item_projected"
	// OpObserveTraffic records one proxy-observed request/response (R2b
	// trafficproxy) as an observed InfoItem. It carries no hypothesis
	// mutation — the ledger is only the InfoItem's journal host.
	OpObserveTraffic Operation = "observe_traffic"
	// OpRecordObjective records the operator-stated task objective as an
	// observed (human-sourced) InfoItem. Like OpObserveTraffic it carries no
	// hypothesis mutation: the objective is context for the loop, never a
	// verdict and never a finding.
	OpRecordObjective Operation = "record_objective"
	// OpSkillLoad is the integration invocation audit for one completed skill
	// load. Audit-only (no hypothesis mutation) — together with
	// OpAgentDelegate and OpMcpCall it is the per-scan authority for the
	// integration invocation records the web layer projects cross-scan into
	// <dataDir>/_invocations/*.jsonl; the telemetry events.jsonl is never an
	// audit source (charter §5.1).
	OpSkillLoad Operation = "skill_load"
	// OpAgentDelegat is the audit record for one sub-agent delegation
	// lifecycle event (spawn/create and completion), audit-only.
	OpAgentDelegat Operation = "agent_delegate"
	// OpMcpCall is the audit record for one completed MCP tool call
	// (ok or error), audit-only.
	OpMcpCall Operation = "mcp_call"
	// OpFindingRecorded projects a persisted finding into the information
	// space as an observed InfoItem (subject=endpoint, purpose=finding). It is
	// appended by the report gate after the finding survives every gate and is
	// durable, so a finding can never exist without a corresponding InfoItem —
	// the model is not asked to remember a second call.
	OpFindingRecorded Operation = "finding_recorded"
	// OpToolObservation records one engine-detected observation from a tool's
	// output (for example a sensitive file served with 200). The engine, not
	// the model, decides that the observation is worth keeping, so budget
	// exhaustion cannot drop it.
	OpToolObservation Operation = "tool_observation"
	// OpRecordDeviation records an executor's stated departure from its
	// assigned intent (R8.4). Audit-only: it never changes plan state.
	OpRecordDeviation Operation = "record_deviation"
	// OpRecordGuidance projects operator guidance delivered to a run into the
	// information space as an observed, human-sourced InfoItem. It is the
	// InfoItem counterpart of the R7 hint node; the journal append never
	// mutates ledger or plan state.
	OpRecordGuidance Operation = "record_guidance"
	// OpContextDigest records one context-compaction snapshot as an inferred
	// InfoItem with supports (IG6b). The digest node is the aggregate/authority
	// reference; this record is its atomic provenance. Audit-only like the other
	// observation ops: it never mutates ledger, plan or scheduling state.
	OpContextDigest Operation = "context_digest"
	// OpReadInfoItem / OpSearchInfo record an engine-side information read
	// (R3 Information-to-Decision Trace). They name what was read, why and
	// whether the result was truncated/incomplete. Audit-only: a read never
	// changes a hypothesis, intent or finding.
	OpReadInfoItem Operation = "read_info_item"
	OpSearchInfo   Operation = "search_info"
	// OpActionAttempt records one target-facing action fingerprint (subject +
	// action class + scope) so a repeated action can be traced back to the
	// earlier attempt/evidence instead of being repeated blindly (R3).
	OpActionAttempt Operation = "action_attempt"
	// OpDecision records one planning/control decision: the candidate set it
	// chose from, the selected candidate, the hard constraints that were
	// evaluated, and why. Audit-only: replaying decisions never re-runs or
	// rewrites the plan; the graph remains the lifecycle authority.
	OpDecision Operation = "decision"
	// OpEgressPause / OpEgressStop record the Engagement-wide egress policy
	// changing state (target asked for a pause, operator/authorization stop).
	// Audit-only: they never end an intent; the agent/pool owns that decision.
	OpEgressPause Operation = "egress_pause"
	OpEgressStop  Operation = "egress_stop"
)

func (o Operation) Valid() bool {
	switch o {
	case OpSeedHypothesis, OpRecordHypothesis, OpClaimHypothesis, OpAssignHypothesis,
		OpSetStatus, OpAddEvidence, OpRecordAttempt, OpSetVOI, OpReclaimHypothesis, OpMergeLedger, OpResetLedger,
		OpInfoItemEnriched, OpInfoItemProjected, OpObserveTraffic, OpRecordObjective,
		OpSkillLoad, OpAgentDelegat, OpMcpCall, OpFindingRecorded, OpToolObservation,
		OpRecordDeviation, OpRecordGuidance, OpContextDigest,
		OpReadInfoItem, OpSearchInfo, OpActionAttempt, OpDecision, OpEgressPause, OpEgressStop:
		return true
	default:
		return false
	}
}

// State is the auditable subset of a hypothesis before/after a mutation.
// It deliberately excludes evidence bodies, which can be large and contain
// target data.
type State struct {
	Status       string  `json:"status,omitempty"`
	AssignedTo   string  `json:"assigned_to,omitempty"`
	Confidence   float64 `json:"confidence,omitempty"`
	VOI          float64 `json:"voi,omitempty"`
	VOIEstimated bool    `json:"voi_estimated,omitempty"`
	Attempts     int     `json:"attempts,omitempty"`
}

// DecisionCandidate is one option considered by a decision. Rank preserves the
// deterministic order the engine/model presented (1 = first), so a later
// replay can verify the same tie-break instead of re-deriving it from prose.
type DecisionCandidate struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind,omitempty"`
	Title      string  `json:"title,omitempty"`
	Rank       int     `json:"rank,omitempty"`
	Score      float64 `json:"score,omitempty"`
	Selected   bool    `json:"selected,omitempty"`
	RejectedBy string  `json:"rejected_by,omitempty"`
}

// Decision is the structured snapshot of one material planning or control
// decision. It is an audit projection: the graph/plan remains authoritative.
//
// InfoUse/NoInfoReason make the "information changed the decision" question
// answerable: a decision either cites BasisRefs or explicitly records that no
// new information was used (for example operator_decision).
type Decision struct {
	ID              string              `json:"id,omitempty"`
	Action          string              `json:"action"`
	Actor           string              `json:"actor,omitempty"`
	At              time.Time           `json:"at,omitempty"`
	IntentID        string              `json:"intent_id,omitempty"`
	HypothesisID    string              `json:"hypothesis_id,omitempty"`
	BasisRefs       []string            `json:"basis_refs,omitempty"`
	Candidates      []DecisionCandidate `json:"candidates,omitempty"`
	HardConstraints []string            `json:"hard_constraints,omitempty"`
	Selected        string              `json:"selected,omitempty"`
	Reason          string              `json:"reason,omitempty"`
	InfoUse         string              `json:"info_use,omitempty"`
	NoInfoReason    string              `json:"no_info_reason,omitempty"`
	SnapshotDigest  string              `json:"snapshot_digest,omitempty"`
}

// Record is one durable domain event. Sequence is per scan and assigned by the
// Journal writer; callers must not invent cross-scan ordering.
type Record struct {
	ScanID       string              `json:"scan_id"`
	Seq          uint64              `json:"seq"`
	At           time.Time           `json:"at"`
	Actor        Actor               `json:"actor"`
	Operation    Operation           `json:"operation"`
	IntentID     string              `json:"intent_id,omitempty"`
	HypothesisID string              `json:"hypothesis_id,omitempty"`
	EvidenceID   string              `json:"evidence_id,omitempty"`
	Summary      string              `json:"summary,omitempty"`
	Decision     *Decision           `json:"decision,omitempty"`
	Before       *State              `json:"before,omitempty"`
	After        *State              `json:"after,omitempty"`
	InfoItem     *infoitem.Item      `json:"info_item,omitempty"`
	InfoItems    []infoitem.Item     `json:"info_items,omitempty"`
	Relations    []infoitem.Relation `json:"relations,omitempty"`
}

// ID returns a stable human-readable audit identifier.
func (r Record) ID() string {
	return fmt.Sprintf("%s:%d", r.ScanID, r.Seq)
}

func (r Record) Validate() error {
	if strings.TrimSpace(r.ScanID) == "" {
		return errors.New("domainjournal: scan_id is required")
	}
	if r.Seq == 0 {
		return errors.New("domainjournal: seq must be positive")
	}
	if r.At.IsZero() {
		return errors.New("domainjournal: at is required")
	}
	if err := r.Actor.Validate(); err != nil {
		return err
	}
	if !r.Operation.Valid() {
		return fmt.Errorf("domainjournal: invalid operation %q", r.Operation)
	}
	if r.Operation == OpDecision && r.Decision == nil {
		return errors.New("domainjournal: decision operation requires a decision payload")
	}
	if r.Decision != nil {
		if r.Operation != OpDecision {
			return errors.New("domainjournal: decision payload requires the decision operation")
		}
		if strings.TrimSpace(r.Decision.Action) == "" {
			return errors.New("domainjournal: decision action is required")
		}
	}
	if r.InfoItem != nil {
		if err := r.InfoItem.Validate(); err != nil {
			return fmt.Errorf("domainjournal: info item: %w", err)
		}
		if r.InfoItem.ScanID != r.ScanID {
			return errors.New("domainjournal: info item scan_id does not match record")
		}
		if r.InfoItem.SourceSeq != r.Seq {
			return errors.New("domainjournal: info item source_seq does not match record seq")
		}
	}
	for n := range r.InfoItems {
		item := r.InfoItems[n]
		if err := item.Validate(); err != nil {
			return fmt.Errorf("domainjournal: info_items[%d]: %w", n, err)
		}
		if item.ScanID != r.ScanID || item.SourceSeq != r.Seq {
			return fmt.Errorf("domainjournal: info_items[%d] scan/seq does not match record", n)
		}
	}
	for n, edge := range r.Relations {
		if err := edge.Validate(); err != nil {
			return fmt.Errorf("domainjournal: relation[%d]: %w", n, err)
		}
		if edge.ScanID != r.ScanID || edge.SourceSeq != r.Seq {
			return fmt.Errorf("domainjournal: relation[%d] scan/seq does not match record", n)
		}
	}
	return nil
}

// Journal serializes one scan's durable records. It is safe for concurrent
// Append calls; all state mutations for a scan must flow through this writer.
type Journal struct {
	path       string
	scanID     string
	instanceID string // run identity (D11) — stamped on backend rows

	mu      sync.Mutex
	lastSeq uint64
}

// SetInstanceID stamps the run identity (D11) on this journal. With the PG
// backend registered, every run_journal row carries it for run-level
// aggregation; the file mode ignores the metadata.
func (j *Journal) SetInstanceID(instanceID string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.instanceID = strings.TrimSpace(instanceID)
}

// Open loads an existing journal and repairs only an incomplete trailing line.
// It never fabricates missing sequence numbers; gaps are surfaced by Project.
//
// With the PG backend registered (R6.1), the durable sequence watermark is
// seeded from the backend's stream: the file is no longer the durable source,
// so a damaged legacy tail must not disable the journal, and a same-scan
// continuation must append AFTER every durable record — re-using a durable
// sequence would silently rewrite audit history through the backend's
// idempotent upsert.
func Open(path, scanID string) (*Journal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("domainjournal: path is required")
	}
	if strings.TrimSpace(scanID) == "" {
		return nil, errors.New("domainjournal: scan_id is required")
	}
	if err := storage.EnsureSecureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	backend := currentBackend()
	records, _, err := ReadRecords(path)
	if err != nil {
		if backend == nil {
			return nil, err
		}
		records = nil
	}
	j := &Journal{path: path, scanID: scanID}
	for _, rec := range records {
		if rec.ScanID != scanID {
			return nil, fmt.Errorf("domainjournal: journal %q contains scan %q, expected %q", path, rec.ScanID, scanID)
		}
		if rec.Seq > j.lastSeq {
			j.lastSeq = rec.Seq
		}
	}
	if backend != nil {
		durable, err := backendLastSeq(backend, scanID)
		if err != nil {
			return nil, fmt.Errorf("domainjournal: open %q: durable seq: %w", path, err)
		}
		if durable > j.lastSeq {
			j.lastSeq = durable
		}
	}
	return j, nil
}

// backendLastSeq returns the scan's durable max sequence in the backend.
func backendLastSeq(b Backend, scanID string) (uint64, error) {
	sources, err := b.LoadJournalSources(context.Background())
	if err != nil {
		return 0, err
	}
	var last uint64
	for _, src := range sources {
		if src.ScanID == scanID && src.LastSeq > last {
			last = src.LastSeq
		}
	}
	return last, nil
}

// Path returns the journal path.
func (j *Journal) Path() string {
	if j == nil {
		return ""
	}
	return j.path
}

// LastSeq returns the highest assigned sequence, including gaps.
func (j *Journal) LastSeq() uint64 {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lastSeq
}

// Append assigns the next per-scan sequence, fsyncs the record, and only then
// advances the in-memory watermark. A failed append leaves the sequence
// reusable.
func (j *Journal) Append(rec Record) (Record, error) {
	if j == nil {
		return rec, errors.New("domainjournal: nil journal")
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	if rec.ScanID == "" {
		rec.ScanID = j.scanID
	}
	if rec.ScanID != j.scanID {
		return rec, fmt.Errorf("domainjournal: record scan_id %q does not match journal %q", rec.ScanID, j.scanID)
	}
	next := j.lastSeq + 1
	if rec.Seq != 0 && rec.Seq != next {
		return rec, fmt.Errorf("domainjournal: record seq %d, expected %d", rec.Seq, next)
	}
	rec.Seq = next
	if rec.At.IsZero() {
		rec.At = time.Now().UTC()
	}
	if rec.InfoItem != nil {
		item := *rec.InfoItem
		item.ScanID = rec.ScanID
		item.SourceSeq = rec.Seq
		if item.CreatedAt.IsZero() {
			item.CreatedAt = rec.At
		}
		if item.Coordinates.ObservedAt.IsZero() {
			item.Coordinates.ObservedAt = rec.At
		}
		rec.InfoItem = &item
	}
	if len(rec.InfoItems) > 0 {
		items := make([]infoitem.Item, len(rec.InfoItems))
		copy(items, rec.InfoItems)
		for i := range items {
			items[i].ScanID = rec.ScanID
			items[i].SourceSeq = rec.Seq
			if items[i].CreatedAt.IsZero() {
				items[i].CreatedAt = rec.At
			}
			if items[i].Coordinates.ObservedAt.IsZero() {
				items[i].Coordinates.ObservedAt = rec.At
			}
		}
		rec.InfoItems = items
	}
	if len(rec.Relations) > 0 {
		relations := make([]infoitem.Relation, len(rec.Relations))
		copy(relations, rec.Relations)
		for n := range relations {
			relations[n].ScanID = rec.ScanID
			relations[n].SourceSeq = rec.Seq
			if relations[n].CreatedAt.IsZero() {
				relations[n].CreatedAt = rec.At
			}
		}
		rec.Relations = relations
	}
	if err := rec.Validate(); err != nil {
		return rec, err
	}

	data, err := json.Marshal(rec)
	if err != nil {
		return rec, fmt.Errorf("domainjournal: marshal: %w", err)
	}
	if len(data) > MaxRecordBytes {
		return rec, fmt.Errorf("domainjournal: record is %d bytes, max %d", len(data), MaxRecordBytes)
	}
	// R6.1 (charter v3.0 §6.3): with the PG backend registered the durable
	// home is run_journal — the file is no longer written (D12 red line).
	// The fsync semantics move into the backend's synchronous transaction;
	// the append path stays serialized on j.mu, the watermark advances only
	// after the durable write succeeds, and a failed append leaves the
	// sequence reusable exactly like a failed file append. The failure
	// watermark is recorded by the implementation (charter §6.4).
	if b := currentBackend(); b != nil {
		if err := b.AppendJournalRecord(rec.ScanID, j.instanceID, rec); err != nil {
			return rec, fmt.Errorf("domainjournal: append: %w", err)
		}
		j.lastSeq = rec.Seq
		notifyObservers(j.path, rec.ScanID, rec.Seq)
		return rec, nil
	}
	data = append(data, '\n')
	if err := storage.AppendSync(j.path, data); err != nil {
		return rec, fmt.Errorf("domainjournal: append: %w", err)
	}
	j.lastSeq = rec.Seq
	notifyObservers(j.path, rec.ScanID, rec.Seq)
	return rec, nil
}

// ReadRecords reads a journal, repairing a trailing partial line caused by a
// crash during append. A malformed complete line is treated as corruption and
// is not silently discarded.
func ReadRecords(path string) ([]Record, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("domainjournal: read %q: %w", path, err)
	}
	truncated := false
	if len(data) > 0 && data[len(data)-1] != '\n' {
		last := bytes.LastIndexByte(data, '\n')
		keep := last + 1
		if err := os.Truncate(path, int64(keep)); err != nil {
			return nil, false, fmt.Errorf("domainjournal: truncate partial tail %q: %w", path, err)
		}
		data = data[:keep]
		truncated = true
	}
	if len(data) == 0 {
		return nil, truncated, nil
	}

	lines := bytes.Split(data, []byte{'\n'})
	records := make([]Record, 0, len(lines))
	for n, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, truncated, fmt.Errorf("domainjournal: decode line %d in %q: %w", n+1, path, err)
		}
		if err := rec.Validate(); err != nil {
			return nil, truncated, fmt.Errorf("domainjournal: invalid record on line %d in %q: %w", n+1, path, err)
		}
		records = append(records, rec)
	}
	return records, truncated, nil
}

// Recorder is the narrow interface consumed by hot-path stores. A nil or
// disabled implementation preserves the old file-only behavior.
type Recorder interface {
	Append(Record) (Record, error)
	LastSeq() uint64
}

// NopRecorder is used when no durable journal is configured.
type NopRecorder struct{}

func (NopRecorder) Append(rec Record) (Record, error) { return rec, nil }
func (NopRecorder) LastSeq() uint64                   { return 0 }

// Projection is a read-only reconstruction. It never feeds scheduling or
// verdict decisions.
type Projection struct {
	ScanID          string           `json:"scan_id"`
	Status          string           `json:"status"`
	Incomplete      bool             `json:"incomplete"`
	Disordered      bool             `json:"disordered"`
	DuplicateSeqs   []uint64         `json:"duplicate_seqs,omitempty"`
	Gaps            []Gap            `json:"gaps,omitempty"`
	LastSeq         uint64           `json:"last_seq"`
	Timeline        []Record         `json:"timeline"`
	Claims          []Claim          `json:"claims,omitempty"`
	Deviations      []Deviation      `json:"deviations,omitempty"`
	HypothesisFinal map[string]State `json:"hypothesis_final,omitempty"`
}

type Gap struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// Deviation is one executor-reported departure from its assigned intent.
type Deviation struct {
	IntentID string    `json:"intent_id,omitempty"`
	Summary  string    `json:"summary"`
	Actor    Actor     `json:"actor"`
	At       time.Time `json:"at"`
	Seq      uint64    `json:"seq"`
}

type Claim struct {
	HypothesisID string    `json:"hypothesis_id"`
	Actor        Actor     `json:"actor"`
	At           time.Time `json:"at"`
	Seq          uint64    `json:"seq"`
	Summary      string    `json:"summary,omitempty"`
}

// Project constructs deterministic audit history and explicitly reports
// duplicates, disorder and missing sequence ranges.
func Project(scanID string, records []Record) Projection {
	p := Projection{
		ScanID: scanID,
		Status: "empty",
		// Non-nil even for an empty journal: nil would marshal as JSON null
		// and the dashboard iterates timeline unconditionally.
		Timeline:        append([]Record{}, records...),
		HypothesisFinal: map[string]State{},
	}
	if len(records) == 0 {
		return p
	}

	seenOrder := make(map[uint64]bool, len(records))
	for n, rec := range records {
		if n > 0 && rec.Seq < records[n-1].Seq {
			p.Disordered = true
		}
		if seenOrder[rec.Seq] {
			p.DuplicateSeqs = append(p.DuplicateSeqs, rec.Seq)
		}
		seenOrder[rec.Seq] = true
	}

	sort.SliceStable(p.Timeline, func(i, j int) bool { return p.Timeline[i].Seq < p.Timeline[j].Seq })
	unique := make(map[uint64]bool, len(records))
	for _, rec := range p.Timeline {
		unique[rec.Seq] = true
		if rec.Seq > p.LastSeq {
			p.LastSeq = rec.Seq
		}
	}
	for seq := uint64(1); seq <= p.LastSeq; seq++ {
		if !unique[seq] {
			if len(p.Gaps) == 0 || p.Gaps[len(p.Gaps)-1].To+1 != seq {
				p.Gaps = append(p.Gaps, Gap{From: seq, To: seq})
			} else {
				p.Gaps[len(p.Gaps)-1].To = seq
			}
		}
	}
	for _, rec := range p.Timeline {
		if rec.Operation == OpClaimHypothesis && rec.HypothesisID != "" {
			p.Claims = append(p.Claims, Claim{
				HypothesisID: rec.HypothesisID,
				Actor:        rec.Actor,
				At:           rec.At,
				Seq:          rec.Seq,
				Summary:      rec.Summary,
			})
		}
		if rec.HypothesisID != "" && rec.After != nil {
			p.HypothesisFinal[rec.HypothesisID] = *rec.After
		}
		if rec.Operation == OpRecordDeviation {
			p.Deviations = append(p.Deviations, Deviation{
				IntentID: rec.IntentID,
				Summary:  rec.Summary,
				Actor:    rec.Actor,
				At:       rec.At,
				Seq:      rec.Seq,
			})
		}
	}

	p.Incomplete = p.Disordered || len(p.Gaps) > 0 || len(p.DuplicateSeqs) > 0
	if p.Incomplete {
		p.Status = "incomplete"
	} else {
		p.Status = "complete"
	}
	return p
}
