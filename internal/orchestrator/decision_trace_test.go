package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func decisionPlanner(t *testing.T, scanID string) (*Planner, *scanctx.ScanContext, *graph.MemoryStore) {
	t.Helper()
	store := graph.NewMemoryStore()
	sctx := scanctx.New(scanID, t.TempDir())
	t.Cleanup(sctx.Close)
	p := &Planner{cfg: PlannerConfig{ScanID: scanID, Repo: store, ScanCtx: sctx}, ctx: context.Background()}
	return p, sctx, store
}

// TestPlannerRecordsIntentDecisionWithBasis pins P1 item 1-4 for the planner:
// the chosen intent is traceable to the ledger IDs that justified it, with a
// stable candidate snapshot and digest.
func TestPlannerRecordsIntentDecisionWithBasis(t *testing.T) {
	p, sctx, _ := decisionPlanner(t, "p1-intent")
	h := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "SQLi in /login", VulnClass: "sqli", Endpoint: "/login",
		Status: scanctx.HypothesisQueued, Origin: "recon",
	})
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		TaskID:        "task-p1",
		Title:         "Test SQLi on /login",
		Phase:         6,
		VulnClass:     "sqli",
		Endpoint:      "/login",
		Target:        "https://app.example",
		DecisionBasis: plannerStrings{h.ID},
		Notes:         "H-1 claims the login form reflects SQL errors",
	}}})
	if added != 1 {
		t.Fatalf("added = %d", added)
	}
	decisions := sctx.Ledger.Decisions()
	if len(decisions) != 1 {
		t.Fatalf("decisions = %+v", decisions)
	}
	d := decisions[0]
	if d.Action != "create_intent" || d.Selected != "task-p1" || d.IntentID != "task-p1" {
		t.Fatalf("decision = %+v", d)
	}
	if len(d.BasisRefs) != 1 || d.BasisRefs[0] != h.ID || d.InfoUse != "used" || d.NoInfoReason != "" {
		t.Fatalf("basis/info use = %+v", d)
	}
	if len(d.Candidates) != 1 || d.Candidates[0].Rank != 1 || !d.Candidates[0].Selected {
		t.Fatalf("candidates = %+v", d.Candidates)
	}
	if d.SnapshotDigest == "" || len(d.HardConstraints) == 0 {
		t.Fatalf("snapshot = %+v", d)
	}
	trace := sctx.Ledger.TraceInfoItem(h.ID)
	if len(trace.Decisions) != 1 || trace.Decisions[0].ID != d.ID {
		t.Fatalf("hypothesis trace = %+v", trace.Decisions)
	}
}

func TestPlannerRecordsNoBasisExplicitly(t *testing.T) {
	p, sctx, _ := decisionPlanner(t, "p1-nobasis")
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		TaskID: "task-policy", Title: "Refresh the authorized surface map", Phase: 2,
		Target: "https://app.example", TargetKind: "web",
	}}})
	if added != 1 {
		t.Fatalf("added = %d", added)
	}
	d := sctx.Ledger.Decisions()[0]
	if d.InfoUse != "none" || d.NoInfoReason != "no_basis_refs" || len(d.BasisRefs) != 0 {
		t.Fatalf("no-basis decision = %+v", d)
	}
}

func TestPlannerRecordsRetireDecision(t *testing.T) {
	p, sctx, store := decisionPlanner(t, "p1-retire")
	h := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "stale direction", VulnClass: "xss", Endpoint: "/search",
		Status: scanctx.HypothesisBlocked, Origin: "recon",
	})
	if _, err := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: "p1-retire", Kind: graph.KindIntent, UnitKey: "old-task",
		State: graph.StateOpen, Payload: `{"task_id":"old-task","title":"old direction"}`,
	}); err != nil {
		t.Fatal(err)
	}
	_, retired := p.apply(plannerOutput{
		Retire:        []string{"old-task"},
		RetireReasons: map[string]string{"old-task": "superseded by a stronger route"},
		RetireBasis:   map[string][]string{"old-task": {h.ID}},
	})
	if retired != 1 {
		t.Fatalf("retired = %d", retired)
	}
	d := sctx.Ledger.Decisions()[0]
	if d.Action != "retire_intent" || d.IntentID != "old-task" || d.Selected != "old-task" ||
		d.Reason != "superseded by a stronger route" || len(d.BasisRefs) != 1 || d.InfoUse != "used" {
		t.Fatalf("retire decision = %+v", d)
	}
}

func TestDecisionSnapshotDigestIsStable(t *testing.T) {
	candidates := []domainjournal.DecisionCandidate{
		{ID: "a", Rank: 1}, {ID: "b", Rank: 2, Selected: true},
	}
	constraints := []string{"scope=pass", "dedupe=pass"}
	first := decisionSnapshotDigest(candidates, constraints, "b")
	second := decisionSnapshotDigest(candidates, constraints, "b")
	if first == "" || first != second {
		t.Fatalf("digest not stable: %q vs %q", first, second)
	}
	changed := decisionSnapshotDigest(candidates, constraints, "a")
	if changed == first {
		t.Fatal("different selection must change the digest")
	}
}

