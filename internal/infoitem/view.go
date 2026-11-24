package infoitem

// view.go — the three-layer read views used by planners, workers and panels.
//
// The model must never receive a full Evidence body through the graph: L0 is
// the index (IDs, nature, a short title/summary), L1 is the bounded detail a
// caller fetches explicitly, and L2 is an opaque artifact reference (archive /
// traffic / file) that never carries bytes. These are pure projections; they
// do not change the stored Item or the ledger's 40/4096 limits.

import (
	"strings"
	"time"
)

const (
	// DefaultIndexTitleRunes bounds an index title.
	DefaultIndexTitleRunes = 80
	// DefaultIndexSummaryRunes bounds an index summary.
	DefaultIndexSummaryRunes = 240
	// MaxDetailRunes bounds one explicitly fetched detail body.
	MaxDetailRunes = 8192
)

// IndexEntry is the L0 shape: cheap to render, safe for every prompt/panel.
type IndexEntry struct {
	ID          string    `json:"id"`
	ScanID      string    `json:"scan_id"`
	Nature      Nature    `json:"nature"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	SubjectKind string    `json:"subject_kind,omitempty"`
	Subject     string    `json:"subject,omitempty"`
	Purpose     string    `json:"purpose,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	EvidenceID  string    `json:"evidence_id,omitempty"`
	HasDetail   bool      `json:"has_detail"`
	HasArtifact bool      `json:"has_artifact"`
	Confidence  float64   `json:"confidence,omitempty"`
	ObservedAt  time.Time `json:"observed_at,omitempty"`
	Truncated   bool      `json:"truncated,omitempty"`
}

// Detail is the L1 shape: an explicit, bounded read.
type Detail struct {
	ID         string    `json:"id"`
	ScanID     string    `json:"scan_id"`
	Nature     Nature    `json:"nature"`
	Title      string    `json:"title"`
	Text       string    `json:"text"`
	Supports   []string  `json:"supports,omitempty"`
	Source     Source    `json:"source"`
	Subject    Subject   `json:"subject"`
	Purpose    string    `json:"purpose,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
}

// ArtifactRef is the L2 shape: references only, no content bytes.
type ArtifactRef struct {
	ID         string `json:"id"`
	ScanID     string `json:"scan_id"`
	EvidenceID string `json:"evidence_id,omitempty"`
	InfoItemID string `json:"info_item_id,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	HasDetail  bool   `json:"has_detail"`
}

// Index projects one Item to L0. titleLimit/summaryLimit are rune bounds.
func Index(item Item, titleLimit, summaryLimit int) IndexEntry {
	if titleLimit <= 0 {
		titleLimit = DefaultIndexTitleRunes
	}
	if summaryLimit <= 0 {
		summaryLimit = DefaultIndexSummaryRunes
	}
	text := strings.TrimSpace(item.Coordinates.Content.Text)
	title, titleTrunc := firstLine(text, titleLimit)
	summary, summaryTrunc := flatten(text, summaryLimit)
	return IndexEntry{
		ID:          item.ID,
		ScanID:      item.ScanID,
		Nature:      NormalizeNature(item.Nature),
		Title:       title,
		Summary:     summary,
		SubjectKind: item.Coordinates.Subject.Kind,
		Subject:     item.Coordinates.Subject.Identifier,
		Purpose:     item.Coordinates.Purpose,
		AgentID:     item.Coordinates.Source.AgentID,
		EvidenceID:  item.Coordinates.Source.EvidenceID,
		HasDetail:   text != "",
		HasArtifact: hasArtifact(item),
		Confidence:  item.Confidence,
		ObservedAt:  item.Coordinates.ObservedAt,
		Truncated:   titleTrunc || summaryTrunc,
	}
}

// DetailOf projects one Item to L1, bounded by max runes.
func DetailOf(item Item, max int) Detail {
	if max <= 0 || max > MaxDetailRunes {
		max = MaxDetailRunes
	}
	text, truncated := truncateRunes(strings.TrimSpace(item.Coordinates.Content.Text), max)
	title, _ := firstLine(text, DefaultIndexTitleRunes)
	return Detail{
		ID:         item.ID,
		ScanID:     item.ScanID,
		Nature:     NormalizeNature(item.Nature),
		Title:      title,
		Text:       text,
		Supports:   append([]string(nil), item.Supports...),
		Source:     item.Coordinates.Source,
		Subject:    item.Coordinates.Subject,
		Purpose:    item.Coordinates.Purpose,
		ObservedAt: item.Coordinates.ObservedAt,
		Truncated:  truncated,
	}
}

// ArtifactOf projects one Item to L2.
func ArtifactOf(item Item) ArtifactRef {
	return ArtifactRef{
		ID:         item.ID,
		ScanID:     item.ScanID,
		EvidenceID: item.Coordinates.Source.EvidenceID,
		InfoItemID: item.ID,
		Kind:       item.Coordinates.Source.Kind,
		Name:       item.Coordinates.Source.Name,
		HasDetail:  strings.TrimSpace(item.Coordinates.Content.Text) != "",
	}
}

// NormalizeNature defaults unknown values to inferred (the ledger rule).
func NormalizeNature(n Nature) Nature {
	if n == NatureObserved {
		return NatureObserved
	}
	return NatureInferred
}

func hasArtifact(item Item) bool {
	return item.Coordinates.Source.EvidenceID != "" ||
		item.Coordinates.Source.Kind == "archive" ||
		item.Coordinates.Source.Kind == "traffic"
}

func firstLine(text string, max int) (string, bool) {
	line := text
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	return truncateRunes(line, max)
}

func flatten(text string, max int) (string, bool) {
	flat := strings.Join(strings.Fields(text), " ")
	return truncateRunes(flat, max)
}

func truncateRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return "", s != ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s, false
	}
	return string(r[:max]) + "…", true
}
