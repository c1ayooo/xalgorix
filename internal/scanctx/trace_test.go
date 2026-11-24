package scanctx

import (
	"strings"
	"testing"
)

func traceStore(t *testing.T) *LedgerStore {
	t.Helper()
	sc := New("trace-"+t.Name(), t.TempDir())
	t.Cleanup(sc.Close)
	return sc.Ledger
}

func TestInfoReadProducesTraceAndDoesNotChangeState(t *testing.T) {
	l := traceStore(t)
	h := l.Upsert(Hypothesis{
		Title: "IDOR on /api/orders", VulnClass: "idor", Endpoint: "/api/orders",
		Preconditions: []string{"unknown"}, Origin: "coordinator", Status: HypothesisQueued,
	})
	ev := l.UpsertEvidenceForTest(t, h.ID, Evidence{Kind: "baseline", Nature: EvidenceObserved, Summary: "control", InfoItemID: "I-9", IntentID: "work#1"})

	if err := l.AppendInfoRead(InfoRead{
		Tool: "read_info_item", Mode: "detail", Reason: "check baseline",
		RefIDs: []string{"I-9"}, Actor: "agent_x", Found: true, Truncated: true,
	}, AuditActorForAgent("agent_x")); err != nil {
		t.Fatal(err)
	}
	trace := l.TraceInfoItem("I-9")
	if len(trace.Reads) != 1 || !trace.Reads[0].Truncated || trace.Reads[0].Actor != "agent_x" {
		t.Fatalf("trace reads = %+v", trace.Reads)
	}
	if len(trace.Evidence) != 1 || trace.Evidence[0].ID != ev.ID {
		t.Fatalf("trace evidence = %+v", trace.Evidence)
	}
	if len(trace.Hypotheses) != 1 || trace.Hypotheses[0] != h.ID {
		t.Fatalf("trace hypotheses = %+v", trace.Hypotheses)
	}
	if len(trace.IntentIDs) != 1 || trace.IntentIDs[0] != "work#1" {
		t.Fatalf("trace intents = %+v", trace.IntentIDs)
	}
	// Audit-only: the read did not touch the hypothesis status/confidence.
	stored, _ := l.Get(h.ID)
	if stored.Status != HypothesisQueued || stored.Confidence != h.Confidence {
		t.Fatalf("read mutated state: %+v", stored)
	}
	if !strings.EqualFold(stored.Evidence[0].InfoItemID, "I-9") {
		t.Fatalf("evidence info link lost: %+v", stored.Evidence[0])
	}
}

func TestActionAttemptsTraceRepeats(t *testing.T) {
	l := traceStore(t)
	att, prior, err := l.RecordActionAttempt("http|GET|a.test/api?query=id", "http_request", "GET /api?id=1 → 403", nil, AuditActorForAgent("agent_x"))
	if err != nil || prior != nil || att.ID != "A-1" {
		t.Fatalf("first attempt = %+v prior=%v err=%v", att, prior, err)
	}
	att2, prior2, err := l.RecordActionAttempt("http|GET|a.test/api?query=id", "http_request", "GET /api?id=2 → 403", nil, AuditActorForAgent("agent_x"))
	if err != nil || prior2 == nil || prior2.ID != "A-1" || att2.Count != 2 {
		t.Fatalf("second attempt = %+v prior=%+v err=%v", att2, prior2, err)
	}
	if got := l.ActionAttempts(); len(got) != 2 {
		t.Fatalf("attempt projection = %d, want 2", len(got))
	}
}

func (ls *LedgerStore) UpsertEvidenceForTest(t *testing.T, hypID string, ev Evidence) Evidence {
	t.Helper()
	if !ls.AddEvidence(hypID, ev) {
		t.Fatalf("add evidence failed for %s", hypID)
	}
	h, ok := ls.Get(hypID)
	if !ok || len(h.Evidence) == 0 {
		t.Fatalf("evidence not stored for %s", hypID)
	}
	return h.Evidence[len(h.Evidence)-1]
}
