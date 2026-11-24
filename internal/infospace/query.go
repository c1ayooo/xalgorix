// Package infospace defines the read-only query surface over the unified
// information space (exploration graph aggregates + InfoItem atoms).
//
// Types live here so both the graph authority (which owns the PG pool) and
// consumers (workers, planner, web read APIs) share one vocabulary without an
// import cycle. Implementations return summaries and references, never full
// bodies: detail stays behind ledger/InfoItem reads.
package infospace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

// Layer selects one information layer. Empty means both.
type Layer string

const (
	LayerAggregate Layer = "aggregate"
	LayerAtom      Layer = "atom"
)

func (l Layer) Valid() bool {
	return l == "" || l == LayerAggregate || l == LayerAtom
}

const (
	DefaultLimit = 50
	MaxLimit     = 200
	MaxDepth     = 3
	DefaultDepth = 2
)

// RegionQuery is the deterministic property-filter activation over both
// layers. RunIDs are the canonical run identity (`run_id` in the views); a
// sibling ScanContext's atoms are reachable through the same run id.
type RegionQuery struct {
	RunIDs         []string
	AllRuns        bool
	Layer          Layer
	Kinds          []string
	State          string
	Subject        string
	SubjectKind    string
	Purpose        string
	Nature         infoitem.Nature
	ContextEquals  map[string]string
	ObservedAfter  *time.Time
	ObservedBefore *time.Time
	Cursor         *infoitem.RegionCursor
	Limit          int
}

func (q *RegionQuery) Normalize() {
	if q == nil {
		return
	}
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	if q.Limit > MaxLimit {
		q.Limit = MaxLimit
	}
}

func (q RegionQuery) Validate() error {
	if len(q.RunIDs) == 0 && !q.AllRuns {
		return errors.New("infospace: region query requires at least one run_id or all_runs")
	}
	if q.AllRuns && len(q.RunIDs) > 0 {
		return errors.New("infospace: region query all_runs is mutually exclusive with run_ids")
	}
	for n, id := range q.RunIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("infospace: region query run_ids[%d] is empty", n)
		}
	}
	if !q.Layer.Valid() {
		return fmt.Errorf("infospace: invalid layer %q", q.Layer)
	}
	if q.Nature != "" && !q.Nature.Valid() {
		return fmt.Errorf("infospace: invalid nature %q", q.Nature)
	}
	for n, kind := range q.Kinds {
		if strings.TrimSpace(kind) == "" {
			return fmt.Errorf("infospace: region query kinds[%d] is empty", n)
		}
	}
	if q.Cursor != nil && (q.Cursor.ID == "" || q.Cursor.ObservedAt.IsZero()) {
		return errors.New("infospace: region cursor requires both observed_at and id")
	}
	if q.ObservedAfter != nil && q.ObservedBefore != nil && q.ObservedBefore.Before(*q.ObservedAfter) {
		return errors.New("infospace: observed_before must not precede observed_after")
	}
	if q.Limit < 1 || q.Limit > MaxLimit {
		return fmt.Errorf("infospace: limit must be within [1,%d]", MaxLimit)
	}
	return nil
}

// Item is one row of the unified projection. Payload is the layer-native JSON
// (aggregate: graph payload; atom: content/coordinates/source/supports); it is
// a summary/document, not the authoritative body.
type Item struct {
	ScanID      string
	RunID       string
	InfoID      string
	Layer       Layer
	Kind        string
	State       string
	UnitKey     string
	Priority    int
	Origin      string
	Subject     string
	SubjectKind string
	Purpose     string
	Nature      string
	ObservedAt  time.Time
	Context     []byte
	Payload     []byte
	CreatedAt   time.Time
}

type RegionResult struct {
	Items      []Item
	Truncated  bool
	HasMore    bool
	NextCursor string
	Incomplete bool
	GapReason  string
}

