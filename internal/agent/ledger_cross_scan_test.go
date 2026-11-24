package agent

// Contract tests for the M4 cross-scan activation consumers wired onto the
// ledger tools (ledger_cross_scan.go):
//
//   - region: read_ledger(filter=schedulable) annotates each schedulable entry
//     with same-endpoint cross-scan history (counts, deduplicated source scan
//     ids, truncation and §6.4 sync-gap flags), under a per-call query budget;
//   - association: claim_next_hypothesis appends a same-subject cross-scan
//     history section (region step) and a current-scan related-items line
//     (bounded edge traversal rooted at THIS scan's items — relation edges are
//     per-scan; cross-scan edge graphs are charter §13 research).
//
// The stub reader implements the full infoitem.Reader interface AND validates
// the queries it receives against the real infoitem contract, so a malformed
// consumer query cannot pass behind a permissive stub (the failure mode the
// M4 review caught). Every test unregisters its process-global registration.
// Output states are distinguishable by contract: reader absence and a truly
// empty result keep the output unchanged (normal no-DSN mode / true empty);
// a reader error surfaces as a visible one-line failure note and never fails
// the tool (charter §3.4: failures must not be silently dropped).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// crossScanStubReader is a fixed-result infoitem.Reader that records queries
// and validates them against the real infoitem contract.
type crossScanStubReader struct {
	region        infoitem.RegionResult
	regionErr     error
	assoc         infoitem.AssociationResult
	assocErr      error
	regionQueries []infoitem.RegionQuery
	assocQueries  []infoitem.AssociationQuery
	byID          map[string]infoitem.Item
	byIDErr       error
}

func (s *crossScanStubReader) Region(_ context.Context, q infoitem.RegionQuery) (infoitem.RegionResult, error) {
	s.regionQueries = append(s.regionQueries, q)
	if err := q.Validate(); err != nil {
		return infoitem.RegionResult{}, fmt.Errorf("stub rejected region query: %w", err)
	}
	if s.regionErr != nil {
		return infoitem.RegionResult{}, s.regionErr
	}
	return s.region, nil
}

func (s *crossScanStubReader) Associate(_ context.Context, q infoitem.AssociationQuery) (infoitem.AssociationResult, error) {
	s.assocQueries = append(s.assocQueries, q)
	if err := q.Validate(); err != nil {
		return infoitem.AssociationResult{}, fmt.Errorf("stub rejected association query: %w", err)
	}
	if s.assocErr != nil {
		return infoitem.AssociationResult{}, s.assocErr
	}
	return s.assoc, nil
}

func (s *crossScanStubReader) ContextSlice(_ context.Context, _ infoitem.ContextQuery) (infoitem.ContextResult, error) {
	return infoitem.ContextResult{}, nil
}

func (s *crossScanStubReader) GetByID(_ context.Context, scanID, id string) (infoitem.Item, error) {
	if s.byIDErr != nil {
		return infoitem.Item{}, s.byIDErr
	}
	item, ok := s.byID[scanID+"/"+id]
	if !ok {
		return infoitem.Item{}, infoitem.ErrNotFound
	}
	return item, nil
}

func crossScanItem(id, scanID string, nature infoitem.Nature) infoitem.Item {
	return infoitem.Item{
		ID:     id,
		ScanID: scanID,
		Nature: nature,
		Coordinates: infoitem.Coordinates{
			Content: infoitem.Content{Text: "prior observation " + id},
			Subject: infoitem.Subject{Kind: "endpoint", Identifier: "/orders"},
		},
	}
}

// seedSchedulableHypothesis records one queued hypothesis with an endpoint, so
// both the schedulable listing and a claim have a subject to annotate.
func seedSchedulableHypothesis(sc *scanctx.ScanContext, class, endpoint string, conf float64) scanctx.Hypothesis {
	return sc.Ledger.Upsert(scanctx.Hypothesis{
		VulnClass:     class,
		Endpoint:      endpoint,
		Confidence:    conf,
		Status:        scanctx.HypothesisQueued,
		Preconditions: []string{"unknown"},
		NextAction:    "probe " + endpoint,
	})
}

// crossScanTestWhitelist is the simulated project scan set for the contract
// tests: the current scan plus the older scans the stubs report. Cross-scan
// history is project-scoped (D7): tasks inside one project share history,
// projects never do, so the agent must carry an explicit project whitelist;
// there is no AllScans fallback.
var crossScanTestWhitelist = []string{"scan-cur", "scan-old-1", "scan-old-2", "scan-old-3", "scan-old-4"}

