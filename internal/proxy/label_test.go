package proxy

import "testing"

func TestUpstreamLabelFor(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	if got := UpstreamLabelFor("none"); got != "global-pool" {
		t.Fatalf("unpinned label = %q", got)
	}
	SetScanUpstream("direct-run", ScanUpstream{Direct: true})
	if got := UpstreamLabelFor("direct-run"); got != "direct" {
		t.Fatalf("direct label = %q", got)
	}
	entry := &Proxy{Raw: "http://10.0.0.9:8080", Type: ProxyTypeHTTP, Host: "10.0.0.9", Port: "8080", Username: "u", Password: "secret"}
	SetScanUpstream("entry-run", ScanUpstream{Entry: entry})
	if got := UpstreamLabelFor("entry-run"); got != entry.String() {
		t.Fatalf("entry label = %q", got)
	}
}
