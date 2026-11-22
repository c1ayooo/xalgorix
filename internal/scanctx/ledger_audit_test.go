package scanctx

import (
	"path/filepath"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

func TestLedgerDomainAuditRecordsClaimsAndInfoItems(t *testing.T) {
	dir := t.TempDir()
	sc := New("scan-audit", dir)
	t.Cleanup(sc.Close)

	h, err := sc.Ledger.UpsertAs(Hypothesis{
		VulnClass:     "sqli",
		Endpoint:      "/search",
		Parameter:     "q",
		Preconditions: []string{"endpoint reachable"},
	}, AuditActorForAgent("coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Ledger.AddEvidenceAs(h.ID, Evidence{
		Kind:     "probe",
		Nature:   EvidenceObserved,
		Summary:  "baseline response",
		Request:  "GET /search?q=control",
		Response: "200 OK",
		AgentID:  "coordinator",
	}, AuditActorForAgent("coordinator")); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := sc.Ledger.ClaimNextAs("sqli", "specialist-1", AuditActorForAgent("specialist-1"))
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if claimed.AssignedTo != "specialist-1" || claimed.Status != HypothesisTesting {
		t.Fatalf("unexpected claim: %+v", claimed)
	}
	if err := sc.Ledger.SetStatusAs(h.ID, HypothesisProven, "report", AuditActorForAgent("specialist-1")); err != nil {
		t.Fatal(err)
	}

	records, truncated, err := domainjournal.ReadRecords(filepath.Join(dir, domainjournal.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("journal should not have a truncated tail")
	}
	if len(records) != 4 {
		t.Fatalf("records = %d, want 4", len(records))
	}
	projection := domainjournal.Project("scan-audit", records)
	if projection.Status != "complete" || projection.Incomplete {
		t.Fatalf("projection incomplete: %+v", projection)
	}
	if len(projection.Claims) != 1 {
		t.Fatalf("claims = %+v", projection.Claims)
	}
	if projection.Claims[0].Actor.ID != "specialist-1" || projection.Claims[0].Actor.Kind != domainjournal.ActorAgent {
		t.Fatalf("claim actor = %+v", projection.Claims[0].Actor)
	}
	if projection.HypothesisFinal[h.ID].Status != string(HypothesisProven) {
		t.Fatalf("final = %+v", projection.HypothesisFinal[h.ID])
	}

	var itemSeen bool
	for _, rec := range records {
		if rec.Operation != domainjournal.OpAddEvidence {
			continue
		}
		if rec.InfoItem == nil {
			t.Fatal("add_evidence audit record has no InfoItem projection")
		}
		itemSeen = true
		if rec.InfoItem.Nature != "observed" {
			t.Fatalf("InfoItem nature = %q", rec.InfoItem.Nature)
		}
		if rec.InfoItem.Coordinates.Subject.Identifier != "/search" {
			t.Fatalf("InfoItem subject = %+v", rec.InfoItem.Coordinates.Subject)
		}
		if rec.InfoItem.SourceSeq != rec.Seq {
			t.Fatalf("InfoItem seq = %d, record seq = %d", rec.InfoItem.SourceSeq, rec.Seq)
		}
	}
	if !itemSeen {
		t.Fatal("no evidence InfoItem projection found")
	}
}

func TestLedgerJournalResumesSequence(t *testing.T) {
	dir := t.TempDir()
	first := New("scan-resume", dir)
	if _, err := first.Ledger.UpsertAs(Hypothesis{VulnClass: "xss", Endpoint: "/x"}, AuditActorForAgent("coordinator")); err != nil {
		t.Fatal(err)
	}
	first.Close()

	second := New("scan-resume", dir)
	t.Cleanup(second.Close)
	if got := second.Ledger.JournalLastSeq(); got != 1 {
		t.Fatalf("resumed journal seq = %d, want 1", got)
	}
	if _, err := second.Ledger.UpsertAs(Hypothesis{VulnClass: "idor", Endpoint: "/o"}, AuditActorForAgent("coordinator")); err != nil {
		t.Fatal(err)
	}
	if got := second.Ledger.JournalLastSeq(); got != 2 {
		t.Fatalf("next journal seq = %d, want 2", got)
	}
}
