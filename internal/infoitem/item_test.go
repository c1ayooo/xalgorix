package infoitem

import (
	"testing"
	"time"
)

func validItem() Item {
	return Item{
		ID:        "ii-1",
		ScanID:    "scan-1",
		SourceSeq: 1,
		Nature:    NatureObserved,
		Coordinates: Coordinates{
			Content:    Content{Text: "HTTP/1.1 200 OK"},
			Source:     Source{Kind: "tool", Name: "http_request"},
			ObservedAt: time.Now().UTC(),
			Subject:    Subject{Kind: "endpoint", Identifier: "https://example.test/api"},
			Purpose:    "verify access control",
		},
		CoordinateStatus: CoordinatesComplete,
		CreatedAt:        time.Now().UTC(),
	}
}

func TestItemValidateAndSubstantiation(t *testing.T) {
	item := validItem()
	if err := item.Validate(); err != nil {
		t.Fatal(err)
	}
	if !item.Substantiated() {
		t.Fatal("observed item must be substantiated")
	}

	item.Nature = NatureInferred
	if item.Substantiated() {
		t.Fatal("inferred item without supports must be unsubstantiated")
	}
	if err := item.Validate(); err != nil {
		t.Fatalf("unsubstantiated inferred item should remain ingestible: %v", err)
	}
	item.Supports = []string{"evidence:s:e1"}
	if !item.Substantiated() {
		t.Fatal("inferred item with supports must be substantiated")
	}
}

func TestItemRejectsInvalidConfidenceAndNature(t *testing.T) {
	item := validItem()
	item.Confidence = 1.1
	if err := item.Validate(); err == nil {
		t.Fatal("expected confidence error")
	}
	item = validItem()
	item.Nature = "guessed"
	if err := item.Validate(); err == nil {
		t.Fatal("expected nature error")
	}
}

func TestStableEvidenceID(t *testing.T) {
	got := StableEvidenceID("scan-1", "ev-1")
	if got != "evidence:scan-1:ev-1" {
		t.Fatalf("id = %q", got)
	}
	if got != StableEvidenceID("scan-1", "ev-1") {
		t.Fatal("stable evidence ID must be deterministic")
	}
}

func TestRelationValidate(t *testing.T) {
	rel := Relation{
		ScanID:    "scan-1",
		SourceSeq: 1,
		FromID:    "ii-1",
		ToID:      "ii-2",
		Type:      RelationSupports,
		Weight:    0.7,
	}
	if err := rel.Validate(); err != nil {
		t.Fatal(err)
	}
	rel.ToID = "ii-1"
	if err := rel.Validate(); err == nil {
		t.Fatal("expected self relation error")
	}
}

func TestQueryBudgets(t *testing.T) {
	q := AssociationQuery{ScanID: "s", StartIDs: []string{"ii-1"}, MaxDepth: 9, Limit: 999}
	q.Normalize()
	if q.MaxDepth != MaxAssociationDepth || q.Limit != MaxQueryLimit {
		t.Fatalf("unexpected normalized query: %+v", q)
	}
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The AllScans region form (CORE v2.1 §4.1 cross-scan shape) is mutually
// exclusive with an explicit ScanIDs list, and one of the two must be present.
func TestRegionQueryAllScansValidation(t *testing.T) {
	allScans := RegionQuery{AllScans: true, Subject: "/orders", Limit: 5}
	if err := allScans.Validate(); err != nil {
		t.Fatalf("all_scans with no scan_ids must validate: %v", err)
	}
	mixed := RegionQuery{AllScans: true, ScanIDs: []string{"scan-1"}, Subject: "/orders", Limit: 5}
	if err := mixed.Validate(); err == nil {
		t.Fatal("all_scans together with scan_ids must be rejected")
	}
	neither := RegionQuery{Subject: "/orders", Limit: 5}
	if err := neither.Validate(); err == nil {
		t.Fatal("neither scan_ids nor all_scans must be rejected")
	}
	perScan := RegionQuery{ScanIDs: []string{"scan-1"}, Subject: "/orders", Limit: 5}
	if err := perScan.Validate(); err != nil {
		t.Fatalf("explicit scan_ids must keep validating: %v", err)
	}
}
