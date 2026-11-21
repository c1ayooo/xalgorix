package trafficproxy

// Attribution registry acceptance (R2b decision 6): token→Run resolution,
// reverse lookup for HTTP tools, per-scan dial-guard configuration (with the
// provisioned loopback ports), unregistration, and the Chromium bypass list.

import (
	"encoding/base64"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
)

func TestAttributionRegistryRoundTrip(t *testing.T) {
	t.Cleanup(func() { UnregisterRun("tok-1", "scan-1") })
	RegisterRun("tok-1", "inst-1", "scan-1", scopeguard.Config{AllowLoopbackPorts: []int{8080}})

	if got := RunTokenFor("scan-1"); got != "tok-1" {
		t.Fatalf("RunTokenFor = %q, want tok-1", got)
	}
	if got := RunTokenFor("scan-unknown"); got != "" {
		t.Fatalf("RunTokenFor(unknown) = %q, want empty", got)
	}
	attr, ok := resolveToken("tok-1")
	if !ok || attr.InstanceID != "inst-1" || attr.ScanCtxID != "scan-1" {
		t.Fatalf("resolveToken = %+v ok=%v", attr, ok)
	}
	if _, ok := resolveToken("nope"); ok {
		t.Fatal("unknown token resolved")
	}
	// Re-registration overwrites (resume replaces the entry).
	RegisterRun("tok-1", "inst-1", "scan-1", scopeguard.Config{})
	UnregisterRun("tok-1", "scan-1")
	if RunTokenFor("scan-1") != "" {
		t.Fatal("token survived unregistration")
	}
	if _, ok := resolveToken("tok-1"); ok {
		t.Fatal("resolveToken succeeded after unregistration")
	}
	UnregisterRun("", "scan-1") // idempotent
}

// TestExtractAttributionChannels pins the two attribution channels (R2b
// closed loop, channel semantics):
//   - Bearer → ChannelBearer (Go tool transport, already adjudicated);
//   - Basic whose username is a live run token → ChannelSubprocess (the
//     env/config-file credentials curl/wget/python translate into this
//     header);
//   - Basic whose username is NOT in the registry stays unattributed and is
//     not consumed (an upstream proxy's own credentials must never
//     impersonate a Run);
//   - garbage / missing headers stay unattributed.
func TestExtractAttributionChannels(t *testing.T) {
	t.Cleanup(func() { UnregisterRun("chan-tok", "chan-scan") })
	RegisterRun("chan-tok", "chan-inst", "chan-scan", scopeguard.Config{})

	mk := func(auth string) *http.Request {
		r, err := http.NewRequest(http.MethodPost, "http://target.example/admin", nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			r.Header.Set("Proxy-Authorization", auth)
		}
		return r
	}

	// Bearer: the gated channel.
	attr, ok := extractAttribution(mk("Bearer chan-tok"))
	if !ok || attr.InstanceID != "chan-inst" || attr.ScanCtxID != "chan-scan" || attr.Channel != ChannelBearer {
		t.Fatalf("Bearer attribution = %+v ok=%v", attr, ok)
	}

	// Basic: the subprocess channel (curl/python send "<token>:" base64).
	sub := base64.StdEncoding.EncodeToString([]byte("chan-tok:"))
	attr, ok = extractAttribution(mk("Basic " + sub))
	if !ok || attr.InstanceID != "chan-inst" || attr.ScanCtxID != "chan-scan" || attr.Channel != ChannelSubprocess {
		t.Fatalf("Basic attribution = %+v ok=%v", attr, ok)
	}

	// Unpadded base64 also decodes.
	unpadded := base64.RawStdEncoding.EncodeToString([]byte("chan-tok:pw"))
	if _, ok = extractAttribution(mk("Basic " + unpadded)); !ok {
		t.Fatal("unpadded Basic credentials failed to attribute")
	}

	// Registry MISS: real-world proxy credentials (user:secret) decode fine
	// but attribute to nothing — and are NOT an error, just system-level.
	upstreamCreds := base64.StdEncoding.EncodeToString([]byte("user:secret"))
	if attr, ok = extractAttribution(mk("Basic " + upstreamCreds)); ok {
		t.Fatalf("upstream proxy credentials were consumed as attribution: %+v", attr)
	}

	// The run token with a password part still resolves (username is the key).
	tokenWithPass := base64.StdEncoding.EncodeToString([]byte("chan-tok:anything"))
	if _, ok = extractAttribution(mk("Basic " + tokenWithPass)); !ok {
		t.Fatal("Basic token:password did not attribute")
	}

	// Garbage: undecodable base64 and unknown schemes stay unattributed.
	if _, ok = extractAttribution(mk("Basic !!!not-base64!!!")); ok {
		t.Fatal("undecodable Basic attributed")
	}
	if _, ok = extractAttribution(mk("Digest dG9rZW4=")); ok {
		t.Fatal("unknown auth scheme attributed")
	}
	if _, ok = extractAttribution(mk("")); ok {
		t.Fatal("missing Proxy-Authorization attributed")
	}
}

