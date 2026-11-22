package httpclient

// R2b traffic-proxy consumer acceptance: with TLS interception active the
// tool transport routes through the loopback proxy, trusts the traffic-proxy
// CA (real TLS verification — no InsecureSkipVerify), stamps the run-token
// attribution header, and the request reaches the target through the MITM.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

func TestBuildClientRoutesThroughTrafficProxy(t *testing.T) {
	_ = proxy.Close()
	// Operator opt-in: the MITM's direct upstream dial must allow the
	// loopback httptest target (XALGORIX_ALLOW_LOCAL_TARGETS equivalent).
	trafficproxy.SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	t.Cleanup(func() { trafficproxy.SetDialGuardBase(scopeguard.Config{}) })
	if err := trafficproxy.Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = trafficproxy.Close()
	})

	client, err := buildClient(10, false, false, nil, "scan-httpclient-test")
	if err != nil {
		t.Fatalf("buildClient: %v", err)
	}

	// The transport proxies through the loopback MITM and trusts its CA.
	at, ok := client.Transport.(*trafficproxy.AttributionTransport)
	if !ok {
		t.Fatalf("transport is not attribution-wrapped: %T", client.Transport)
	}
	tr, ok := at.Unwrap().(*http.Transport)
	if !ok {
		t.Fatalf("inner transport is not *http.Transport")
	}
	if tr.Proxy == nil {
		t.Fatal("transport does not proxy through the traffic proxy")
	}
	localURL, err := trafficproxy.LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(localURL)
	req, _ := http.NewRequest(http.MethodGet, "http://target.example/", nil)
	proxyURL, err := tr.Proxy(req)
	if err != nil || proxyURL.Host != parsed.Host {
		t.Fatalf("transport proxy = %v (err=%v), want %s", proxyURL, err, parsed.Host)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Fatal("transport does not trust the traffic-proxy CA")
	}
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("transport skipped TLS verification under CA trust")
	}

	// End-to-end: the request reaches an https target through the MITM with
	// full verification on the client leg (RootCAs proves interception).
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "consumer-ok:"+r.Header.Get("X-Probe"))
	}))
	defer target.Close()
	req2, _ := http.NewRequest(http.MethodGet, target.URL+"/via-mitm", nil)
	req2.Header.Set("X-Probe", "attr-marker")
	resp, err := client.Do(req2)
	if err != nil {
		t.Fatalf("request through the MITM: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "consumer-ok:attr-marker") {
		t.Fatalf("unexpected response: status=%d body=%q", resp.StatusCode, body)
	}
}

func TestBuildClientOffModeKeepsDirectRoute(t *testing.T) {
	// Traffic proxy off (and no proxy pool): the transport must be a plain
	// direct transport, byte-identical to the pre-R2b behavior.
	_ = proxy.Close()
	_ = trafficproxy.Close()
	t.Cleanup(func() { _ = trafficproxy.Close() })

	client, err := buildClient(10, false, false, nil, "")
	if err != nil {
		t.Fatalf("buildClient: %v", err)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("off-mode transport is wrapped: %T", client.Transport)
	}
	localURL, err := trafficproxy.LocalURL()
	if err == nil {
		t.Fatalf("LocalURL available in off mode: %s", localURL)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://target.example/", nil)
	proxyURL, perr := tr.Proxy(req)
	if perr == nil && proxyURL != nil {
		t.Fatalf("off-mode transport proxies through %s", proxyURL)
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.RootCAs != nil {
		t.Fatal("off-mode transport trusts the traffic-proxy CA")
	}
}
