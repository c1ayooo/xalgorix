package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func traceAgent(t *testing.T, name string) *Agent {
	t.Helper()
	a := &Agent{scanCtx: scanctx.New("r3-"+name, t.TempDir())}
	t.Cleanup(a.scanCtx.Close)
	a.recordHypothesisTool(map[string]string{
		"vuln_class": "idor", "endpoint": "/api/orders", "title": "Orders are enumerable across users",
		"preconditions": "a low-privilege session", "baseline": "user B gets 403",
	})
	return a
}

func TestEvidenceInfoItemLinkIsValidatedAndTraced(t *testing.T) {
	a := traceAgent(t, "link")
	h := a.scanCtx.Ledger.All()[0]
	// Invalid anchor is refused (engine-side validation).
	res, err := a.addHypothesisEvidenceTool(map[string]string{
		"hypothesis_id": h.ID, "kind": "probe", "summary": "observed a leak", "info_item_id": "I-404",
	})
	if err != nil || res.Error == "" {
		t.Fatalf("unknown info_item_id must be refused: %+v %v", res, err)
	}
	// Valid anchor: an existing evidence ID in this scan.
	base, err := a.addHypothesisEvidenceTool(map[string]string{
		"hypothesis_id": h.ID, "kind": "baseline", "nature": "observed", "summary": "control 403",
		"request": "GET /api/orders?id=2", "response": "HTTP/1.1 403",
	})
	if err != nil || base.Error != "" {
		t.Fatalf("baseline add failed: %+v %v", base, err)
	}
	cur, _ := a.scanCtx.Ledger.Get(h.ID)
	baseID := cur.Evidence[0].ID
	res, err = a.addHypothesisEvidenceTool(map[string]string{
		"hypothesis_id": h.ID, "kind": "probe", "summary": "order 2 returned under user A",
		"info_item_id": baseID, "intent_id": "work#3",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("valid info link failed: %+v %v", res, err)
	}
	trace := a.scanCtx.Ledger.TraceInfoItem(baseID)
	linked := false
	for _, ev := range trace.Evidence {
		if ev.InfoItemID == baseID {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("trace did not join the linked evidence: %+v", trace)
	}
	if len(trace.IntentIDs) != 1 || trace.IntentIDs[0] != "work#3" {
		t.Fatalf("trace did not join the intent: %+v", trace.IntentIDs)
	}
}

func TestUnsubstantiatedInferenceCannotRefute(t *testing.T) {
	a := &Agent{scanCtx: scanctx.New("r3-infer", t.TempDir())}
	t.Cleanup(a.scanCtx.Close)
	if res, err := a.recordHypothesisTool(map[string]string{
		"vuln_class": "xss", "endpoint": "/search", "title": "Reflected XSS on /search",
		"preconditions": "a rendered page",
	}); err != nil || res.Error != "" {
		t.Fatalf("record failed: %+v %v", res, err)
	}
	h := a.scanCtx.Ledger.All()[0]
	res, err := a.addHypothesisEvidenceTool(map[string]string{
		"hypothesis_id": h.ID, "kind": "probe", "nature": "inferred", "summary": "looks safe",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("inferred observation should be stored: %+v %v", res, err)
	}
	res, err = a.updateHypothesisTool(map[string]string{"hypothesis_id": h.ID, "status": "rejected"})
	if err != nil || res.Error == "" || !strings.Contains(res.Error, "refuting observation") {
		t.Fatalf("unsubstantiated inferred note must not reject: %+v %v", res, err)
	}
}

func TestRepeatedActionIsTraced(t *testing.T) {
	ctx, state := newTestCtxState(t)
	args := map[string]string{
		"tool_name": "http_request", "url": "https://app.test/api/orders?id=1",
		"method": "GET", "output": "HTTP/1.1 403 Forbidden",
	}
	first := hookActionTrace(state, args)
	if first.Nudge != "" {
		t.Fatalf("first action must not nudge: %q", first.Nudge)
	}
	second := hookActionTrace(state, args)
	if !strings.Contains(second.Nudge, "REPEAT ACTION") || !strings.Contains(second.Nudge, "A-1") {
		t.Fatalf("first repeat must cite the prior attempt: %q", second.Nudge)
	}
	attempts := ctx.Ledger.ActionAttempts()
	if len(attempts) != 2 || attempts[1].Count != 2 {
		t.Fatalf("attempts = %+v", attempts)
	}
	// Third repeat is recorded but does not re-nudge (spam guard).
	third := hookActionTrace(state, args)
	if third.Nudge != "" {
		t.Fatalf("subsequent repeats must stay quiet: %q", third.Nudge)
	}
}

func TestDigestPreservesAuthoritativeIDs(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })
	sctx := scanctx.New("r3-digest", t.TempDir())
	t.Cleanup(sctx.Close)
	sctx.SetInstanceID("r3-digest-run")
	h := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "IDOR on /api/orders", VulnClass: "idor", Endpoint: "/api/orders",
		Preconditions: []string{"unknown"}, Origin: "coordinator", Status: scanctx.HypothesisQueued,
	})
	ag := &Agent{state: NewScanState(), scanCtx: sctx}
	ag.state.ScanContextID = sctx.ID
	snap := ag.buildContextSnapshotLabeled("test", nil, 4096, "test")
	if !snap.HasState {
		t.Fatal("expected a state snapshot")
	}
	if !strings.Contains(snap.Text, h.ID) {
		t.Fatalf("digest must keep the authoritative hypothesis ID %s:\n%s", h.ID, snap.Text)
	}
	if strings.Contains(snap.Text, "COVERAGE OPPORTUNITIES") {
		t.Fatal("coverage advisory must not be promoted into the digest")
	}
}

func TestRepeatedActionBudgetIsSurfaced(t *testing.T) {
	_, state := newTestCtxState(t)
	args := map[string]string{
		"tool_name": "http_request", "url": "https://app.test/api/orders?id=1",
		"method": "GET", "output": "HTTP/1.1 403 Forbidden",
	}
	for i := 0; i < repeatActionBudget; i++ {
		res := hookActionTrace(state, args)
		if i+1 == repeatActionBudget {
			if !strings.Contains(res.Nudge, "REPEAT BUDGET EXHAUSTED") {
				t.Fatalf("budget nudge = %q", res.Nudge)
			}
		}
	}
}
