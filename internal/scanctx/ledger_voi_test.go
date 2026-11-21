package scanctx

import (
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/domainjournal"
)

func TestVOIWeightIsBounded(t *testing.T) {
	ls := NewLedgerStore()
	ls.SetVOIWeight(-1)
	if got := ls.VOIWeight(); got != 0 {
		t.Fatalf("negative weight = %v", got)
	}
	ls.SetVOIWeight(0.9)
	if got := ls.VOIWeight(); got != MaxVOIWeight {
		t.Fatalf("clamped weight = %v, want %v", got, MaxVOIWeight)
	}
}

func TestVOIWeightZeroPreservesConfidenceOrdering(t *testing.T) {
	ls := NewLedgerStore()
	low := ls.Upsert(Hypothesis{VulnClass: "xss", Endpoint: "/low", Confidence: 0.4})
	high := ls.Upsert(Hypothesis{VulnClass: "sqli", Endpoint: "/high", Confidence: 0.9})
	ls.SetVOIWeight(0)
	got := ls.Schedulable(10)
	if len(got) != 2 || got[0].ID != high.ID || got[1].ID != low.ID {
		t.Fatalf("w=0 ordering = %+v", got)
	}
	claimed, ok, err := ls.ClaimNextAs("", "agent", AuditActorForAgent("agent"))
	if err != nil || !ok || claimed.ID != high.ID {
		t.Fatalf("w=0 claim = %+v ok=%v err=%v", claimed, ok, err)
	}
	_ = low
}

func TestVOIWeightedOrderingAndClaimAgree(t *testing.T) {
	ls := NewLedgerStore()
	lowConfHighVOI := ls.Upsert(Hypothesis{
		VulnClass: "idor", Endpoint: "/a", Confidence: 0.5, VOI: 1, VOIEstimated: true,
	})
	highConfLowVOI := ls.Upsert(Hypothesis{
		VulnClass: "sqli", Endpoint: "/b", Confidence: 0.9, VOI: 0, VOIEstimated: true,
	})
	ls.SetVOIWeight(0.30)
	// 0.7*0.5 + 0.3*1 = 0.65 > 0.7*0.9 = 0.63.
	first := ls.Schedulable(10)
	second := ls.Schedulable(10)
	if len(first) != 2 || first[0].ID != lowConfHighVOI.ID {
		t.Fatalf("weighted ordering = %+v", first)
	}
	if first[0].ID != second[0].ID || first[1].ID != second[1].ID {
		t.Fatalf("ordering is not deterministic: %+v vs %+v", first, second)
	}
	claimed, ok, err := ls.ClaimNextAs("", "agent", AuditActorForAgent("agent"))
	if err != nil || !ok || claimed.ID != lowConfHighVOI.ID {
		t.Fatalf("weighted claim = %+v ok=%v err=%v", claimed, ok, err)
	}
	_ = highConfLowVOI
}

func TestMissingVOIContributesZero(t *testing.T) {
	ls := NewLedgerStore()
	missing := ls.Upsert(Hypothesis{VulnClass: "xss", Endpoint: "/m", Confidence: 0.8})
	estimated := ls.Upsert(Hypothesis{VulnClass: "sqli", Endpoint: "/e", Confidence: 0.7, VOI: 0.1, VOIEstimated: true})
	ls.SetVOIWeight(0.30)
	top := ls.Schedulable(1)
	if len(top) != 1 || top[0].ID != missing.ID {
		t.Fatalf("missing VOI should contribute zero; got %+v (estimated item %s)", top, estimated.ID)
	}
}

func TestSetVOIAsAuditsChange(t *testing.T) {
	dir := t.TempDir()
	sc := New("voi-audit", dir)
	defer sc.Close()
	h, err := sc.Ledger.UpsertAs(Hypothesis{VulnClass: "xss", Endpoint: "/x"}, AuditActorForAgent("coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Ledger.SetVOIAs(h.ID, 0.7, true, domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent-1"}); err != nil {
		t.Fatal(err)
	}
	got, _ := sc.Ledger.Get(h.ID)
	if !got.VOIEstimated || got.VOI != 0.7 {
		t.Fatalf("VOI not stored: %+v", got)
	}
	if sc.Ledger.JournalLastSeq() < 2 {
		t.Fatalf("VOI change was not audited; seq=%d", sc.Ledger.JournalLastSeq())
	}
}