func newCrossScanAgent(t *testing.T, scanID string) *Agent {
	t.Helper()
	return newCrossScanAgentWithProject(t, scanID, crossScanTestWhitelist)
}

func newCrossScanAgentWithProject(t *testing.T, scanID string, scanIDs []string) *Agent {
	t.Helper()
	a := newCrossScanAgentNoProject(t, scanID)
	ids := append([]string(nil), scanIDs...)
	a.SetCrossScanScanIDsResolver(func() []string { return ids })
	return a
}

// newCrossScanAgentNoProject builds an agent with a reader but no project
// scope, mirroring a project-less run: cross-scan annotations must be
// suppressed instead of widening to every scan.
func newCrossScanAgentNoProject(t *testing.T, scanID string) *Agent {
	t.Helper()
	sc := scanctx.New(scanID, "")
	t.Cleanup(sc.Close)
	return &Agent{scanCtx: sc, ctx: context.Background()}
}

func TestReadLedgerSchedulableCrossScanRegionNote(t *testing.T) {
	a := newCrossScanAgent(t, "scan-cur")
	seedSchedulableHypothesis(a.scanCtx, "idor", "/orders", 0.9)

	stub := &crossScanStubReader{region: infoitem.RegionResult{
		Items: []infoitem.Item{
			crossScanItem("ii-1", "scan-old-1", infoitem.NatureObserved),
			crossScanItem("ii-2", "scan-old-2", infoitem.NatureObserved),
			crossScanItem("ii-3", "scan-old-1", infoitem.NatureInferred),
			crossScanItem("ii-4", "scan-old-3", infoitem.NatureObserved),
			crossScanItem("ii-5", "scan-old-4", infoitem.NatureObserved),
			// Same-subject item from the CURRENT scan must not count as
			// cross-scan history: the local ledger already covers it.
			crossScanItem("ii-cur", "scan-cur", infoitem.NatureObserved),
		},
		Truncated:  true,
		Incomplete: true,
		GapReason:  "journal gap",
	}}
	cancel := infoitem.RegisterReader(stub)
	defer cancel()

	res, err := a.readLedgerTool(map[string]string{"filter": "schedulable"})
	if err != nil || res.Error != "" {
		t.Fatalf("read_ledger failed: result=%+v err=%v", res, err)
	}

	// Counts (current-scan item excluded), deduped scan ids capped at 3 of 4,
	// the truncation flag and the §6.4 sync-gap flag passed through.
	want := "    ↳ 同端点跨扫描历史: 5 条既往观察 (observed=4 inferred=1), 扫描: scan-old-1, scan-old-2, scan-old-3, 结果已截断, 冷路径同步存在缺口(journal gap)\n"
	if !strings.Contains(res.Output, want) {
		t.Fatalf("cross-scan region note missing or wrong:\noutput:\n%s\nwant note: %s", res.Output, want)
	}
	if strings.Contains(res.Output, "scan-cur") {
		t.Fatalf("current-scan item leaked into cross-scan history:\n%s", res.Output)
	}

	// Cross-scan query shape: the project whitelist (D7 — never AllScans),
	// subject = endpoint, small limit. The stub's Validate already rejects a
	// malformed query; these assertions pin the exact shape.
	if len(stub.regionQueries) != 1 {
		t.Fatalf("expected exactly 1 region query, got %d", len(stub.regionQueries))
	}
	q := stub.regionQueries[0]
	if q.Subject != "/orders" || q.Limit != crossScanRegionLimit || q.AllScans {
		t.Fatalf("unexpected region query shape: %+v", q)
	}
	if strings.Join(q.ScanIDs, ",") != strings.Join(crossScanTestWhitelist, ",") {
		t.Fatalf("region query scan ids = %v, want project whitelist %v", q.ScanIDs, crossScanTestWhitelist)
	}

	// A second schedulable hypothesis on the same endpoint reuses the cached
	// note instead of issuing another region query.
	seedSchedulableHypothesis(a.scanCtx, "sqli", "/orders", 0.8)
	res2, err := a.readLedgerTool(map[string]string{"filter": "schedulable"})
	if err != nil || res2.Error != "" {
		t.Fatalf("second read_ledger failed: result=%+v err=%v", res2, err)
	}
	if strings.Count(res2.Output, crossScanRegionMarker) != 2 {
		t.Fatalf("expected the note on both entries:\n%s", res2.Output)
	}
	// Two calls with two same-endpoint entries in the second call: one query
	// per call proves the per-call endpoint dedup (a broken dedup would issue
	// 3 total).
	if len(stub.regionQueries) != 2 {
		t.Fatalf("expected one region query per call (endpoint dedup within a call), got %d total", len(stub.regionQueries))
	}
}

