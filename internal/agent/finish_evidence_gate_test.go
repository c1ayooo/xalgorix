package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestFinishGateBlocksWhenFindingsHaveNoLedgerEvidence(t *testing.T) {
	ctxID := "gate-evidence-" + t.Name()
	sc := scanctx.New(ctxID, "")
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(ctxID) })

	sc.Vulns.Add(map[string]interface{}{"title": "exposed .env", "severity": "high"})

	state := &ScanState{ScanContextID: ctxID, FinishAttempts: 1}
	res := hookFinishGatekeeper(state, nil)
	if !res.Block {
		t.Fatalf("expected finish to be blocked, got %+v", res)
	}
	if !strings.Contains(res.BlockReason, "UNRECORDED EVIDENCE") {
		t.Fatalf("expected the evidence obligation reason, got %q", res.BlockReason)
	}
}

func TestFinishGateAllowsOnceEvidenceExists(t *testing.T) {
	ctxID := "gate-evidence-ok-" + t.Name()
	sc := scanctx.New(ctxID, "")
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(ctxID) })

	sc.Vulns.Add(map[string]interface{}{"title": "exposed .env", "severity": "high"})
	h := sc.Ledger.Upsert(scanctx.Hypothesis{VulnClass: "file_disclosure", Endpoint: "/.env"})
	sc.Ledger.AddEvidence(h.ID, scanctx.Evidence{Kind: "probe", Summary: "200 / .env", Nature: scanctx.EvidenceObserved})

	res := hookFinishGatekeeper(&ScanState{ScanContextID: ctxID, FinishAttempts: 1}, nil)
	if res.Block && strings.Contains(res.BlockReason, "UNRECORDED EVIDENCE") {
		t.Fatalf("evidence obligation must not fire when evidence exists: %q", res.BlockReason)
	}
}
