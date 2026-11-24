package infospace

import (
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

func TestRelationsQueryNormalizeValidate(t *testing.T) {
	q := RelationsQuery{RunIDs: []string{"run-1"}}
	q.Normalize()
	if q.Limit != RelationsDefaultLimit {
		t.Fatalf("limit = %d, want %d", q.Limit, RelationsDefaultLimit)
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("valid relations query rejected: %v", err)
	}
	if err := (RelationsQuery{AllRuns: true, RunIDs: []string{"r"}}).Validate(); err == nil {
		t.Fatal("all_runs + run_ids must be rejected")
	}
	if err := (RelationsQuery{}).Validate(); err == nil {
		t.Fatal("empty scope must be rejected")
	}
	huge := RelationsQuery{RunIDs: []string{"r"}, Limit: RelationsMaxLimit + 1}
	huge.Normalize()
	if huge.Limit != RelationsMaxLimit {
		t.Fatalf("normalized limit = %d, want %d", huge.Limit, RelationsMaxLimit)
	}
	after := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	before := after.Add(-time.Minute)
	if err := (RelationsQuery{RunIDs: []string{"r"}, ObservedAfter: &after, ObservedBefore: &before}).Validate(); err == nil {
		t.Fatal("before < after must be rejected")
	}
}

func TestRelationCategoryOf(t *testing.T) {
	for _, rel := range []string{"spawns", "derived_from", "yields", "proves", "supports", "covers", "depends_on", "future_rel"} {
		if RelationCategoryOf(rel) != RelationChain {
			t.Fatalf("%s must be a chain relation", rel)
		}
	}
	for _, rel := range []string{"references", "contradicts", "same_session", "same_subject", "materializes"} {
		if RelationCategoryOf(rel) != RelationInfo {
			t.Fatalf("%s must be an info relation", rel)
		}
	}
}

func TestHeatmapQueryNormalizeValidate(t *testing.T) {
	q := HeatmapQuery{RunIDs: []string{"run-1"}}
	q.Normalize()
	if q.RowLimit != HeatmapDefaultRows {
		t.Fatalf("row limit = %d, want %d", q.RowLimit, HeatmapDefaultRows)
	}
	if len(q.Types) != len(DefaultInfoTypes()) {
		t.Fatalf("default types = %v", q.Types)
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("valid heatmap query rejected: %v", err)
	}

	if err := (HeatmapQuery{AllRuns: true, RunIDs: []string{"r"}}).Validate(); err == nil {
		t.Fatal("all_runs + run_ids must be rejected")
	}
	if err := (HeatmapQuery{}).Validate(); err == nil {
		t.Fatal("empty scope must be rejected")
	}
	if err := (HeatmapQuery{RunIDs: []string{"r"}, Types: []InfoType{"bogus"}}).Validate(); err == nil {
		t.Fatal("unknown info type must be rejected")
	}

	huge := HeatmapQuery{RunIDs: []string{"r"}, RowLimit: HeatmapMaxRows + 50}
	huge.Normalize()
	if huge.RowLimit != HeatmapMaxRows {
		t.Fatalf("normalized row limit = %d, want %d", huge.RowLimit, HeatmapMaxRows)
	}

	after := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	before := after.Add(-time.Minute)
	if err := (HeatmapQuery{RunIDs: []string{"r"}, ObservedAfter: &after, ObservedBefore: &before}).Validate(); err == nil {
		t.Fatal("before < after must be rejected")
	}
}

func TestRegionQueryNormalizeValidate(t *testing.T) {
	q := RegionQuery{RunIDs: []string{"run-1"}}
	q.Normalize()
	if q.Limit != DefaultLimit {
		t.Fatalf("limit = %d, want %d", q.Limit, DefaultLimit)
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("valid query rejected: %v", err)
	}

	if err := (RegionQuery{AllRuns: true, RunIDs: []string{"run-1"}}).Validate(); err == nil {
		t.Fatal("all_runs + run_ids must be rejected")
	}
	if err := (RegionQuery{}).Validate(); err == nil {
		t.Fatal("empty scope must be rejected")
	}
	if err := (RegionQuery{RunIDs: []string{""}}).Validate(); err == nil {
		t.Fatal("empty run id must be rejected")
	}
	if err := (RegionQuery{RunIDs: []string{"r"}, Layer: "bogus"}).Validate(); err == nil {
		t.Fatal("invalid layer must be rejected")
	}
	if err := (RegionQuery{RunIDs: []string{"r"}, Nature: "guess"}).Validate(); err == nil {
		t.Fatal("invalid nature must be rejected")
	}
	if err := (RegionQuery{RunIDs: []string{"r"}, Limit: MaxLimit + 1}).Validate(); err == nil {
		t.Fatal("over-limit must be rejected")
	}

	huge := RegionQuery{RunIDs: []string{"r"}, Limit: MaxLimit + 5}
	huge.Normalize()
	if huge.Limit != MaxLimit {
		t.Fatalf("normalized limit = %d, want %d", huge.Limit, MaxLimit)
	}

	after := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	before := after.Add(-time.Minute)
	if err := (RegionQuery{RunIDs: []string{"r"}, ObservedAfter: &after, ObservedBefore: &before}).Validate(); err == nil {
		t.Fatal("before < after must be rejected")
	}

	cursor := &infoitem.RegionCursor{ObservedAt: after, ID: "info-1"}
	q2 := RegionQuery{RunIDs: []string{"r"}, Cursor: cursor}
	q2.Normalize()
	if err := q2.Validate(); err != nil {
		t.Fatalf("valid cursor rejected: %v", err)
	}
	if err := (RegionQuery{RunIDs: []string{"r"}, Cursor: &infoitem.RegionCursor{ID: "x"}}).Validate(); err == nil {
		t.Fatal("cursor without observed_at must be rejected")
	}
}

func TestAssociationQueryBoundsAndRelations(t *testing.T) {
	q := AssociationQuery{RunID: "run-1", StartIDs: []string{"a"}}
	q.Normalize()
	if q.MaxDepth != DefaultDepth || q.Limit != 20 {
		t.Fatalf("defaults = depth %d limit %d", q.MaxDepth, q.Limit)
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("valid query rejected: %v", err)
	}

	deep := AssociationQuery{RunID: "r", StartIDs: []string{"a"}, MaxDepth: 9}
	deep.Normalize()
	if deep.MaxDepth != MaxDepth {
		t.Fatalf("clamped depth = %d, want %d", deep.MaxDepth, MaxDepth)
	}
	if err := deep.Validate(); err != nil {
		t.Fatalf("clamped depth rejected: %v", err)
	}

	if err := (AssociationQuery{StartIDs: []string{"a"}}).Validate(); err == nil {
		t.Fatal("missing run id must be rejected")
	}
	if err := (AssociationQuery{RunID: "r"}).Validate(); err == nil {
		t.Fatal("missing start ids must be rejected")
	}
	if err := (AssociationQuery{RunID: "r", StartIDs: []string{"a"}, EdgeTypes: []string{"bogus"}}).Validate(); err == nil {
		t.Fatal("unknown relation must be rejected")
	}
	for _, rel := range []string{
		"supports", "contradicts", "references", "same_session", "same_subject",
		"spawns", "derived_from", "yields", "proves", "covers", "depends_on",
		RelationMaterializes,
	} {
		if !KnownRelation(rel) {
			t.Fatalf("relation %q should be known", rel)
		}
	}
}

func TestContextQueryValidate(t *testing.T) {
	q := ContextQuery{RunID: "run-1"}
	q.Normalize()
	if q.Limit != DefaultLimit {
		t.Fatalf("limit = %d, want %d", q.Limit, DefaultLimit)
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("valid context rejected: %v", err)
	}
	if err := (ContextQuery{}).Validate(); err == nil {
		t.Fatal("missing run id must be rejected")
	}
	since := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	until := since.Add(-time.Minute)
	if err := (ContextQuery{RunID: "r", Since: &since, Until: &until}).Validate(); err == nil {
		t.Fatal("until < since must be rejected")
	}
}