func TestCrossScanHistoryIgnoresForeignProjectItems(t *testing.T) {
	// The project is the isolation fence. Even if the store returns an item
	// from another project's scan (buggy store, stale index), the consumer
	// must drop it — no count, no scan label, no ID handed to the model.
	a := newCrossScanAgentWithProject(t, "scan-cur", []string{"scan-cur", "scan-old-1"})
	seedSchedulableHypothesis(a.scanCtx, "idor", "/orders", 0.9)

	stub := &crossScanStubReader{region: infoitem.RegionResult{
		Items: []infoitem.Item{
			crossScanItem("ii-own", "scan-old-1", infoitem.NatureObserved),
			crossScanItem("ii-foreign", "scan-foreign-project", infoitem.NatureObserved),
			crossScanItem("ii-cur", "scan-cur", infoitem.NatureObserved),
		},
	}}
	cancel := infoitem.RegisterReader(stub)
	defer cancel()

	res, err := a.readLedgerTool(map[string]string{"filter": "schedulable"})
	if err != nil || res.Error != "" {
		t.Fatalf("read_ledger failed: result=%+v err=%v", res, err)
	}
	want := "同端点跨扫描历史: 1 条既往观察 (observed=1 inferred=0), 扫描: scan-old-1"
	if !strings.Contains(res.Output, want) {
		t.Fatalf("cross-scan note missing or counts foreign items:\n%s\nwant: %s", res.Output, want)
	}
	if strings.Contains(res.Output, "scan-foreign-project") {
		t.Fatalf("foreign project's scan id leaked into the note:\n%s", res.Output)
	}
}

func TestCrossScanHistoryRequiresProjectWhitelist(t *testing.T) {
	// A project-less agent (no resolver) must not fall back to AllScans: the
	// annotation is suppressed and nothing is queried.
	a := newCrossScanAgentNoProject(t, "scan-cur")
	seedSchedulableHypothesis(a.scanCtx, "idor", "/orders", 0.9)

	stub := &crossScanStubReader{region: infoitem.RegionResult{
		Items: []infoitem.Item{crossScanItem("ii-1", "scan-old-1", infoitem.NatureObserved)},
	}}
	cancel := infoitem.RegisterReader(stub)
	defer cancel()

	res, err := a.readLedgerTool(map[string]string{"filter": "schedulable"})
	if err != nil || res.Error != "" {
		t.Fatalf("read_ledger failed: result=%+v err=%v", res, err)
	}
	if strings.Contains(res.Output, crossScanRegionMarker) {
		t.Fatalf("cross-scan note rendered without a project whitelist:\n%s", res.Output)
	}
	if len(stub.regionQueries) != 0 {
		t.Fatalf("project-less agent issued %d cross-scan queries, want 0", len(stub.regionQueries))
	}

	// The claim path must suppress its section the same way.
	claimRes, err := a.claimNextHypothesisTool(map[string]string{})
	if err != nil || claimRes.Error != "" {
		t.Fatalf("claim failed: result=%+v err=%v", claimRes, err)
	}
	if strings.Contains(claimRes.Output, crossScanAssocMarker) {
		t.Fatalf("cross-scan association note rendered without a project whitelist:\n%s", claimRes.Output)
	}
	if len(stub.regionQueries) != 0 {
		t.Fatalf("project-less claim issued %d cross-scan queries, want 0", len(stub.regionQueries))
	}
}

