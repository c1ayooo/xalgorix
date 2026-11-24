package infoitem

import (
	"testing"
	"time"
)

func TestInferRelationsDeterministicEdges(t *testing.T) {
	item := Item{
		ID:     "new",
		ScanID: "scan-1",
		Coordinates: Coordinates{
			Subject: Subject{Kind: "endpoint", Identifier: "/api/orders"},
			Purpose: "idor",
			Source:  Source{AgentID: "agent-1"},
			Context: map[string]string{
				"evidence_kind": "control",
				"hypothesis":    "H-1",
			},
		},
	}
	candidates := []RelationCandidate{
		{ID: "same-subject", Subject: Subject{Kind: "endpoint", Identifier: "/api/orders"}, Purpose: "xss"},
		{ID: "same-session", Subject: Subject{Kind: "endpoint", Identifier: "/other"}, AgentID: "agent-1"},
		{ID: "positive", Subject: Subject{Kind: "endpoint", Identifier: "/api/orders"}, Purpose: "idor", EvidenceKind: "exploit", HypothesisID: "H-1"},
		{ID: "other-hypothesis", Subject: Subject{Kind: "endpoint", Identifier: "/api/orders"}, Purpose: "idor", EvidenceKind: "exploit", HypothesisID: "H-2"},
		{ID: "different-purpose", Subject: Subject{Kind: "endpoint", Identifier: "/api/orders"}, Purpose: "sqli", EvidenceKind: "exploit", HypothesisID: "H-1"},
		{ID: "new"},
	}
	got := InferRelations(item, candidates, 9, time.Unix(100, 0))

	byTarget := map[string]map[RelationType]bool{}
	for _, rel := range got {
		if rel.ScanID != "scan-1" || rel.SourceSeq != 9 || rel.FromID != "new" || rel.Weight != 1 {
			t.Fatalf("unexpected relation envelope: %+v", rel)
		}
		if byTarget[rel.ToID] == nil {
			byTarget[rel.ToID] = map[RelationType]bool{}
		}
		if byTarget[rel.ToID][rel.Type] {
			t.Fatalf("duplicate edge %s -> %s (%s)", rel.FromID, rel.ToID, rel.Type)
		}
		byTarget[rel.ToID][rel.Type] = true
	}

	if !byTarget["same-subject"][RelationSameSubject] {
		t.Fatalf("missing same_subject edge: %+v", got)
	}
	if !byTarget["same-session"][RelationSameSession] {
		t.Fatalf("missing same_session edge: %+v", got)
	}
	if !byTarget["positive"][RelationContradicts] {
		t.Fatalf("control vs exploit must contradict: %+v", got)
	}
	if byTarget["other-hypothesis"][RelationContradicts] {
		t.Fatalf("contradiction across hypotheses is not allowed: %+v", got)
	}
	if byTarget["different-purpose"][RelationContradicts] {
		t.Fatalf("contradiction across purposes is not allowed: %+v", got)
	}
	if len(byTarget["new"]) != 0 {
		t.Fatalf("self edge must be skipped: %+v", got)
	}
}

func TestInferRelationsRequiresPositiveEvidence(t *testing.T) {
	item := Item{
		ID:     "control",
		ScanID: "s",
		Coordinates: Coordinates{
			Subject: Subject{Kind: "endpoint", Identifier: "/login"},
			Purpose: "auth",
			Context: map[string]string{"evidence_kind": "control"},
		},
	}
	candidates := []RelationCandidate{
		{ID: "artifact", Subject: Subject{Kind: "endpoint", Identifier: "/login"}, Purpose: "auth", EvidenceKind: "artifact"},
	}
	for _, rel := range InferRelations(item, candidates, 1, time.Now()) {
		if rel.Type == RelationContradicts {
			t.Fatalf("artifact is not a positive exploit claim: %+v", rel)
		}
	}
}

func TestInferRelationsEmptyAgentNeverLinksSession(t *testing.T) {
	item := Item{ID: "a", ScanID: "s", Coordinates: Coordinates{Subject: Subject{Kind: "endpoint", Identifier: "/x"}}}
	candidates := []RelationCandidate{{ID: "b", Subject: Subject{Kind: "endpoint", Identifier: "/y"}}}
	for _, rel := range InferRelations(item, candidates, 1, time.Now()) {
		if rel.Type == RelationSameSession {
			t.Fatalf("empty agent id must not create a session edge: %+v", rel)
		}
	}
}
