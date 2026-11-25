package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

func infoTraceAgent(t *testing.T, id string) (*Agent, *scanctx.ScanContext) {
	t.Helper()
	sctx := scanctx.New(id, t.TempDir())
	t.Cleanup(sctx.Close)
	return &Agent{scanCtx: sctx, state: &ScanState{ScanContextID: sctx.ID}}, sctx
}

// TestRecordInfoSearchPartialIsNotCompleteEmpty pins P1 acceptance 4: a
// truncated/incomplete read is recorded as partial, not as a complete empty
// result the planner may act on.
func TestRecordInfoSearchPartialIsNotCompleteEmpty(t *testing.T) {
	a, sctx := infoTraceAgent(t, "info-partial")
	a.recordInfoSearch("region", "graph", map[string]string{
		"mode": "region", "subject": "/api/orders", "limit": "10",
	}, tools.Result{
		Output:   "InfoSpace activation mode=region results=0 truncated=true incomplete=true",
		Metadata: map[string]any{"count": 0, "truncated": true, "incomplete": true},
	})
	reads := sctx.Ledger.InfoReads()
	if len(reads) != 1 {
		t.Fatalf("reads = %+v", reads)
	}
	r := reads[0]
	if r.Found || !r.Incomplete || !r.Truncated || r.ResultCount != 0 {
		t.Fatalf("partial read flags = %+v", r)
	}
	if !strings.Contains(r.Query, "region") || !strings.Contains(r.Query, "/api/orders") {
		t.Fatalf("query = %q", r.Query)
	}
	if !strings.Contains(r.Reason, "partial") {
		t.Fatalf("reason = %q", r.Reason)
	}
}

func TestInfoResultWarnsOnPartialView(t *testing.T) {
	out := infoResult("region", nil, true, true, "reader timed out", nil).Output
	if !strings.Contains(out, "PARTIAL RESULT") || !strings.Contains(out, "not treat it as proof") {
		t.Fatalf("missing partial warning:\n%s", out)
	}
	out = infoSpaceResult("region", nil, false, true, "gap", nil, nil).Output
	if !strings.Contains(out, "PARTIAL RESULT") {
		t.Fatalf("graph result missing partial warning:\n%s", out)
	}
}

func TestReadInfoItemTraceIncludesDecisions(t *testing.T) {
	a, sctx := infoTraceAgent(t, "info-trace")
	h := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "IDOR in /orders", VulnClass: "idor", Endpoint: "/orders",
		Status: scanctx.HypothesisQueued, Origin: "recon",
	})
	if !sctx.Ledger.AddEvidence(h.ID, scanctx.Evidence{
		Kind: "probe", Summary: "order id 2 returns order 1", Nature: scanctx.EvidenceObserved,
		InfoItemID: "ii-42", IntentID: "T-9",
	}) {
		t.Fatal("add evidence failed")
	}
	got, _ := sctx.Ledger.Get(h.ID)
	evID := got.Evidence[0].ID
	if _, err := sctx.Ledger.AppendDecision(scanctx.Decision{
		Action: "create_intent", Actor: "planner", IntentID: "T-10",
		BasisRefs: []string{evID}, Selected: "T-10",
		Reason:  "the observed cross-order read justifies an IDOR test",
		InfoUse: "used",
	}, scanctx.AuditActorForAgent("planner")); err != nil {
		t.Fatal(err)
	}

	res, err := a.readInfoItemTool(map[string]string{"id": "ii-42", "layer": "trace"})
	if err != nil || res.Error != "" {
		t.Fatalf("trace read = %+v err=%v", res, err)
	}
	for _, want := range []string{`"decisions"`, "create_intent", "T-10", "ii-42"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("trace output missing %q:\n%s", want, res.Output)
		}
	}
	reads := sctx.Ledger.InfoReads()
	if len(reads) != 1 || reads[0].Mode != "trace" || !strings.Contains(reads[0].Query, "ii-42") {
		t.Fatalf("trace read record = %+v", reads)
	}
}

// TestSearchInfoDisabledIsIncompleteNotEmpty pins the defer wiring: a disabled
// reader is recorded as an unavailable partial view, never as a complete empty
// result.
func TestSearchInfoDisabledIsIncompleteNotEmpty(t *testing.T) {
	a, sctx := infoTraceAgent(t, "info-disabled")
	prev := graph.Backend()
	graph.SetBackend(nil)
	t.Cleanup(func() { graph.SetBackend(prev) })
	res, err := a.searchInfoTool(map[string]string{"mode": "region", "subject": "/api"})
	if err != nil || res.Error != "" {
		t.Fatalf("search = %+v err=%v", res, err)
	}
	reads := sctx.Ledger.InfoReads()
	if len(reads) != 1 || !reads[0].Incomplete || reads[0].Found ||
		!strings.Contains(reads[0].Reason, "unavailable") {
		t.Fatalf("disabled search trace = %+v", reads)
	}
}

func graphBackendForTest() graph.Repository          { return graph.Backend() }
func graphBackendForTestReset(prev graph.Repository) { graph.SetBackend(prev) }
