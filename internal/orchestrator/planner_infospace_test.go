package orchestrator

// planner_infospace_test.go — IG2 acceptance: the planner's bounded
// information-space lighting and its lexical fallback. No PG required: the
// tests inject a fake infospace.Reader through PlannerConfig.InfoSpace.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

type fakeInfoSpaceReader struct {
	region     infospace.RegionResult
	contextRes infospace.ContextResult
	regionErr  error
	contextErr error

	regionCalls  []infospace.RegionQuery
	contextCalls []infospace.ContextQuery
}

func (f *fakeInfoSpaceReader) Region(_ context.Context, q infospace.RegionQuery) (infospace.RegionResult, error) {
	f.regionCalls = append(f.regionCalls, q)
	return f.region, f.regionErr
}

func (f *fakeInfoSpaceReader) Associate(context.Context, infospace.AssociationQuery) (infospace.AssociationResult, error) {
	return infospace.AssociationResult{}, nil
}

func (f *fakeInfoSpaceReader) ContextSlice(_ context.Context, q infospace.ContextQuery) (infospace.ContextResult, error) {
	f.contextCalls = append(f.contextCalls, q)
	return f.contextRes, f.contextErr
}

// fakeItems builds n items with descending observed_at and shuffled-ish IDs so
// the planner's defensive ordering is exercised.
func fakeItems(n int, layer infospace.Layer, kind string) []infospace.Item {
	base := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	items := make([]infospace.Item, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, infospace.Item{
			ScanID:     "run-ig2",
			RunID:      "run-ig2",
			InfoID:     infoIDFor(i),
			Layer:      layer,
			Kind:       kind,
			State:      "open",
			UnitKey:    "unit-" + infoIDFor(i),
			Subject:    "10.0.1.5:8080",
			Purpose:    "sqli",
			Nature:     "observed",
			ObservedAt: base.Add(-time.Duration(i) * time.Minute),
			Payload:    []byte(`{"body":"SECRET-BODY-MUST-NOT-LEAK"}`),
		})
	}
	return items
}

// infoIDFor gives a zero-padded id so lexicographic order equals index order.
func infoIDFor(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return "info-0" + string(digits[i])
	}
	return "info-" + string(digits[i/10]) + string(digits[i%10])
}

func countInfoSpaceItemLines(out string) int {
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- [aggregate/") || strings.HasPrefix(line, "- [atom/") {
			count++
		}
	}
	return count
}