func TestReadLedgerSchedulableRegionNoteDegradesToUnchangedOutput(t *testing.T) {
	seed := func(sc *scanctx.ScanContext) {
		seedSchedulableHypothesis(sc, "idor", "/orders", 0.9)
	}

	// Baseline: no reader registered — the pre-M4 output.
	aNil := newCrossScanAgent(t, "scan-cur")
	seed(aNil.scanCtx)
	base, err := aNil.readLedgerTool(map[string]string{"filter": "schedulable"})
	if err != nil || base.Error != "" {
		t.Fatalf("baseline read_ledger failed: result=%+v err=%v", base, err)
	}
	if strings.Contains(base.Output, crossScanRegionMarker) {
		t.Fatalf("annotation leaked without a reader:\n%s", base.Output)
	}

	// Same input with a registered reader that has no history: identical output.
	aEmpty := newCrossScanAgent(t, "scan-cur")
	seed(aEmpty.scanCtx)
	cancel := infoitem.RegisterReader(&crossScanStubReader{})
	defer cancel()
	gotEmpty, err := aEmpty.readLedgerTool(map[string]string{"filter": "schedulable"})
	if err != nil || gotEmpty.Error != "" {
		t.Fatalf("empty-history read_ledger failed: result=%+v err=%v", gotEmpty, err)
	}
	if gotEmpty.Output != base.Output {
		t.Fatalf("empty history changed the output:\nbaseline: %q\ngot:      %q", base.Output, gotEmpty.Output)
	}

	// A failing reader must not fail the tool, and the failure must be VISIBLE
	// (charter §3.4: cold-path errors are never silently dropped) — a one-line
	// failure note replaces the annotation.
	aErr := newCrossScanAgent(t, "scan-cur")
	seed(aErr.scanCtx)
	cancelErr := infoitem.RegisterReader(&crossScanStubReader{regionErr: errors.New("cold path down")})
	defer cancelErr()
	gotErr, err := aErr.readLedgerTool(map[string]string{"filter": "schedulable"})
	if err != nil || gotErr.Error != "" {
		t.Fatalf("reader error must not fail the tool: result=%+v err=%v", gotErr, err)
	}
	if !strings.Contains(gotErr.Output, "冷路径查询失败，跨扫描历史暂不可用: cold path down") {
		t.Fatalf("reader error must surface a visible note:\n%s", gotErr.Output)
	}
	if strings.Contains(gotErr.Output, crossScanRegionMarker) {
		t.Fatalf("failed lookup must not fabricate a history note:\n%s", gotErr.Output)
	}
}

// The per-call query budget: distinct endpoints beyond crossScanMaxRegionQueries
// get a visible budget note instead of issuing unbounded cold-path reads.
func TestReadLedgerSchedulableRegionQueryBudget(t *testing.T) {
	a := newCrossScanAgent(t, "scan-cur")
	const extra = 3
	for i := 0; i < crossScanMaxRegionQueries+extra; i++ {
		seedSchedulableHypothesis(a.scanCtx, "idor", fmt.Sprintf("/orders/%d", i), 0.9)
	}

	stub := &crossScanStubReader{}
	cancel := infoitem.RegisterReader(stub)
	defer cancel()

	res, err := a.readLedgerTool(map[string]string{"filter": "schedulable"})
	if err != nil || res.Error != "" {
		t.Fatalf("read_ledger failed: result=%+v err=%v", res, err)
	}
	if len(stub.regionQueries) != crossScanMaxRegionQueries {
		t.Fatalf("region queries = %d, want the budget cap %d", len(stub.regionQueries), crossScanMaxRegionQueries)
	}
	wantBudget := fmt.Sprintf(crossScanBudgetNoteFmt, crossScanMaxRegionQueries)
	if strings.Count(res.Output, wantBudget) != 1 {
		t.Fatalf("budget note must appear exactly once:\n%s", res.Output)
	}
}

