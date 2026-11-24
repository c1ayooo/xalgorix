package infoitem

import (
	"strings"
	"time"
)

// RelationCandidate is the bounded slice of an existing InfoItem that
// deterministic relation inference needs. Callers load at most
// RelationCandidateLimit rows so neither memory nor edge count grows without
// bound.
type RelationCandidate struct {
	ID           string
	Subject      Subject
	Purpose      string
	EvidenceKind string
	AgentID      string
	HypothesisID string
}

// RelationCandidateLimit bounds the recent-item window inspected for one
// applied record. It matches the lexical recall window and keeps the number of
// edges written per record deterministic.
const RelationCandidateLimit = 20

var (
	controlEvidenceKinds = map[string]bool{
		"baseline": true,
		"control":  true,
	}
	positiveEvidenceKinds = map[string]bool{
		"probe":       true,
		"exploit":     true,
		"finding_ref": true,
	}
)

// InferRelations returns the deterministic same_subject, same_session and
// contradicts edges between item and a bounded candidate window. It performs
// no semantic scoring: every edge is justified by an exact field comparison,
// and the caller stores it idempotently by (scan_id, source_seq, ...).
//
//   - same_subject: identical subject kind+identifier.
//   - same_session: identical non-empty source agent id (the actor/session that
//     produced the item), never the empty string.
//   - contradicts: a control/baseline evidence item against a positive
//     probe/exploit/finding_ref item for the same subject and purpose (and the
//     same hypothesis when both name one). Control evidence shows the observed
//     behavior is intended; it contradicts the positive claim.
func InferRelations(item Item, candidates []RelationCandidate, seq uint64, now time.Time) []Relation {
	if item.ID == "" || seq == 0 || len(candidates) == 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	itemSubject := strings.TrimSpace(item.Coordinates.Subject.Identifier)
	itemKind := strings.TrimSpace(item.Coordinates.Subject.Kind)
	itemPurpose := strings.TrimSpace(item.Coordinates.Purpose)
	itemAgent := strings.TrimSpace(item.Coordinates.Source.AgentID)
	itemEvidenceKind := strings.ToLower(strings.TrimSpace(item.Coordinates.Context["evidence_kind"]))
	itemHypothesis := strings.TrimSpace(item.Coordinates.Context["hypothesis"])

	seen := make(map[[2]string]struct{}, len(candidates))
	out := make([]Relation, 0, len(candidates))
	add := func(toID string, typ RelationType) {
		key := [2]string{toID, string(typ)}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, Relation{
			ScanID:    item.ScanID,
			SourceSeq: seq,
			FromID:    item.ID,
			ToID:      toID,
			Type:      typ,
			Weight:    1,
			CreatedAt: now,
		})
	}

	for _, c := range candidates {
		if c.ID == "" || c.ID == item.ID {
			continue
		}
		cSubject := strings.TrimSpace(c.Subject.Identifier)
		cKind := strings.TrimSpace(c.Subject.Kind)

		if itemSubject != "" && cSubject != "" && itemSubject == cSubject && itemKind == cKind {
			add(c.ID, RelationSameSubject)
		}
		if itemAgent != "" && itemAgent == strings.TrimSpace(c.AgentID) {
			add(c.ID, RelationSameSession)
		}
		if itemSubject == "" || cSubject == "" || itemSubject != cSubject || itemPurpose == "" || itemPurpose != strings.TrimSpace(c.Purpose) {
			continue
		}
		if itemHypothesis != "" && c.HypothesisID != "" && itemHypothesis != c.HypothesisID {
			continue
		}
		newControl, oldControl := controlEvidenceKinds[itemEvidenceKind], controlEvidenceKinds[strings.ToLower(strings.TrimSpace(c.EvidenceKind))]
		newPositive := positiveEvidenceKinds[itemEvidenceKind]
		oldPositive := positiveEvidenceKinds[strings.ToLower(strings.TrimSpace(c.EvidenceKind))]
		if (newControl && oldPositive) || (newPositive && oldControl) {
			add(c.ID, RelationContradicts)
		}
	}
	return out
}