func TestGuardForOperatorMachine(t *testing.T) {
	base := scopeguard.Config{BindAddr: "127.0.0.1", Port: 9137}
	SetDialGuardBase(base)
	t.Cleanup(func() { SetDialGuardBase(scopeguard.Config{}) })

	// The dashboard listener is refused for everyone.
	if err := guardFor("", "127.0.0.1:9137"); err == nil {
		t.Fatal("dashboard listener dial was allowed")
	}
	// Any loopback dial without opt-ins is refused (the operator's machine).
	if err := guardFor("", "127.0.0.1:8080"); err == nil {
		t.Fatal("operator loopback dial was allowed without opt-ins")
	}
	// A public address passes the operator-machine boundary (scope stays at
	// the pre-proxy layer).
	if err := guardFor("", "93.184.216.34:443"); err != nil {
		t.Fatalf("public dial refused by the operator-machine guard: %v", err)
	}
	// Attributed traffic uses its scan's configuration: a provisioned
	// loopback port is allowed for THAT scan only.
	t.Cleanup(func() { UnregisterRun("prov-tok", "prov-scan") })
	RegisterRun("prov-tok", "inst", "prov-scan", scopeguard.Config{
		BindAddr:           "127.0.0.1",
		Port:               9137,
		AllowLoopbackPorts: []int{8080},
	})
	if err := guardFor("prov-scan", "127.0.0.1:8080"); err != nil {
		t.Fatalf("provisioned loopback dial refused: %v", err)
	}
	if err := guardFor("prov-scan", "127.0.0.1:9137"); err == nil {
		t.Fatal("dashboard listener allowed through the per-scan config")
	}
	if err := guardFor("other-scan", "127.0.0.1:8080"); err == nil {
		t.Fatal("provision opt-in leaked to another scan's traffic")
	}
}

func TestProxyBypassList(t *testing.T) {
	// Interception inactive: callers keep their legacy behavior.
	_ = Close()
	if _, ok := ProxyBypassList("scan-bypass"); ok {
		t.Fatal("ProxyBypassList active without a manager")
	}

	if err := Configure(true, tempCADir(t), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })

	// No provisioned ports: force loopback through the proxy.
	got, ok := ProxyBypassList("scan-bypass")
	if !ok || got != "<-loopback>" {
		t.Fatalf("bypass = %q ok=%v, want <-loopback>", got, ok)
	}
	// A provisioned scan's ports bypass the proxy (Chromium dials the app it
	// provisioned directly).
	t.Cleanup(func() { UnregisterRun("bypass-tok", "scan-bypass") })
	RegisterRun("bypass-tok", "inst", "scan-bypass", scopeguard.Config{AllowLoopbackPorts: []int{8081}})
	got, _ = ProxyBypassList("scan-bypass")
	if !strings.Contains(got, "<-loopback>") || !strings.Contains(got, "127.0.0.1:8081") || !strings.Contains(got, "[::1]:8081") {
		t.Fatalf("bypass missing provision entries: %q", got)
	}
	if strings.Contains(got, "9137") {
		t.Fatalf("dashboard port leaked into the bypass list: %q", got)
	}
}

func tempCADir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ca")
	return dir
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestAttributionTransportStamping pins the run-token header semantics: the
// token is stamped only while TLS interception is active, only when the
// request does not already carry Proxy-Authorization, and never toward a
// remote upstream (off mode).
func TestAttributionTransportStamping(t *testing.T) {
	t.Cleanup(func() { UnregisterRun("stamp-tok", "stamp-scan") })
	RegisterRun("stamp-tok", "inst", "stamp-scan", scopeguard.Config{})

	var captured atomic.Pointer[http.Request]
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		captured.Store(r)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	// MITM off: nothing stamped.
	_ = Close()
	at := &AttributionTransport{Inner: inner, ScanCtxID: "stamp-scan"}
	req, _ := http.NewRequest(http.MethodGet, "http://target.example/", nil)
	if _, err := at.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := captured.Load().Header.Get("Proxy-Authorization"); got != "" {
		t.Fatalf("token stamped while interception inactive: %q", got)
	}

	// MITM on: stamped.
	if err := Configure(true, tempCADir(t), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })
	req, _ = http.NewRequest(http.MethodGet, "http://target.example/", nil)
	if _, err := at.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := captured.Load().Header.Get("Proxy-Authorization"); got != "Bearer stamp-tok" {
		t.Fatalf("stamped header = %q, want Bearer stamp-tok", got)
	}

	// An explicitly set Proxy-Authorization is never overwritten.
	req, _ = http.NewRequest(http.MethodGet, "http://target.example/", nil)
	req.Header.Set("Proxy-Authorization", "Basic explicit")
	if _, err := at.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := captured.Load().Header.Get("Proxy-Authorization"); got != "Basic explicit" {
		t.Fatalf("explicit proxy auth was overwritten: %q", got)
	}
}
