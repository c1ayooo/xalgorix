package httpclient

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// TestBuildClientHonoursTaskEgressPin covers the MITM-off path: the built
// transport must dial the egress entry the run pinned, not the global pool.
func TestBuildClientHonoursTaskEgressPin(t *testing.T) {
	proxy.Reset()
	trafficproxy.Reset()
	t.Cleanup(func() { proxy.Reset(); trafficproxy.Reset() })
	if err := proxy.InitWithPolicy(true, false, "http://127.0.0.1:18080", "", "roundrobin", 0); err != nil {
		t.Fatalf("init pool: %v", err)
	}
	const ctxID = "egress-pin-scan"
	sc := scanctx.New(ctxID, t.TempDir())
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(ctxID) })

	entry := &proxy.Proxy{Raw: "http://10.7.7.7:8080", Type: proxy.ProxyTypeHTTP, Host: "10.7.7.7", Port: "8080"}
	proxy.SetScanUpstream(ctxID, proxy.ScanUpstream{Entry: entry})
	t.Cleanup(func() { proxy.ClearScanUpstream(ctxID) })

	got, proxied, err := egressProxyURL(ctxID)
	if err != nil || !proxied || got == nil || got.Host != "10.7.7.7:8080" {
		t.Fatalf("egress = %v/%v/%v; want the pinned egress", got, proxied, err)
	}
}