// AssociationQuery walks explicit relation edges from start IDs. Depth and
// result count are always bounded; paths are returned with the hit.
type AssociationQuery struct {
	RunID     string
	StartIDs  []string
	EdgeTypes []string
	MaxDepth  int
	Limit     int
}

func (q *AssociationQuery) Normalize() {
	if q == nil {
		return
	}
	if q.MaxDepth <= 0 {
		q.MaxDepth = DefaultDepth
	}
	if q.MaxDepth > MaxDepth {
		q.MaxDepth = MaxDepth
	}
	if q.Limit <= 0 {
		q.Limit = 20
	}
	if q.Limit > MaxLimit {
		q.Limit = MaxLimit
	}
}

func (q AssociationQuery) Validate() error {
	if strings.TrimSpace(q.RunID) == "" {
		return errors.New("infospace: association query run_id is required")
	}
	if len(q.StartIDs) == 0 {
		return errors.New("infospace: association query requires start_ids")
	}
	for n, id := range q.StartIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("infospace: association start_ids[%d] is empty", n)
		}
	}
	if q.MaxDepth < 1 || q.MaxDepth > MaxDepth {
		return fmt.Errorf("infospace: max_depth must be within [1,%d]", MaxDepth)
	}
	if q.Limit < 1 || q.Limit > MaxLimit {
		return fmt.Errorf("infospace: limit must be within [1,%d]", MaxLimit)
	}
	for _, rel := range q.EdgeTypes {
		if !KnownRelation(rel) {
			return fmt.Errorf("infospace: unsupported edge type %q", rel)
		}
	}
	return nil
}

type AssociationHit struct {
	Item  Item
	Path  []string
	Rels  []string
	Depth int
}

type AssociationResult struct {
	Hits       []AssociationHit
	Truncated  bool
	Incomplete bool
	GapReason  string
}

// ContextQuery fixes a small set of dimensions and returns the mixed view for
// human review or LLM synthesis. It is bounded like Region.
type ContextQuery struct {
	RunID         string
	Subject       string
	SubjectKind   string
	Layer         Layer
	ContextEquals map[string]string
	Since         *time.Time
	Until         *time.Time
	Limit         int
}

func (q *ContextQuery) Normalize() {
	if q == nil {
		return
	}
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	if q.Limit > MaxLimit {
		q.Limit = MaxLimit
	}
}

func (q ContextQuery) Validate() error {
	if strings.TrimSpace(q.RunID) == "" {
		return errors.New("infospace: context query run_id is required")
	}
	if !q.Layer.Valid() {
		return fmt.Errorf("infospace: invalid layer %q", q.Layer)
	}
	if q.Since != nil && q.Until != nil && q.Until.Before(*q.Since) {
		return errors.New("infospace: until must not precede since")
	}
	if q.Limit < 1 || q.Limit > MaxLimit {
		return fmt.Errorf("infospace: limit must be within [1,%d]", MaxLimit)
	}
	return nil
}

type ContextResult struct {
	Items      []Item
	Truncated  bool
	Incomplete bool
	GapReason  string
}

// InfoType is one stage of the information-space heatmap. It names the
// decision-facing category of an information object, not its storage kind:
// atoms split by nature (observed/inferred) and aggregates follow the loop
// belief → action (intent) → evidence (fact) → verdict (finding). goal/hint/
// digest are workflow/control objects and stay off the map; they are reachable
// through Region.
type InfoType string

const (
	InfoTypeObservation InfoType = "observation"
	InfoTypeInference   InfoType = "inference"
	InfoTypeBelief      InfoType = "belief"
	InfoTypeIntent      InfoType = "intent"
	InfoTypeFact        InfoType = "fact"
	InfoTypeFinding     InfoType = "finding"
)

// HeatmapMaxRows caps distinct subjects per query. The cap is a row budget,
// not a data limit: SubjectTotal discloses how many subjects exist so the
// caller can render an explicit truncation instead of a silently partial map.
const (
	HeatmapDefaultRows = 40
	HeatmapMaxRows     = 200
)