func TestLedgerClaimCrossScanAssociationNote(t *testing.T) {
	a := newCrossScanAgent(t, "scan-cur")
	seedSchedulableHypothesis(a.scanCtx, "idor", "/orders", 0.9)

	stub := &crossScanStubReader{
		region: infoitem.RegionResult{Items: []infoitem.Item{
			crossScanItem("ii-old-1", "scan-old-1", infoitem.NatureObserved),
			crossScanItem("ii-old-2", "scan-old-2", infoitem.NatureInferred),
			// The current-scan item is the association root: relation edges
			// live per scan, so traversal starts from THIS scan's items.
			crossScanItem("ii-cur", "scan-cur", infoitem.NatureObserved),
		}},
		assoc: infoitem.AssociationResult{
			Hits: []infoitem.AssociationHit{
				{Item: crossScanItem("ii-rel-1", "scan-cur", infoitem.NatureObserved), Path: []string{"ii-cur", "ii-rel-1"}, Depth: 1},
				{Item: crossScanItem("ii-rel-2", "scan-cur", infoitem.NatureObserved), Path: []string{"ii-cur", "ii-rel-2"}, Depth: 2},
			},
			Truncated: true,
		},
	}
	cancel := infoitem.RegisterReader(stub)
	defer cancel()

	res, err := a.claimNextHypothesisTool(map[string]string{})
	if err != nil || res.Error != "" {
		t.Fatalf("claim failed: result=%+v err=%v", res, err)
	}

	// Claim semantics unchanged (scheduling behavior is off-limits for M4).
	if id, _ := res.Metadata["hypothesis_id"].(string); id == "" {
		t.Fatalf("expected a claim, metadata=%+v", res.Metadata)
	}
	if status, _ := res.Metadata["status"].(string); status != string(scanctx.HypothesisTesting) {
		t.Fatalf("claim did not move the hypothesis to testing: %v", res.Metadata["status"])
	}

	// Region step counts with the current-scan item excluded.
	wantRegion := "同主体跨扫描历史: 2 条既往观察 (observed=1 inferred=1), 扫描: scan-old-1, scan-old-2"
	if !strings.Contains(res.Output, wantRegion) {
		t.Fatalf("cross-scan region line missing or wrong:\noutput:\n%s\nwant: %s", res.Output, wantRegion)
	}
	// Association step: reachable count, source scans, truncation passthrough.
	wantAssoc := "当前扫描内关联可达项: 2 个 (沿边 ≤2 跳), 扫描: scan-cur, 结果已截断\n"
	if !strings.Contains(res.Output, wantAssoc) {
		t.Fatalf("association line missing or wrong:\noutput:\n%s\nwant: %s", res.Output, wantAssoc)
	}
	// The section is appended at the very end, after the standing guidance.
	if idx := strings.Index(res.Output, "Work this hypothesis"); idx < 0 ||
		strings.Index(res.Output, crossScanAssocMarker) < idx {
		t.Fatalf("cross-scan section must follow the guidance line:\n%s", res.Output)
	}

	// Query shapes: region is project-whitelisted and subject-filtered;
	// association is rooted at THIS scan's same-subject item IDs only, with
	// bounded depth and budget, unrestricted edge types.
	if len(stub.regionQueries) != 1 {
		t.Fatalf("expected 1 region query, got %d", len(stub.regionQueries))
	}
	rq := stub.regionQueries[0]
	if rq.Subject != "/orders" || rq.Limit != crossScanRegionLimit || rq.AllScans {
		t.Fatalf("unexpected region query shape: %+v", rq)
	}
	if strings.Join(rq.ScanIDs, ",") != strings.Join(crossScanTestWhitelist, ",") {
		t.Fatalf("region query scan ids = %v, want project whitelist %v", rq.ScanIDs, crossScanTestWhitelist)
	}
	if len(stub.assocQueries) != 1 {
		t.Fatalf("expected 1 association query, got %d", len(stub.assocQueries))
	}
	aq := stub.assocQueries[0]
	if aq.ScanID != a.scanCtx.ID {
		t.Fatalf("association query scan id = %q, want %q", aq.ScanID, a.scanCtx.ID)
	}
	if len(aq.StartIDs) != 1 || aq.StartIDs[0] != "ii-cur" {
		t.Fatalf("association start ids must be the CURRENT-scan items only (edges are per-scan): %+v", aq.StartIDs)
	}
	if aq.MaxDepth != crossScanAssocMaxDepth || aq.Limit != crossScanAssocLimit || len(aq.EdgeTypes) != 0 {
		t.Fatalf("unexpected association query shape: %+v", aq)
	}
}

