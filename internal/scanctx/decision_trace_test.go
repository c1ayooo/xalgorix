package scanctx

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

func traceContext(t *testing.T, id string) *ScanContext {
	t.Helper()
	sctx := New(id, t.TempDir())
	t.Cleanup(sctx.Close)
	return sctx
}

// TestInfoTraceJoinsReadEvidenceDecision pins P1's ID join: one InfoItem read
// leads to the Evidence derived from it, the Hypothesis holding that Evidence,
// the Intent that produced it, and the Decision that cited the Evidence.
func TestInfoTraceJoinsReadEvidenceDecision(t *testing.T) {
	sctx := traceContext(t, "scan-trace")
	ls := sctx.Ledger
	h := ls.Upsert(Hypothesis{
		Title: "SQLi in /login", VulnClass: "sqli", Endpoint: "/login",
		Status: HypothesisQueued, Origin: "recon",
	})
	if !ls.AddEvidence(h.ID, Evidence{
		Kind: "probe", Summary: "quote reflects an error",
		Nature: EvidenceObserved, InfoItemID: "ii-1", IntentID: "T-1",
	}) {
		t.Fatal("add evidence failed")
	}
	evID := ""
	if got, ok := ls.Get(h.ID); ok && len(got.Evidence) == 1 {
		evID = got.Evidence[0].ID
	}
	if evID == "" {
		t.Fatal("evidence id missing")
	}
	if err := ls.AppendInfoRead(InfoRead{
		Tool: "read_info_item", Mode: "detail", Query: `{"id":"ii-1"}`,
		RefIDs: []string{"ii-1"}, Actor: "test", ResultCount: 1, Found: true,
	}, AuditActorForAgent("test")); err != nil {
		t.Fatalf("append read: %v", err)
	}
	decision, err := ls.AppendDecision(Decision{
		Action: "create_intent", Actor: "planner", IntentID: "T-2",
		BasisRefs: []string{evID},
		Candidates: []DecisionCandidate{
			{ID: "T-1", Kind: "intent", Rank: 1},
			{ID: "T-2", Kind: "intent", Rank: 2, Selected: true},
		},
		HardConstraints: []string{"scope=pass"},
		Selected:        "T-2",
		Reason:          "the observed evidence makes this worth testing",
		InfoUse:         "used",
		SnapshotDigest:  "digest",
	}, AuditActorForAgent("planner"))
	if err != nil {
		t.Fatalf("append decision: %v", err)
	}
	if decision.ID == "" {
		t.Fatal("decision id was not assigned")
	}

	trace := ls.TraceInfoItem("ii-1")
	if len(trace.Reads) != 1 || trace.Reads[0].Query != `{"id":"ii-1"}` || trace.Reads[0].ResultCount != 1 {
		t.Fatalf("reads = %+v", trace.Reads)
	}
	if len(trace.Evidence) != 1 || trace.Evidence[0].ID != evID {
		t.Fatalf("evidence = %+v", trace.Evidence)
	}
	if len(trace.Hypotheses) != 1 || trace.Hypotheses[0] != h.ID {
		t.Fatalf("hypotheses = %+v", trace.Hypotheses)
	}
	if len(trace.IntentIDs) != 1 || trace.IntentIDs[0] != "T-1" {
		t.Fatalf("intent ids = %+v", trace.IntentIDs)
	}
	if len(trace.Decisions) != 1 || trace.Decisions[0].ID != decision.ID ||
		trace.Decisions[0].Selected != "T-2" || trace.Decisions[0].InfoUse != "used" {
		t.Fatalf("decisions = %+v", trace.Decisions)
	}

	// Tracing by the Hypothesis ID reaches the same decision through basis.
	byHyp := ls.TraceInfoItem(h.ID)
	if len(byHyp.Decisions) != 1 || byHyp.Decisions[0].ID != decision.ID {
		t.Fatalf("hypothesis trace decisions = %+v", byHyp.Decisions)
	}
}

func TestAppendDecisionRequiresActionAndJournal(t *testing.T) {
	sctx := traceContext(t, "scan-trace-invalid")
	if _, err := sctx.Ledger.AppendDecision(Decision{Actor: "planner"}, AuditActorForAgent("planner")); err == nil {
		t.Fatal("empty action must be rejected")
	}
	bare := NewLedgerStore()
	if _, err := bare.AppendDecision(Decision{Action: "create_intent"}, domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "x", Source: "test"}); err == nil ||
		!strings.Contains(err.Error(), "no journal") {
		t.Fatalf("journal-less decision error = %v", err)
	}
}

func TestInfoReadSummaryCarriesQueryAndCount(t *testing.T) {
	sctx := traceContext(t, "scan-read-summary")
	ls := sctx.Ledger
	if err := ls.AppendInfoRead(InfoRead{
		Tool: "search_info", Mode: "region", Query: `{"mode":"region","limit":"5"}`,
		RefIDs: []string{"ii-9"}, Actor: "test", ResultCount: 0,
		Found: false, Incomplete: true, Reason: "search returned a partial result",
	}, AuditActorForAgent("test")); err != nil {
		t.Fatalf("append: %v", err)
	}
	reads := ls.InfoReads()
	if len(reads) != 1 || !reads[0].Incomplete || reads[0].ResultCount != 0 ||
		!strings.Contains(reads[0].Query, "region") {
		t.Fatalf("reads = %+v", reads)
	}
}
