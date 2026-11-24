package trafficproxy

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
)

func TestSelectUpstreamHonoursTaskPin(t *testing.T) {
	proxy.Reset()
	t.Cleanup(proxy.Reset)
	m := &Manager{}

	// Direct pin beats the global pool.
	proxy.SetScanUpstream("pin-direct", proxy.ScanUpstream{Direct: true})
	p, direct, err := m.selectUpstream("pin-direct")
	if err != nil || !direct || p != nil {
		t.Fatalf("direct pin = %v/%v/%v", p, direct, err)
	}

	// Entry pin resolves to that pool member.
	entry := &proxy.Proxy{Raw: "http://10.0.0.5:8080", Type: proxy.ProxyTypeHTTP, Host: "10.0.0.5", Port: "8080"}
	proxy.SetScanUpstream("pin-entry", proxy.ScanUpstream{Entry: entry})
	p, direct, err = m.selectUpstream("pin-entry")
	if err != nil || direct || p != entry {
		t.Fatalf("entry pin = %v/%v/%v", p, direct, err)
	}

	// No pin + no global manager = direct (not required).
	p, direct, err = m.selectUpstream("unpinned")
	if err != nil || !direct || p != nil {
		t.Fatalf("unpinned = %v/%v/%v", p, direct, err)
	}
}