func TestLedgerClaimAssociationNoteDegradesGracefully(t *testing.T) {
	seed := func(sc *scanctx.ScanContext) {
		seedSchedulableHypothesis(sc, "idor", "/orders", 0.9)
	}

	// Baseline: no reader registered — the pre-M4 output.
	aNil := newCrossScanAgent(t, "scan-cur")
	seed(aNil.scanCtx)
	base, err := aNil.claimNextHypothesisTool(map[string]string{})
	if err != nil || base.Error != "" {
		t.Fatalf("baseline claim failed: result=%+v err=%v", base, err)
	}
	if strings.Contains(base.Output, crossScanAssocMarker) {
		t.Fatalf("annotation leaked without a reader:\n%s", base.Output)
	}

	// Same input, reader present but no history at all: identical output, and
	// the association step is skipped entirely (no wasted edge traversal).
	aEmpty := newCrossScanAgent(t, "scan-cur")
	seed(aEmpty.scanCtx)
	emptyStub := &crossScanStubReader{}
	cancel := infoitem.RegisterReader(emptyStub)
	defer cancel()
	gotEmpty, err := aEmpty.claimNextHypothesisTool(map[string]string{})
	if err != nil || gotEmpty.Error != "" {
		t.Fatalf("empty-history claim failed: result=%+v err=%v", gotEmpty, err)
	}
	if gotEmpty.Output != base.Output {
		t.Fatalf("empty history changed the output:\nbaseline: %q\ngot:      %q", base.Output, gotEmpty.Output)
	}
	if len(emptyStub.assocQueries) != 0 {
		t.Fatalf("association must be skipped without any same-subject items: %+v", emptyStub.assocQueries)
	}

	// Prior-scan history exists but nothing is projected for THIS scan yet:
	// the region line renders and the edge traversal is honestly omitted
	// (there are no per-scan edges to walk).
	aNoRoots := newCrossScanAgent(t, "scan-cur")
	seed(aNoRoots.scanCtx)
	cancelNoRoots := infoitem.RegisterReader(&crossScanStubReader{
		region: infoitem.RegionResult{Items: []infoitem.Item{
			crossScanItem("ii-old-1", "scan-old-1", infoitem.NatureObserved),
		}},
	})
	defer cancelNoRoots()
	gotNoRoots, err := aNoRoots.claimNextHypothesisTool(map[string]string{})
	if err != nil || gotNoRoots.Error != "" {
		t.Fatalf("no-roots claim failed: result=%+v err=%v", gotNoRoots, err)
	}
	if !strings.Contains(gotNoRoots.Output, wantClaimRegionLine()) {
		t.Fatalf("region line missing:\n%s", gotNoRoots.Output)
	}
	if strings.Contains(gotNoRoots.Output, crossScanAssocReachMarker) {
		t.Fatalf("association section must be omitted without current-scan roots:\n%s", gotNoRoots.Output)
	}

	// Region step fails: visible failure note, tool must not fail.
	aRegionErr := newCrossScanAgent(t, "scan-cur")
	seed(aRegionErr.scanCtx)
	cancelRegionErr := infoitem.RegisterReader(&crossScanStubReader{regionErr: errors.New("cold path down")})
	defer cancelRegionErr()
	gotRegionErr, err := aRegionErr.claimNextHypothesisTool(map[string]string{})
	if err != nil || gotRegionErr.Error != "" {
		t.Fatalf("region error must not fail the tool: result=%+v err=%v", gotRegionErr, err)
	}
	if !strings.Contains(gotRegionErr.Output, "冷路径查询失败，跨扫描历史暂不可用: cold path down") {
		t.Fatalf("region error must surface a visible note:\n%s", gotRegionErr.Output)
	}

	// Association step fails after a successful region step: the region line
	// stays, the failure gets a visible note, the tool must not fail.
	aAssocErr := newCrossScanAgent(t, "scan-cur")
	seed(aAssocErr.scanCtx)
	cancelAssocErr := infoitem.RegisterReader(&crossScanStubReader{
		region: infoitem.RegionResult{Items: []infoitem.Item{
			crossScanItem("ii-old-1", "scan-old-1", infoitem.NatureObserved),
			crossScanItem("ii-cur", "scan-cur", infoitem.NatureObserved),
		}},
		assocErr: errors.New("edge table unavailable"),
	})
	defer cancelAssocErr()
	gotAssocErr, err := aAssocErr.claimNextHypothesisTool(map[string]string{})
	if err != nil || gotAssocErr.Error != "" {
		t.Fatalf("association error must not fail the tool: result=%+v err=%v", gotAssocErr, err)
	}
	if !strings.Contains(gotAssocErr.Output, wantClaimRegionLine()) {
		t.Fatalf("region line must survive an association failure:\n%s", gotAssocErr.Output)
	}
	if !strings.Contains(gotAssocErr.Output, "当前扫描内关联查询失败: edge table unavailable") {
		t.Fatalf("association error must surface a visible note:\n%s", gotAssocErr.Output)
	}
}

// wantClaimRegionLine is the exact region line a single prior-scan item
// produces in the claim output.
func wantClaimRegionLine() string {
	return "同主体跨扫描历史: 1 条既往观察 (observed=1 inferred=0), 扫描: scan-old-1"
}
