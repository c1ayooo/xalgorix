package reporting

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// TestReportVulnerabilitySchemaMetadata pins the advisory type metadata added
// for native tool calling. The report gate, required flags and salvage behavior
// are unchanged — only types/enums are documented.
func TestReportVulnerabilitySchemaMetadata(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg)
	tool, ok := reg.Get("report_vulnerability")
	if !ok {
		t.Fatal("report_vulnerability not registered")
	}
	params := make(map[string]tools.Parameter, len(tool.Parameters))
	for _, p := range tool.Parameters {
		params[p.Name] = p
	}

	if !params["title"].Required || params["title"].Type != "string" {
		t.Fatalf("title = %+v", params["title"])
	}
	if got := params["cvss"].Type; got != "number" {
		t.Errorf("cvss type = %q, want number", got)
	}
	sev := params["severity"].Enum
	wantSev := []string{"critical", "high", "medium", "low", "info"}
	if len(sev) != len(wantSev) {
		t.Fatalf("severity enum = %v", sev)
	}
	for i := range wantSev {
		if sev[i] != wantSev[i] {
			t.Fatalf("severity enum = %v, want %v", sev, wantSev)
		}
	}
	if len(params["verification_method"].Enum) == 0 {
		t.Error("verification_method enum is empty")
	}
}
