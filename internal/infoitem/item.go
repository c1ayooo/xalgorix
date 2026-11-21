// Package infoitem defines the durable information model described by
// docs/CORE_PHILOSOPHY.md. It is deliberately independent of scanctx so the
// hot-path ledger can project stable evidence into it without import cycles.
package infoitem

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Nature separates an observation from an interpretation. A high-confidence
// inferred item is still only an interpretation and never proof of a finding.
type Nature string

const (
	NatureObserved Nature = "observed"
	NatureInferred Nature = "inferred"
)

func (n Nature) Valid() bool {
	return n == NatureObserved || n == NatureInferred
}

// CoordinateStatus records whether the seven-dimensional coordinates have
// been extracted. It says nothing about whether the content is true.
type CoordinateStatus string

const (
	CoordinatesComplete CoordinateStatus = "complete"
	CoordinatesPartial  CoordinateStatus = "partial"
	CoordinatesPending  CoordinateStatus = "pending"
)

func (s CoordinateStatus) Valid() bool {
	return s == "" || s == CoordinatesComplete || s == CoordinatesPartial || s == CoordinatesPending
}

// Content is the semantic payload. MediaType keeps raw tool output
// distinguishable from prose and structured JSON without hiding its origin.
type Content struct {
	Text      string `json:"text"`
	MediaType string `json:"media_type,omitempty"`
}

// Source identifies who produced the item. EvidenceID is set when the item is
// projected from a ledger Evidence record.
type Source struct {
	Kind       string `json:"kind"`
	Name       string `json:"name,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	EvidenceID string `json:"evidence_id,omitempty"`
}

// Subject identifies the target object the information is about.
type Subject struct {
	Kind       string `json:"kind,omitempty"`
	Identifier string `json:"identifier"`
}

// Coordinates are the seven semantic dimensions. Time is represented by
// ObservedAt/ValidFrom/ValidUntil; relations are represented by RelationIDs
// and the edge table rather than duplicated prose.
type Coordinates struct {
	Content     Content           `json:"content"`
	Source      Source            `json:"source"`
	ObservedAt  time.Time         `json:"observed_at"`
	Subject     Subject           `json:"subject"`
	Purpose     string            `json:"purpose"`
	Context     map[string]string `json:"context,omitempty"`
	RelationIDs []string          `json:"relation_ids,omitempty"`
}

// Item is one global information instance. The same observation at the same
// coordinates is still a distinct instance with a distinct ID.
type Item struct {
	ID               string           `json:"id"`
	ScanID           string           `json:"scan_id"`
	SourceSeq        uint64           `json:"source_seq"`
	Nature           Nature           `json:"nature"`
	Coordinates      Coordinates      `json:"coordinates"`
	Supports         []string         `json:"supports,omitempty"`
	ValidFrom        time.Time        `json:"valid_from,omitempty"`
	ValidUntil       *time.Time       `json:"valid_until,omitempty"`
	Confidence       float64          `json:"confidence,omitempty"`
	CoordinateStatus CoordinateStatus `json:"coordinate_status,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
}

// RelationType is the edge vocabulary used by association lighting.
type RelationType string

const (
	RelationSupports    RelationType = "supports"
	RelationContradicts RelationType = "contradicts"
	RelationReferences  RelationType = "references"
	RelationSameSession RelationType = "same_session"
	RelationSameSubject RelationType = "same_subject"
)

func (r RelationType) Valid() bool {
	switch r {
	case RelationSupports, RelationContradicts, RelationReferences, RelationSameSession, RelationSameSubject:
		return true
	default:
		return false
	}
}

