package runtime

import "testing"

func TestCapabilityReportDetectsMissingTools(t *testing.T) {
	pack := Toolpack{Tools: []ToolpackTool{
		{Name: "subfinder"}, {Name: "httpx"}, {Name: "nuclei"}, {Name: "ffuf"},
	}}
	web := pack.CapabilityReport(CapabilityWeb)
	if web.Status != "capability_missing" || len(web.Missing) != 1 || web.Missing[0] != "sqlmap" {
		t.Fatalf("web report = %+v", web)
	}
	binary := pack.CapabilityReport(CapabilityBinary)
	if binary.ImageProfile != ProfileFull || binary.Status != "capability_missing" || len(binary.Missing) == 0 {
		t.Fatalf("binary report = %+v", binary)
	}
}

func TestCapabilityForUnknownDefaultsToWeb(t *testing.T) {
	if got := CapabilityFor("nonsense").Profile; got != CapabilityWeb {
		t.Fatalf("unknown capability = %s", got)
	}
	if got := CapabilityFor("binary").ImageProfile; got != ProfileFull {
		t.Fatalf("binary image profile = %s", got)
	}
}

func TestCapabilityReportsCoverDeclaredProfiles(t *testing.T) {
	reports := (Toolpack{}).CapabilityReports()
	if len(reports) != len(CapabilityProfiles()) {
		t.Fatalf("reports = %d, want %d", len(reports), len(CapabilityProfiles()))
	}
	for _, report := range reports {
		if report.Status != "capability_missing" {
			t.Fatalf("empty manifest should be missing, got %+v", report)
		}
	}
}
