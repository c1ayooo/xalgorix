package scanctx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

func TestEvidenceNatureDefaultsToInferred(t *testing.T) {
	ls := NewLedgerStore()
	h := ls.Upsert(Hypothesis{VulnClass: "xss", Endpoint: "/search"})
	if !ls.AddEvidence(h.ID, Evidence{Kind: "probe", Summary: "reflection seen"}) {
		t.Fatal("AddEvidence returned false")
	}
	got, _ := ls.Get(h.ID)
	if len(got.Evidence) != 1 {
		t.Fatalf("evidence count = %d", len(got.Evidence))
	}
	if got.Evidence[0].Nature != EvidenceInferred {
		t.Fatalf("default nature = %q, want inferred", got.Evidence[0].Nature)
	}
}

func TestEvidenceEngineObservationAndStableInfoItemID(t *testing.T) {
	ls := NewLedgerStore()
	ls.SetScanID("scan-42")
	h := ls.Upsert(Hypothesis{VulnClass: "sqli", Endpoint: "/q"})
	ls.AddEvidence(h.ID, Evidence{
		Kind:     "exploit",
		Nature:   EvidenceObserved,
		Source:   "verify_sqli",
		Summary:  "DB error reproduced",
		Request:  "GET /q?id=1'",
		Response: "SQL syntax error",
	})
	got, _ := ls.Get(h.ID)
	ev := got.Evidence[0]
	if ev.Nature != EvidenceObserved {
		t.Fatalf("nature = %q", ev.Nature)
	}
	if ev.InfoItemID != "evidence:scan-42:"+ev.ID {
		t.Fatalf("info item id = %q", ev.InfoItemID)
	}
}

func TestEvidenceSupportsMustReferenceExistingEvidence(t *testing.T) {
	ls := NewLedgerStore()
	h := ls.Upsert(Hypothesis{VulnClass: "idor", Endpoint: "/orders"})
	ls.AddEvidence(h.ID, Evidence{Kind: "baseline", Nature: EvidenceObserved, Summary: "anonymous got 401"})
	base, _ := ls.Get(h.ID)
	baseID := base.Evidence[0].ID

	ls.AddEvidence(h.ID, Evidence{
		Kind:     "probe",
		Nature:   EvidenceInferred,
		Summary:  "the parameter is likely an object identifier",
		Supports: []string{baseID, "EV-does-not-exist"},
	})
	got, _ := ls.Get(h.ID)
	inf := got.Evidence[1]
	if len(inf.Supports) != 1 || inf.Supports[0] != baseID {
		t.Fatalf("supports = %v, want only %s", inf.Supports, baseID)
	}
}