func (t InfoType) Valid() bool {
	switch t {
	case InfoTypeObservation, InfoTypeInference, InfoTypeBelief, InfoTypeIntent, InfoTypeFact, InfoTypeFinding:
		return true
	default:
		return false
	}
}

// DefaultInfoTypes is the deterministic loop order of the map: observation →
// inference → belief → action (intent) → evidence (fact) → verdict (finding).
// The dashboard colours a subject by the furthest stage it reached.
func DefaultInfoTypes() []InfoType {
	return []InfoType{
		InfoTypeObservation,
		InfoTypeInference,
		InfoTypeBelief,
		InfoTypeIntent,
		InfoTypeFact,
		InfoTypeFinding,
	}
}

// HeatmapQuery is the bounded aggregation form of Region: one cell per
// (subject, info type). It is read-only, deterministic and never returns
// bodies; drill-down uses Region filtered by the same subject and type.
type HeatmapQuery struct {
	RunIDs      []string
	AllRuns     bool
	Subject     string
	SubjectKind string
	Purpose     string
	// Query is a case-insensitive substring filter on the subject, used by the
	// project map search. Empty means all subjects.
	Query          string
	Types          []InfoType
	ObservedAfter  *time.Time
	ObservedBefore *time.Time
	RowLimit       int
}

func (q *HeatmapQuery) Normalize() {
	if q == nil {
		return
	}
	if q.RowLimit <= 0 {
		q.RowLimit = HeatmapDefaultRows
	}
	if q.RowLimit > HeatmapMaxRows {
		q.RowLimit = HeatmapMaxRows
	}
	if len(q.Types) == 0 {
		q.Types = DefaultInfoTypes()
	}
}

func (q HeatmapQuery) Validate() error {
	if len(q.RunIDs) == 0 && !q.AllRuns {
		return errors.New("infospace: heatmap query requires at least one run_id or all_runs")
	}
	if q.AllRuns && len(q.RunIDs) > 0 {
		return errors.New("infospace: heatmap query all_runs is mutually exclusive with run_ids")
	}
	for n, id := range q.RunIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("infospace: heatmap query run_ids[%d] is empty", n)
		}
	}
	for _, t := range q.Types {
		if !t.Valid() {
			return fmt.Errorf("infospace: unsupported info type %q", t)
		}
	}
	if q.ObservedAfter != nil && q.ObservedBefore != nil && q.ObservedBefore.Before(*q.ObservedAfter) {
		return errors.New("infospace: observed_before must not precede observed_after")
	}
	if q.RowLimit < 1 || q.RowLimit > HeatmapMaxRows {
		return fmt.Errorf("infospace: row limit must be within [1,%d]", HeatmapMaxRows)
	}
	return nil
}

// HeatmapRow is one subject line. Counts carries every requested type (zero
// included) so the caller never has to guess "missing column" vs "zero".
type HeatmapRow struct {
	Subject      string
	SubjectKind  string
	Total        int
	Counts       map[InfoType]int
	LastObserved *time.Time
}

// HeatmapResult is the bounded map. Unlabeled counts information whose subject
// is empty: it cannot be placed on the map (Region has no "empty subject"
// filter) and is reported instead of being silently dropped.
type HeatmapResult struct {
	Types        []InfoType
	Rows         []HeatmapRow
	Truncated    bool
	SubjectTotal int
	Unlabeled    int
	Incomplete   bool
	GapReason    string
}

// HeatmapReader is the optional aggregation extension to Reader. A backend
// that implements it serves the heatmap from the unified view; one that does
// not is an explicit "unavailable", never a client-side approximation.
type HeatmapReader interface {
	Heatmap(ctx context.Context, q HeatmapQuery) (HeatmapResult, error)
}

// RelationCategory separates an attack/evidence chain edge from a pure
// information association. The map shows chains by default and association
// edges only behind an explicit toggle.
type RelationCategory string

const (
	RelationChain RelationCategory = "chain"
	RelationInfo  RelationCategory = "info"
)

