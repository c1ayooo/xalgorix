package agent

import "testing"

func TestSetScanContextsPreservesAllPathsAndSingleCompatibility(t *testing.T) {
	a := &Agent{}
	a.SetScanContexts([]string{"/ctx/a", " /ctx/b ", "/ctx/a", ""})
	if len(a.scanContextPaths) != 2 || a.scanContextPaths[0] != "/ctx/a" || a.scanContextPaths[1] != "/ctx/b" {
		t.Fatalf("scanContextPaths = %v", a.scanContextPaths)
	}
	if a.scanContext != "/ctx/a" {
		t.Fatalf("single-path compatibility field = %q", a.scanContext)
	}
	a.SetScanContext("/ctx/c")
	if len(a.scanContextPaths) != 1 || a.scanContextPaths[0] != "/ctx/c" || a.scanContext != "/ctx/c" {
		t.Fatalf("SetScanContext did not reset paths: %v / %q", a.scanContextPaths, a.scanContext)
	}
}
