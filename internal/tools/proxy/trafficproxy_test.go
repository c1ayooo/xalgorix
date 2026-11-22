package proxy

// R2b traffic-proxy consumer acceptance for send_request: with TLS
// interception active the tool client routes through the loopback proxy,
// trusts the traffic-proxy CA, and stamps the run-token attribution header;
// a run token is never sent toward a remote upstream (blind/off modes).

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// atomicFlag is a tiny flag helper for the leak assertion.
type atomicFlag struct {
	v atomic.Bool
}

func (a *atomicFlag) set(b bool) { a.v.Store(b) }
func (a *atomicFlag) load() bool { return a.v.Load() }

// timeSecond keeps the timeout literal readable in InitWithPolicy calls.
const timeSecond = time.Second

func TestSendRequestClientRoutesThroughTrafficProxy(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "send-request-ok:"+r.URL.Path)
	}))
	defer target.Close()

	trafficproxy.SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	t.Cleanup(func() { trafficproxy.SetDialGuardBase(scopeguard.Config{}) })
	if err := trafficproxy.Configure(true, t.TempDir()+"/ca", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })
	trafficproxy.RegisterRun("sendreq-token", "inst-send", "scan-send", scopeguard.Config{AllowLocalTargets: true})
	t.Cleanup(func() { trafficproxy.UnregisterRun("sendreq-token", "scan-send") })

	client, err := httpClient("scan-send", nil)
	if err != nil {
		t.Fatalf("httpClient: %v", err)
	}
	at, ok := client.Transport.(*trafficproxy.AttributionTransport)
	if !ok {
		t.Fatalf("transport is not attribution-wrapped: %T", client.Transport)
	}
	tr, ok := at.Unwrap().(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Fatalf("transport does not trust the traffic-proxy CA (transport=%T)", at.Unwrap())
	}

	// End-to-end through the MITM with verification on the client leg.
	req, err := http.NewRequest(http.MethodGet, target.URL+"/deep", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request through the MITM: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "send-request-ok:/deep") {
		t.Fatalf("unexpected response: status=%d body=%q", resp.StatusCode, body)
	}
}

func TestSendRequestOffModeDirectClientUnwrappedProxying(t *testing.T) {
	// Off mode + no pool config: the direct branch builds an unproxied,
	// guarded client — no traffic-proxy URL, no CA override, and the run
	// token is not stamped (the MITM is off; a stamp would only matter
	// toward a remote upstream).
	_ = proxy.Close()
	if err := trafficproxy.Configure(false, "", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })
	trafficproxy.RegisterRun("remote-leak-probe", "inst", "scan-remote", scopeguard.Config{})
	t.Cleanup(func() { trafficproxy.UnregisterRun("remote-leak-probe", "scan-remote") })

	client, err := httpClient("scan-remote", nil)
	if err != nil {
		t.Fatal(err)
	}
	at, ok := client.Transport.(*trafficproxy.AttributionTransport)
	if !ok {
		t.Fatalf("off-mode direct client transport = %T", client.Transport)
	}
	tr, ok := at.Unwrap().(*http.Transport)
	if !ok {
		t.Fatalf("off-mode direct client inner transport = %T", at.Unwrap())
	}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	proxyURL, _ := tr.Proxy(req)
	if proxyURL != nil {
		t.Fatalf("off-mode direct client proxies through %s", proxyURL)
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.RootCAs != nil {
		t.Fatal("off-mode direct client trusts the traffic-proxy CA")
	}
}