// Relation is an explicit edge between two InfoItems. SourceSeq is the
// domain-journal sequence that produced the edge, making replay idempotent.
type Relation struct {
	ScanID    string       `json:"scan_id"`
	SourceSeq uint64       `json:"source_seq"`
	FromID    string       `json:"from_id"`
	ToID      string       `json:"to_id"`
	Type      RelationType `json:"type"`
	Weight    float64      `json:"weight,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
}

func (r Relation) Validate() error {
	if strings.TrimSpace(r.ScanID) == "" {
		return errors.New("infoitem: relation scan_id is required")
	}
	if r.FromID == "" || r.ToID == "" {
		return errors.New("infoitem: relation endpoints are required")
	}
	if r.FromID == r.ToID {
		return errors.New("infoitem: self relation is not allowed")
	}
	if !r.Type.Valid() {
		return fmt.Errorf("infoitem: invalid relation type %q", r.Type)
	}
	if r.SourceSeq == 0 {
		return errors.New("infoitem: relation source_seq must be positive")
	}
	if math.IsNaN(r.Weight) || math.IsInf(r.Weight, 0) || r.Weight < 0 || r.Weight > 1 {
		return errors.New("infoitem: relation weight must be within [0,1]")
	}
	return nil
}

// Substantiated reports whether an inferred item names at least one support.
// Missing supports do not block ingestion, but such an item must not enter
// proof, activation, or finding decisions.
func (i Item) Substantiated() bool {
	return i.Nature != NatureInferred || len(i.Supports) > 0
}

// Validate checks structural invariants only. It does not decide truth.
func (i Item) Validate() error {
	if strings.TrimSpace(i.ID) == "" {
		return errors.New("infoitem: id is required")
	}
	if strings.TrimSpace(i.ScanID) == "" {
		return errors.New("infoitem: scan_id is required")
	}
	if i.SourceSeq == 0 {
		return errors.New("infoitem: source_seq must be positive")
	}
	if !i.Nature.Valid() {
		return fmt.Errorf("infoitem: invalid nature %q", i.Nature)
	}
	if strings.TrimSpace(i.Coordinates.Content.Text) == "" {
		return errors.New("infoitem: content is required")
	}
	if strings.TrimSpace(i.Coordinates.Source.Kind) == "" {
		return errors.New("infoitem: source kind is required")
	}
	if strings.TrimSpace(i.Coordinates.Subject.Identifier) == "" {
		return errors.New("infoitem: subject identifier is required")
	}
	if i.CreatedAt.IsZero() {
		return errors.New("infoitem: created_at is required")
	}
	if i.ValidFrom.After(time.Time{}) && i.ValidUntil != nil && !i.ValidUntil.After(i.ValidFrom) {
		return errors.New("infoitem: valid_until must be after valid_from")
	}
	if math.IsNaN(i.Confidence) || math.IsInf(i.Confidence, 0) || i.Confidence < 0 || i.Confidence > 1 {
		return errors.New("infoitem: confidence must be within [0,1]")
	}
	if !i.CoordinateStatus.Valid() {
		return fmt.Errorf("infoitem: invalid coordinate status %q", i.CoordinateStatus)
	}
	for n, support := range i.Supports {
		if strings.TrimSpace(support) == "" {
			return fmt.Errorf("infoitem: supports[%d] is empty", n)
		}
	}
	return nil
}

// NewID returns a process-independent random identifier suitable for a global
// InfoItem instance.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("infoitem: generate id: %w", err)
	}
	return "ii-" + hex.EncodeToString(b[:]), nil
}

// StableEvidenceID maps a ledger evidence instance to a deterministic global
// identifier. The stable mapping lets idempotent cold-path replay converge on
// the same row.
func StableEvidenceID(scanID, evidenceID string) string {
	return "evidence:" + strings.TrimSpace(scanID) + ":" + strings.TrimSpace(evidenceID)
}

// NewEvidenceItem projects one ledger Evidence instance into an InfoItem.
func NewEvidenceItem(scanID, evidenceID string, nature Nature, content Content, source Source, subject Subject, observedAt time.Time, seq uint64) Item {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	if source.EvidenceID == "" {
		source.EvidenceID = evidenceID
	}
	return Item{
		ID:        StableEvidenceID(scanID, evidenceID),
		ScanID:    scanID,
		SourceSeq: seq,
		Nature:    nature,
		Coordinates: Coordinates{
			Content:    content,
			Source:     source,
			ObservedAt: observedAt,
			Subject:    subject,
		},
		CoordinateStatus: CoordinatesComplete,
		CreatedAt:        time.Now().UTC(),
	}
}

// Normalize fills safe defaults without promoting an inferred item to an
// observation.
func (i *Item) Normalize() {
	if i == nil {
		return
	}
	if i.Nature == "" {
		i.Nature = NatureInferred
	}
	if i.CoordinateStatus == "" {
		i.CoordinateStatus = CoordinatesComplete
	}
	if i.CreatedAt.IsZero() {
		i.CreatedAt = time.Now().UTC()
	}
	if i.Coordinates.ObservedAt.IsZero() {
		i.Coordinates.ObservedAt = i.CreatedAt
	}
}
