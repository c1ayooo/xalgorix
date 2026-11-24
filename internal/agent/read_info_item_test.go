package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func seedInfoItem(t *testing.T) *Agent {
	t.Helper()
	sctx := scanctx.New("scan-info", "")
	sctx.Ledger.Upsert(scanctx.Hypothesis{ID: "H-1", VulnClass: "sqli", Status: scanctx.HypothesisQueued})
	if !sctx.Ledger.AddEvidence("H-1", scanctx.Evidence{
		ID: "EV-1", Kind: "probe", Summary: "login form reflected payload\nsecond line",
		Request: "POST /login body=...", Response: "HTTP 200 <script>", Nature: scanctx.EvidenceObserved,
	}) {
		t.Fatal("seed evidence failed")
	}
	return &Agent{scanCtx: sctx}
}

func TestReadInfoItemTool_Layers(t *testing.T) {
	ag := seedInfoItem(t)

	res, err := ag.readInfoItemTool(map[string]string{"id": "EV-1"})
	if err != nil || res.Error != "" {
		t.Fatalf("index: err=%v res=%+v", err, res)
	}
	var index map[string]any
	if err := json.Unmarshal([]byte(res.Output), &index); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	if index["layer"] != "index" || index["hypothesis_id"] != "H-1" || index["title"] != "login form reflected payload" {
		t.Fatalf("index = %#v", index)
	}
	if strings.Contains(index["summary"].(string), "\n") {
		t.Fatalf("index summary kept newline: %q", index["summary"])
	}

	res, _ = ag.readInfoItemTool(map[string]string{"id": "EV-1", "layer": "detail"})
	var detail map[string]any
	_ = json.Unmarshal([]byte(res.Output), &detail)
	if detail["layer"] != "detail" || detail["request"] == "" || detail["response"] == "" {
		t.Fatalf("detail = %#v", detail)
	}

	res, _ = ag.readInfoItemTool(map[string]string{"id": "EV-1", "layer": "artifact"})
	var artifact map[string]any
	_ = json.Unmarshal([]byte(res.Output), &artifact)
	if artifact["layer"] != "artifact" || artifact["has_request"] != true {
		t.Fatalf("artifact = %#v", artifact)
	}
	if _, ok := artifact["request"]; ok {
		t.Fatalf("artifact leaked body: %#v", artifact)
	}
}

func TestReadInfoItemTool_Errors(t *testing.T) {
	ag := seedInfoItem(t)
	if res, _ := ag.readInfoItemTool(map[string]string{}); res.Error == "" {
		t.Fatal("missing id must error")
	}
	if res, _ := ag.readInfoItemTool(map[string]string{"id": "EV-1", "layer": "raw"}); res.Error == "" {
		t.Fatal("invalid layer must error")
	}
	if res, _ := ag.readInfoItemTool(map[string]string{"id": "E-404"}); !strings.Contains(res.Error, "not found") {
		t.Fatalf("unknown id = %+v", res)
	}
	if res, _ := (&Agent{}).readInfoItemTool(map[string]string{"id": "EV-1"}); res.Error == "" {
		t.Fatal("no ledger must degrade with an error result")
	}
}

func TestRecordDeviationTool_WritesJournal(t *testing.T) {
	sctx := scanctx.New("scan-dev-tool", t.TempDir())
	ag := &Agent{ID: "agent-1", scanCtx: sctx}
	res, err := ag.recordDeviationTool(map[string]string{"intent_id": "T-1", "deviation": "target redirected"})
	if err != nil || res.Error != "" {
		t.Fatalf("record: err=%v res=%+v", err, res)
	}
	res, _ = ag.recordDeviationTool(map[string]string{"deviation": "  "})
	if res.Error == "" {
		t.Fatal("empty deviation must be rejected")
	}
}

func TestReadInfoItemTool_ColdPathFallbackAndScope(t *testing.T) {
	infoitem.RegisterReader(&crossScanStubReader{byID: map[string]infoitem.Item{
		"scan-current/info-1": {
			ID: "info-1", ScanID: "scan-current", Nature: infoitem.NatureObserved,
			Coordinates: infoitem.Coordinates{
				Content: infoitem.Content{Text: "cold path detail"},
				Subject: infoitem.Subject{Kind: "endpoint", Identifier: "/login"},
			},
		},
		"scan-other/info-2": {
			ID: "info-2", ScanID: "scan-other", Nature: infoitem.NatureObserved,
			Coordinates: infoitem.Coordinates{
				Content: infoitem.Content{Text: "other scan detail"},
			},
		},
	}})
	t.Cleanup(func() { infoitem.RegisterReader(nil) })

	sctx := scanctx.New("scan-current", "")
	ag := &Agent{scanCtx: sctx}

	res, _ := ag.readInfoItemTool(map[string]string{"id": "info-1"})
	if res.Error != "" || !strings.Contains(res.Output, "cold path detail") {
		t.Fatalf("current-scan fallback = %+v", res)
	}

	res, _ = ag.readInfoItemTool(map[string]string{"id": "info-2", "scan_id": "scan-other"})
	if !strings.Contains(res.Error, "not authorized") {
		t.Fatalf("cross-scan without resolver = %+v", res)
	}

	ag.SetCrossScanResolver(func(scanID string) bool { return scanID == "scan-other" })
	res, _ = ag.readInfoItemTool(map[string]string{"id": "info-2", "scan_id": "scan-other"})
	if res.Error != "" || !strings.Contains(res.Output, "other scan detail") {
		t.Fatalf("authorized cross-scan = %+v", res)
	}

	res, _ = ag.readInfoItemTool(map[string]string{"id": "missing"})
	if res.Error == "" {
		t.Fatal("unknown id must error")
	}
}