func TestPlannerReadInfoItemIsTraced(t *testing.T) {
	p, sctx, _ := decisionPlanner(t, "p1-read")
	h := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "XSS in /search", VulnClass: "xss", Endpoint: "/search",
		Status: scanctx.HypothesisQueued, Origin: "recon", NextAction: "reflect a nonce",
	})
	if out := p.readInfoItem(h.ID); !strings.Contains(out, h.ID) {
		t.Fatalf("read output = %q", out)
	}
	reads := sctx.Ledger.InfoReads()
	if len(reads) != 1 || reads[0].Tool != "read_info_item" || reads[0].Mode != "belief" ||
		!reads[0].Found || reads[0].ResultCount != 1 || !strings.Contains(reads[0].Query, "planner") {
		t.Fatalf("planner read trace = %+v", reads)
	}
	trace := sctx.Ledger.TraceInfoItem(h.ID)
	if len(trace.Reads) != 1 || trace.Reads[0].Tool != "read_info_item" {
		t.Fatalf("trace reads = %+v", trace.Reads)
	}
}

func TestPlannerReadMarksTruncatedDetail(t *testing.T) {
	p, sctx, _ := decisionPlanner(t, "p1-truncated")
	h := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "SSTI in /render", VulnClass: "ssti", Endpoint: "/render",
		Status: scanctx.HypothesisQueued, Origin: "recon",
	})
	if !sctx.Ledger.AddEvidence(h.ID, scanctx.Evidence{
		Kind: "probe", Nature: scanctx.EvidenceObserved,
		Summary: strings.Repeat("a", 1300),
		Request: "GET /render?t=1", Response: "HTTP/1.1 200 OK",
	}) {
		t.Fatal("add evidence failed")
	}
	got, _ := sctx.Ledger.Get(h.ID)
	out := p.readInfoItem(got.Evidence[0].ID)
	if !strings.Contains(out, "truncated: missing detail is unknown, not absent") {
		t.Fatalf("truncation marker missing:\n%s", out)
	}
}

func TestPlannerDecisionCandidateUsesGeneratedTaskID(t *testing.T) {
	p, sctx, _ := decisionPlanner(t, "p1-generated-id")
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{{
		Title: "Probe /health", Phase: 2, Target: "https://app.example", TargetKind: "web",
	}}})
	if added != 1 {
		t.Fatalf("added = %d", added)
	}
	d := sctx.Ledger.Decisions()[0]
	if len(d.Candidates) != 1 || d.Candidates[0].ID != d.Selected || d.Selected == "" {
		t.Fatalf("candidate/selected mismatch = %+v", d)
	}
}

func TestShadowVoiOrderIsDeterministic(t *testing.T) {
	sctx := scanctx.New("p4-shadow", t.TempDir())
	t.Cleanup(sctx.Close)
	h := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "XSS in /b", VulnClass: "xss", Endpoint: "/b", Target: "https://b.example",
		Status: scanctx.HypothesisQueued, Origin: "recon", VOI: 0.9, VOIEstimated: true,
	})
	if h.ID == "" {
		t.Fatal("hypothesis missing")
	}
	intents := []plannerIntent{
		{TaskID: "a", Title: "phase-first", Phase: 2, Target: "https://a.example", VulnClass: "sqli"},
		{TaskID: "b", Title: "voi-first", Phase: 8, Target: "https://b.example", Endpoint: "/b", VulnClass: "xss"},
	}
	first := shadowVoiOrder(intents, sctx.Ledger.All())
	second := shadowVoiOrder(intents, sctx.Ledger.All())
	if len(first) != 2 || first[0] != 1 || first[1] != 0 {
		t.Fatalf("shadow order = %+v, want [1 0]", first)
	}
	if first[0] != second[0] || first[1] != second[1] {
		t.Fatalf("shadow order is not stable: %+v vs %+v", first, second)
	}
}

func TestPlannerRecordsShadowWithoutChangingPlan(t *testing.T) {
	p, sctx, store := decisionPlanner(t, "p4-shadow-plan")
	sctx.Ledger.SetVOIWeight(0.3)
	sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title: "XSS in /b", VulnClass: "xss", Endpoint: "/b", Target: "https://b.example",
		Status: scanctx.HypothesisQueued, Origin: "recon", VOI: 0.9, VOIEstimated: true,
	})
	added, _ := p.apply(plannerOutput{Intents: []plannerIntent{
		{TaskID: "a", Title: "first in model order", Phase: 2, Target: "https://a.example", VulnClass: "sqli"},
		{TaskID: "b", Title: "high voi", Phase: 8, Target: "https://b.example", Endpoint: "/b", VulnClass: "xss"},
	}})
	if added != 2 {
		t.Fatalf("added = %d", added)
	}
	decisions := sctx.Ledger.Decisions()
	if len(decisions) != 2 {
		t.Fatalf("decisions = %+v", decisions)
	}
	byIntent := map[string]domainjournal.Decision{}
	for _, d := range decisions {
		byIntent[d.IntentID] = d
		if d.ShadowPolicy != "voi-first" || len(d.ShadowOrder) != 2 {
			t.Fatalf("shadow fields = %+v", d)
		}
	}
	if !byIntent["a"].ShadowChanged || byIntent["b"].ShadowChanged {
		t.Fatalf("shadow changed flags = a:%v b:%v", byIntent["a"].ShadowChanged, byIntent["b"].ShadowChanged)
	}
	if !strings.Contains(byIntent["a"].ShadowReason, "b") {
		t.Fatalf("shadow reason = %q", byIntent["a"].ShadowReason)
	}
	// Shadow is audit-only: both intents still exist with their actual priorities.
	tasks, _ := store.PlanTasks(context.Background(), "p4-shadow-plan")
	if len(tasks) != 2 {
		t.Fatalf("plan tasks = %+v", tasks)
	}
}
