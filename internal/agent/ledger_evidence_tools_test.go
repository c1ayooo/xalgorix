package agent

import (
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

func TestRecordHypothesisRequiresPreconditions(t *testing.T) {
	a := &Agent{scanCtx: scanctx.New("scan-test", "")}
	t.Cleanup(a.scanCtx.Close)

	res, err := a.recordHypothesisTool(map[string]string{"vuln_class": "idor", "endpoint": "/orders"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "preconditions") {
		t.Fatalf("expected preconditions correction, got %+v", res)
	}

	res, err = a.recordHypothesisTool(map[string]string{
		"vuln_class":    "idor",
		"endpoint":      "/orders",
		"preconditions": "unknown",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	h := a.scanCtx.Ledger.All()
	if len(h) != 1 || len(h[0].Preconditions) != 1 || h[0].Preconditions[0] != "unknown" {
		t.Fatalf("preconditions not preserved: %+v", h)
	}
}

func TestRecordHypothesisValidatesAndStoresVOI(t *testing.T) {
	a := &Agent{scanCtx: scanctx.New("scan-voi", "")}
	t.Cleanup(a.scanCtx.Close)

	res, err := a.recordHypothesisTool(map[string]string{
		"vuln_class": "xss", "preconditions": "unknown", "voi": "1.2",
	})
	if err != nil || res.Error == "" {
		t.Fatalf("expected VOI validation error, got result=%+v err=%v", res, err)
	}
	res, err = a.recordHypothesisTool(map[string]string{
		"vuln_class": "xss", "preconditions": "unknown", "voi": "0.4",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("record VOI: result=%+v err=%v", res, err)
	}
	h := a.scanCtx.Ledger.All()[0]
	if !h.VOIEstimated || h.VOI != 0.4 {
		t.Fatalf("VOI not stored: %+v", h)
	}
}

func TestAddEvidenceObservedRequiresRawData(t *testing.T) {
	a := &Agent{scanCtx: scanctx.New("scan-test", "")}
	t.Cleanup(a.scanCtx.Close)
	a.recordHypothesisTool(map[string]string{"vuln_class": "xss", "preconditions": "unknown"})
	h := a.scanCtx.Ledger.All()[0]

	res, err := a.addHypothesisEvidenceTool(map[string]string{
		"hypothesis_id": h.ID,
		"summary":       "interpretation only",
		"nature":        "observed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "raw request or response") {
		t.Fatalf("expected raw data error, got %+v", res)
	}
}

func TestAddEvidenceInferredSupportsMustExist(t *testing.T) {
	a := &Agent{scanCtx: scanctx.New("scan-test", "")}
	t.Cleanup(a.scanCtx.Close)
	a.recordHypothesisTool(map[string]string{"vuln_class": "xss", "preconditions": "unknown"})
	h := a.scanCtx.Ledger.All()[0]

	res, err := a.addHypothesisEvidenceTool(map[string]string{
		"hypothesis_id": h.ID,
		"summary":       "likely vulnerable",
		"nature":        "inferred",
		"supports":      "EV-99",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "unknown support evidence") {
		t.Fatalf("expected support error, got %+v", res)
	}
}