// RelationCategoryOf classifies a unified-space relation. Unknown relations
// default to "chain" so a future producer is visible rather than hidden.
func RelationCategoryOf(rel string) RelationCategory {
	switch strings.ToLower(strings.TrimSpace(rel)) {
	case "references", "contradicts", "same_session", "same_subject", "materializes":
		return RelationInfo
	default:
		return RelationChain
	}
}

const (
	RelationsDefaultLimit = 400
	RelationsMaxLimit     = 800
)

// RelationsQuery returns subject-level relation aggregates across the selected
// runs. It carries no bodies: each edge names the relation, count and one
// representative title per endpoint.
type RelationsQuery struct {
	RunIDs         []string
	AllRuns        bool
	ObservedAfter  *time.Time
	ObservedBefore *time.Time
	Limit          int
}

func (q *RelationsQuery) Normalize() {
	if q == nil {
		return
	}
	if q.Limit <= 0 {
		q.Limit = RelationsDefaultLimit
	}
	if q.Limit > RelationsMaxLimit {
		q.Limit = RelationsMaxLimit
	}
}

func (q RelationsQuery) Validate() error {
	if len(q.RunIDs) == 0 && !q.AllRuns {
		return errors.New("infospace: relations query requires at least one run_id or all_runs")
	}
	if q.AllRuns && len(q.RunIDs) > 0 {
		return errors.New("infospace: relations query all_runs is mutually exclusive with run_ids")
	}
	for n, id := range q.RunIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("infospace: relations query run_ids[%d] is empty", n)
		}
	}
	if q.ObservedAfter != nil && q.ObservedBefore != nil && q.ObservedBefore.Before(*q.ObservedAfter) {
		return errors.New("infospace: observed_before must not precede observed_after")
	}
	if q.Limit < 1 || q.Limit > RelationsMaxLimit {
		return fmt.Errorf("infospace: relations limit must be within [1,%d]", RelationsMaxLimit)
	}
	return nil
}

// RelationEdge is one subject → subject relation aggregate. SrcTitle/DstTitle
// are the newest summaries of the objects that produced the edge; they are
// references, not bodies.
type RelationEdge struct {
	SrcSubject string
	SrcKind    string
	DstSubject string
	DstKind    string
	Rel        string
	Category   RelationCategory
	Count      int
	SrcTitle   string
	DstTitle   string
	LastAt     *time.Time
}

type RelationsResult struct {
	Edges      []RelationEdge
	Truncated  bool
	Incomplete bool
	GapReason  string
}

// RelationsReader is the optional project-graph extension. A backend without
// it answers "relations unavailable" instead of pretending the graph is empty.
type RelationsReader interface {
	Relations(ctx context.Context, q RelationsQuery) (RelationsResult, error)
}

// Reader is implemented by the graph authority's PG store. A nil or
// non-implementing backend means the unified space is unavailable; callers
// fall back to the InfoItem cold-path reader or ledger.
type Reader interface {
	Region(ctx context.Context, q RegionQuery) (RegionResult, error)
	Associate(ctx context.Context, q AssociationQuery) (AssociationResult, error)
	ContextSlice(ctx context.Context, q ContextQuery) (ContextResult, error)
}

// RelationMaterializes is the aggregate↔atom link relation (IG3). It is a
// read-model edge projected from graph_info_links by the 9002 view; it is
// never written into exploration_edges.
const RelationMaterializes = "materializes"

// KnownRelation is the union of graph lineage relations, InfoItem relation
// types and the read-model aggregate→atom link. Unknown types are rejected
// instead of silently returning nothing.
func KnownRelation(rel string) bool {
	switch strings.ToLower(strings.TrimSpace(rel)) {
	case "spawns", "derived_from", "yields", "proves", "supports", "covers", "depends_on",
		"contradicts", "references", "same_session", "same_subject",
		RelationMaterializes:
		return true
	default:
		return false
	}
}