func TestPlannerOverviewInfoSpaceBlockBoundedAndDeterministic(t *testing.T) {
	// Deliberately unsorted input with more rows than the block budget.
	regionItems := fakeItems(30, infospace.LayerAggregate, "belief")
	contextItems := fakeItems(15, infospace.LayerAggregate, "fact")
	for i := range contextItems {
		contextItems[i].InfoID = "ctx-" + infoIDFor(i)
	}
	reader := &fakeInfoSpaceReader{
		region:     infospace.RegionResult{Items: regionItems},
		contextRes: infospace.ContextResult{Items: contextItems},
	}
	p := &Planner{cfg: PlannerConfig{
		ScanID: "run-ig2", Repo: graph.NewMemoryStore(), InfoSpace: reader,
		Target: "10.0.1.5:8080",
	}, ctx: context.Background()}

	first := p.overview()
	second := p.overview()
	third := p.overview()
	if first != second || second != third {
		t.Fatalf("overview is not stable across identical snapshots:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if !strings.Contains(first, "Information space") {
		t.Fatalf("missing information-space block:\n%s", first)
	}
	for _, want := range []string{"aggregate/belief/open", "aggregate/fact/open", "subject=10.0.1.5:8080", "nature=observed"} {
		if !strings.Contains(first, want) {
			t.Fatalf("information-space block missing %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "SECRET-BODY") {
		t.Fatalf("information-space block leaked a payload body:\n%s", first)
	}
	if got := countInfoSpaceItemLines(first); got > infoSpaceBlockMaxLines {
		t.Fatalf("information-space item lines = %d, want <= %d:\n%s", got, infoSpaceBlockMaxLines, first)
	} else if got != infoSpaceBlockMaxLines {
		t.Fatalf("information-space item lines = %d, want the full bounded budget %d", got, infoSpaceBlockMaxLines)
	}
	// Newest region items are kept, oldest are dropped.
	if !strings.Contains(first, "info-00") {
		t.Fatalf("newest region item missing:\n%s", first)
	}
	if strings.Contains(first, "info-29") {
		t.Fatalf("oldest region item past the limit leaked in:\n%s", first)
	}
}

func TestPlannerOverviewInfoSpaceQueryCoordinates(t *testing.T) {
	reader := &fakeInfoSpaceReader{
		region:     infospace.RegionResult{Items: fakeItems(2, infospace.LayerAggregate, "belief")},
		contextRes: infospace.ContextResult{Items: fakeItems(1, infospace.LayerAggregate, "fact")},
	}
	p := &Planner{cfg: PlannerConfig{
		ScanID: "run-ig2", Repo: graph.NewMemoryStore(), InfoSpace: reader,
		Target:            "10.0.1.5:8080",
		ControlPlaneHosts: []string{"tsecbench.zc.tencent.com"},
		ObjectiveCriteria: []PlannerCriterion{{ID: "C1", VulnClass: "sqli", Required: true}},
	}, ctx: context.Background()}
	p.overview()
	if len(reader.regionCalls) != 1 {
		t.Fatalf("region calls = %d, want 1", len(reader.regionCalls))
	}
	q := reader.regionCalls[0]
	if len(q.RunIDs) != 1 || q.RunIDs[0] != "run-ig2" {
		t.Fatalf("region run ids = %v", q.RunIDs)
	}
	if q.Subject != "10.0.1.5:8080" {
		t.Fatalf("region subject = %q, want the concrete target", q.Subject)
	}
	if q.Purpose != "sqli" {
		t.Fatalf("region purpose = %q, want the one explicit vuln class", q.Purpose)
	}
	if q.Limit != infoSpaceRegionLimit {
		t.Fatalf("region limit = %d, want %d", q.Limit, infoSpaceRegionLimit)
	}
	if len(reader.contextCalls) != 1 || reader.contextCalls[0].Subject != "10.0.1.5:8080" {
		t.Fatalf("context calls = %+v", reader.contextCalls)
	}

	// A placeholder target and ambiguous classes must not become filters.
	reader2 := &fakeInfoSpaceReader{}
	p2 := &Planner{cfg: PlannerConfig{
		ScanID: "run-ig2", Repo: graph.NewMemoryStore(), InfoSpace: reader2,
		Target: "CONTAINER_ADDR",
		ObjectiveCriteria: []PlannerCriterion{
			{ID: "C1", VulnClass: "sqli"}, {ID: "C2", VulnClass: "xss"},
		},
	}, ctx: context.Background()}
	p2.overview()
	if len(reader2.regionCalls) != 1 {
		t.Fatalf("region calls = %d, want 1", len(reader2.regionCalls))
	}
	if reader2.regionCalls[0].Subject != "" || reader2.regionCalls[0].Purpose != "" {
		t.Fatalf("placeholder target / ambiguous class leaked into the query: %+v", reader2.regionCalls[0])
	}
	if len(reader2.contextCalls) != 0 {
		t.Fatalf("context slice queried without a concrete subject: %+v", reader2.contextCalls)
	}
}

func TestPlannerOverviewInfoSpaceFallback(t *testing.T) {
	// No reader at all: the lexical prior-work block is the only lighting and
	// the overview must not panic or mention the information space.
	graph.SetBackend(nil)
	t.Cleanup(func() { graph.SetBackend(nil) })
	sctx := scanctx.New("run-fallback", "")
	sctx.Ledger.Upsert(scanctx.Hypothesis{
		ID: "H-1", VulnClass: "sqli", Endpoint: "/login", Status: scanctx.HypothesisQueued,
		NextAction: "confirm error-based injection",
	})
	p := &Planner{cfg: PlannerConfig{
		ScanID: "run-fallback", Repo: graph.NewMemoryStore(), ScanCtx: sctx,
		Target: "10.0.1.5:8080", Goal: "find SQL injection",
	}, ctx: context.Background()}
	out := p.overview()
	if strings.Contains(out, "Information space") {
		t.Fatalf("no reader configured but the information-space block rendered:\n%s", out)
	}
	if !strings.Contains(out, "Prior work") {
		t.Fatalf("lexical prior-work fallback missing:\n%s", out)
	}

	// Reader present but failing (PG down / views not migrated): same fallback.
	reader := &fakeInfoSpaceReader{regionErr: errors.New("graph: information space unavailable")}
	p2 := &Planner{cfg: PlannerConfig{
		ScanID: "run-fallback", Repo: graph.NewMemoryStore(), ScanCtx: sctx,
		InfoSpace: reader, Target: "10.0.1.5:8080", Goal: "find SQL injection",
	}, ctx: context.Background()}
	out2 := p2.overview()
	if strings.Contains(out2, "Information space") {
		t.Fatalf("failing reader still rendered the block:\n%s", out2)
	}
	if !strings.Contains(out2, "Prior work") {
		t.Fatalf("prior-work fallback missing after reader error:\n%s", out2)
	}
}

func TestPlannerOverviewInfoSpaceEmptyResultFallsBack(t *testing.T) {
	reader := &fakeInfoSpaceReader{} // no items
	p := &Planner{cfg: PlannerConfig{
		ScanID: "run-empty", Repo: graph.NewMemoryStore(), InfoSpace: reader,
		Target: "10.0.1.5:8080",
	}, ctx: context.Background()}
	out := p.overview()
	if strings.Contains(out, "Information space") {
		t.Fatalf("empty information space rendered an empty header:\n%s", out)
	}
}
