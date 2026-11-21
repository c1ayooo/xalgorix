package infoitem

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultQueryLimit      = 50
	MaxQueryLimit          = 200
	MaxAssociationDepth    = 3
	DefaultAssociationHops = 2
)

// RegionQuery is the deterministic property-filter activation entrance.
// Empty fields are unconstrained.
type RegionQuery struct {
	// ScanIDs constrains the query to these scans. Leave it empty and set
	// AllScans for the cross-scan shape (CORE v2.1 §4.1): the deterministic
	// property filter across every scan's cold-path items.
	ScanIDs        []string          `json:"scan_ids,omitempty"`
	AllScans       bool              `json:"all_scans,omitempty"`
	SubjectKind    string            `json:"subject_kind,omitempty"`
	Subject        string            `json:"subject,omitempty"`
	Purpose        string            `json:"purpose,omitempty"`
	Nature         Nature            `json:"nature,omitempty"`
	ContextEquals  map[string]string `json:"context_equals,omitempty"`
	ObservedAfter  *time.Time        `json:"observed_after,omitempty"`
	ObservedBefore *time.Time        `json:"observed_before,omitempty"`
	Limit          int               `json:"limit,omitempty"`
}

// Normalize applies bounded defaults without changing the filter meaning.
func (q *RegionQuery) Normalize() {
	if q == nil {
		return
	}
	q.Limit = NormalizeLimit(q.Limit)
}

// Validate rejects unsupported filters instead of silently returning broad
// results.
func (q RegionQuery) Validate() error {
	if len(q.ScanIDs) == 0 && !q.AllScans {
		return errors.New("infoitem: region query requires at least one scan_id or all_scans")
	}
	if q.AllScans && len(q.ScanIDs) > 0 {
		return errors.New("infoitem: region query all_scans is mutually exclusive with scan_ids")
	}
	for n, id := range q.ScanIDs {
		if id == "" {
			return fmt.Errorf("infoitem: region query scan_ids[%d] is empty", n)
		}
	}
	if q.Nature != "" && !q.Nature.Valid() {
		return fmt.Errorf("infoitem: invalid nature %q", q.Nature)
	}
	if q.Limit < 0 || q.Limit > MaxQueryLimit {
		return fmt.Errorf("infoitem: limit must be within [1,%d]", MaxQueryLimit)
	}
	return nil
}

// RegionResult carries provenance about truncation and cold-path gaps.
type RegionResult struct {
	Items      []Item `json:"items"`
	Truncated  bool   `json:"truncated"`
	Incomplete bool   `json:"incomplete"`
	GapReason  string `json:"gap_reason,omitempty"`
}

// AssociationQuery traverses explicit relation edges with hard depth and
// result budgets.
type AssociationQuery struct {
	ScanID    string         `json:"scan_id"`
	StartIDs  []string       `json:"start_ids"`
	EdgeTypes []RelationType `json:"edge_types,omitempty"`
	MaxDepth  int            `json:"max_depth,omitempty"`
	Limit     int            `json:"limit,omitempty"`
}

func (q *AssociationQuery) Normalize() {
	if q == nil {
		return
	}
	if q.MaxDepth == 0 {
		q.MaxDepth = DefaultAssociationHops
	}
	if q.MaxDepth > MaxAssociationDepth {
		q.MaxDepth = MaxAssociationDepth
	}
	q.Limit = NormalizeLimit(q.Limit)
}

func (q AssociationQuery) Validate() error {
	if q.ScanID == "" {
		return errors.New("infoitem: association query scan_id is required")
	}
	if len(q.StartIDs) == 0 {
		return errors.New("infoitem: association query start_ids is required")
	}
	if q.MaxDepth < 1 || q.MaxDepth > MaxAssociationDepth {
		return fmt.Errorf("infoitem: max_depth must be within [1,%d]", MaxAssociationDepth)
	}
	if q.Limit < 1 || q.Limit > MaxQueryLimit {
		return fmt.Errorf("infoitem: limit must be within [1,%d]", MaxQueryLimit)
	}
	for _, typ := range q.EdgeTypes {
		if !typ.Valid() {
			return fmt.Errorf("infoitem: invalid edge type %q", typ)
		}
	}
	return nil
}

// AssociationHit describes one reached item and the path that reached it.
// Path includes the start ID and the final item ID.
type AssociationHit struct {
	Item  Item     `json:"item"`
	Path  []string `json:"path"`
	Depth int      `json:"depth"`
}

type AssociationResult struct {
	Hits       []AssociationHit `json:"hits"`
	Truncated  bool             `json:"truncated"`
	Incomplete bool             `json:"incomplete"`
	GapReason  string           `json:"gap_reason,omitempty"`
}

// ContextQuery fixes a small set of dimensions and returns the mixed view for
// human review or LLM synthesis. It must not become an unbounded scan dump.
type ContextQuery struct {
	ScanID        string            `json:"scan_id"`
	Subject       string            `json:"subject,omitempty"`
	SubjectKind   string            `json:"subject_kind,omitempty"`
	ContextEquals map[string]string `json:"context_equals,omitempty"`
	Since         *time.Time        `json:"since,omitempty"`
	Until         *time.Time        `json:"until,omitempty"`
	Limit         int               `json:"limit,omitempty"`
}

func (q *ContextQuery) Normalize() {
	if q == nil {
		return
	}
	q.Limit = NormalizeLimit(q.Limit)
}

func (q ContextQuery) Validate() error {
	if q.ScanID == "" {
		return errors.New("infoitem: context query scan_id is required")
	}
	if q.Limit < 1 || q.Limit > MaxQueryLimit {
		return fmt.Errorf("infoitem: limit must be within [1,%d]", MaxQueryLimit)
	}
	if q.Since != nil && q.Until != nil && q.Until.Before(*q.Since) {
		return errors.New("infoitem: until must not precede since")
	}
	return nil
}

type ContextResult struct {
	Items      []Item `json:"items"`
	Truncated  bool   `json:"truncated"`
	Incomplete bool   `json:"incomplete"`
	GapReason  string `json:"gap_reason,omitempty"`
}

// Reader is implemented by the PostgreSQL cold-path adapter and by bounded
// in-memory test adapters. A nil or disabled Reader means cross-scan
// activation is unavailable, not that scanning has failed.
type Reader interface {
	Region(ctx context.Context, q RegionQuery) (RegionResult, error)
	Associate(ctx context.Context, q AssociationQuery) (AssociationResult, error)
	ContextSlice(ctx context.Context, q ContextQuery) (ContextResult, error)
}

func NormalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultQueryLimit
	}
	if limit > MaxQueryLimit {
		return MaxQueryLimit
	}
	return limit
}
