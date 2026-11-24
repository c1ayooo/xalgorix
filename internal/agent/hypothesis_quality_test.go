package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func qualityAgent(t *testing.T, name string) *Agent {
	t.Helper()
	a := &Agent{scanCtx: scanctx.New("quality-"+name, "")}
	t.Cleanup(a.scanCtx.Close)
	return a
}

func TestRecordHypothesisRequiresFalsifiableClaim(t *testing.T) {
	a := qualityAgent(t, "claim")
	res, err := a.recordHypothesisTool(map[string]string{"vuln_class": "idor", "preconditions": "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "falsifiable CLAIM") {
		t.Fatalf("expected claim correction, got %+v", res)
	}
}

func TestRecordHypothesisStoresQualityFields(t *testing.T) {
	a := qualityAgent(t, "fields")
	res, err := a.recordHypothesisTool(map[string]string{
		"vuln_class": "idor", "endpoint": "/api/orders", "title": "Orders are enumerable across users",
		"preconditions": "a low-privilege session", "baseline": "user B gets 403",
		"falsification":    "the same request as user B returns 403 on every attempt",
		"next_observation": "order_id belonging to user B returned with user A's session",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("record failed: %+v %v", res, err)
	}
	h := a.scanCtx.Ledger.All()[0]
	if h.Falsification == "" || h.NextObservation == "" || h.Baseline == "" {
		t.Fatalf("quality fields not stored: %+v", h)
	}
	if !h.ClaimEligible() {
		t.Fatal("a model-recorded formal hypothesis must be claim-eligible")
	}
}

func TestUpdateHypothesisProvenRequiresFindingRef(t *testing.T) {
	a := qualityAgent(t, "proven")
	a.recordHypothesisTool(map[string]string{"vuln_class": "sqli", "title": "SQLi on /users", "preconditions": "unknown"})
	h := a.scanCtx.Ledger.All()[0]

	res, err := a.updateHypothesisTool(map[string]string{"hypothesis_id": h.ID, "status": "proven"})
	if err != nil || res.Error == "" || !strings.Contains(res.Error, "report_vulnerability") {
		t.Fatalf("proven without finding must be refused: %+v %v", res, err)
	}
	// Link an authoritative finding reference, then proven is allowed.
	a.scanCtx.Ledger.AddEvidence(h.ID, scanctx.Evidence{Kind: scanctx.EvidenceFindingRef, FindingID: "XALG-9", Summary: "confirmed"})
	res, err = a.updateHypothesisTool(map[string]string{"hypothesis_id": h.ID, "status": "proven"})
	if err != nil || res.Error != "" {
		t.Fatalf("proven with finding_ref must succeed: %+v %v", res, err)
	}
}

func TestUpdateHypothesisRejectedRequiresRefutingEvidence(t *testing.T) {
	a := qualityAgent(t, "rejected")
	a.recordHypothesisTool(map[string]string{"vuln_class": "xss", "title": "Reflected XSS on /search", "preconditions": "unknown"})
	h := a.scanCtx.Ledger.All()[0]

	res, err := a.updateHypothesisTool(map[string]string{"hypothesis_id": h.ID, "status": "rejected"})
	if err != nil || res.Error == "" || !strings.Contains(res.Error, "refuting observation") {
		t.Fatalf("rejected without evidence must be refused: %+v %v", res, err)
	}
	a.scanCtx.Ledger.AddEvidence(h.ID, scanctx.Evidence{Kind: "baseline", Summary: "control did not reflect the nonce", Nature: scanctx.EvidenceObserved, Response: "HTTP/1.1 200 plain"})
	res, err = a.updateHypothesisTool(map[string]string{"hypothesis_id": h.ID, "status": "rejected"})
	if err != nil || res.Error != "" {
		t.Fatalf("rejected with baseline must succeed: %+v %v", res, err)
	}
}

func TestUpdateHypothesisBlockedRequiresReason(t *testing.T) {
	a := qualityAgent(t, "blocked")
	a.recordHypothesisTool(map[string]string{"vuln_class": "auth", "title": "Default admin credentials work", "preconditions": "unknown"})
	h := a.scanCtx.Ledger.All()[0]

	res, err := a.updateHypothesisTool(map[string]string{"hypothesis_id": h.ID, "status": "blocked"})
	if err != nil || res.Error == "" {
		t.Fatalf("blocked without reason must be refused: %+v %v", res, err)
	}
	res, err = a.updateHypothesisTool(map[string]string{"hypothesis_id": h.ID, "status": "blocked", "next_action": "needs valid credentials"})
	if err != nil || res.Error != "" {
		t.Fatalf("blocked with reason must succeed: %+v %v", res, err)
	}
}