func TestLedgerLoadDefaultsLegacyEvidenceToInferred(t *testing.T) {
	dir := t.TempDir()
	snapshot := map[string]any{
		"seq":    1,
		"ev_seq": 1,
		"order":  []string{"H-1"},
		"hypotheses": []map[string]any{{
			"id":         "H-1",
			"dedup_key":  "xss|/x||",
			"vuln_class": "xss",
			"status":     "queued",
			"evidence": []map[string]any{{
				"id":      "EV-1",
				"kind":    "probe",
				"summary": "legacy evidence",
			}},
		}},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ledger.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	ls := NewLedgerStore()
	ls.SetPersistPath(dir)
	if n := ls.LoadFromDisk(); n != 1 {
		t.Fatalf("loaded = %d", n)
	}
	got, _ := ls.Get("H-1")
	if len(got.Evidence) != 1 || got.Evidence[0].Nature != EvidenceInferred {
		t.Fatalf("legacy evidence nature = %+v, want inferred", got.Evidence)
	}
}

func TestLedgerMergeRemapsSupports(t *testing.T) {
	src := NewLedgerStore()
	h := src.Upsert(Hypothesis{VulnClass: "ssrf", Endpoint: "/fetch"})
	src.AddEvidence(h.ID, Evidence{Kind: "baseline", Nature: EvidenceObserved, Summary: "control"})
	base, _ := src.Get(h.ID)
	src.AddEvidence(h.ID, Evidence{
		Kind:     "probe",
		Nature:   EvidenceInferred,
		Summary:  "server fetches metadata",
		Supports: []string{base.Evidence[0].ID},
	})

	dst := NewLedgerStore()
	if n := dst.Merge(src); n != 1 {
		t.Fatalf("merged = %d", n)
	}
	merged, _ := dst.Get("H-1")
	if len(merged.Evidence) != 2 {
		t.Fatalf("evidence = %d", len(merged.Evidence))
	}
	inf := merged.Evidence[1]
	if len(inf.Supports) != 1 || inf.Supports[0] != merged.Evidence[0].ID {
		t.Fatalf("supports not remapped: %v (base %s)", inf.Supports, merged.Evidence[0].ID)
	}
}

func TestMergeAuditProjectsChildEvidence(t *testing.T) {
	parent := New("merge-audit", t.TempDir())
	defer parent.Close()
	child := NewLedgerStore()
	h := child.Upsert(Hypothesis{VulnClass: "sqli", Endpoint: "/q"})
	child.AddEvidence(h.ID, Evidence{
		Kind: "exploit", Nature: EvidenceObserved, Source: "verify_sqli",
		Summary: "db error", Request: "GET /q?id=1'", Response: "syntax error",
	})
	if n := parent.Ledger.Merge(child); n != 1 {
		t.Fatalf("merged = %d", n)
	}
	records, _, err := domainjournal.ReadRecords(filepath.Join(parent.ScanDir, domainjournal.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 {
		t.Fatal("no merge audit record")
	}
	last := records[len(records)-1]
	if last.Operation != domainjournal.OpMergeLedger || len(last.InfoItems) != 1 {
		t.Fatalf("merge audit = %+v", last)
	}
	if last.InfoItems[0].Nature != infoitem.NatureObserved {
		t.Fatalf("merged projection nature = %q", last.InfoItems[0].Nature)
	}
}

func TestAuditFailureDoesNotCommitMutation(t *testing.T) {
	dir := t.TempDir()
	sc := New("audit-fail", dir)
	defer sc.Close()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.Ledger.UpsertAs(Hypothesis{VulnClass: "xss", Endpoint: "/x"}, AuditActorForAgent("agent")); err == nil {
		t.Fatal("expected durable audit failure")
	}
	if got := sc.Ledger.Len(); got != 0 {
		t.Fatalf("unaudited hypothesis committed: len=%d", got)
	}
}

func TestEvidenceAuditFailureDoesNotCommitEvidence(t *testing.T) {
	dir := t.TempDir()
	sc := New("audit-evidence-fail", dir)
	defer sc.Close()
	h, err := sc.Ledger.UpsertAs(Hypothesis{VulnClass: "xss", Endpoint: "/x"}, AuditActorForAgent("agent"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := sc.Ledger.AddEvidenceAs(h.ID, Evidence{Kind: "probe", Summary: "not committed"}, AuditActorForAgent("agent")); err == nil {
		t.Fatal("expected durable audit failure")
	}
	got, _ := sc.Ledger.Get(h.ID)
	if len(got.Evidence) != 0 {
		t.Fatalf("unaudited evidence committed: %+v", got.Evidence)
	}
}

func TestCapEvidenceDropsDanglingSupports(t *testing.T) {
	ls := NewLedgerStore()
	h := ls.Upsert(Hypothesis{VulnClass: "xss", Endpoint: "/x"})
	ls.AddEvidence(h.ID, Evidence{Kind: "baseline", Nature: EvidenceObserved, Summary: "base"})
	base, _ := ls.Get(h.ID)
	ls.AddEvidence(h.ID, Evidence{
		Kind:     "probe",
		Nature:   EvidenceInferred,
		Summary:  "inference",
		Supports: []string{base.Evidence[0].ID},
	})
	for i := 0; i < maxEvidencePerHypothesis+10; i++ {
		ls.AddEvidence(h.ID, Evidence{Kind: "probe", Nature: EvidenceObserved, Summary: "later observation"})
	}
	got, _ := ls.Get(h.ID)
	present := make(map[string]bool, len(got.Evidence))
	for _, ev := range got.Evidence {
		present[ev.ID] = true
	}
	for _, ev := range got.Evidence {
		for _, support := range ev.Supports {
			if !present[support] {
				t.Fatalf("%s retains dangling support %s", ev.ID, support)
			}
		}
	}
}
