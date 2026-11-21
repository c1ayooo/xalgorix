package trafficproxy

// Subprocess attribution channel acceptance (R2b closed loop, channel 2):
// EnvironmentForScan embeds the run token as proxy-URL userinfo (curl/python
// translate it into Proxy-Authorization: Basic), and a subprocess-shaped
// Basic request is attributed into the domain journal exactly like the
// Bearer channel — while carrying no token anywhere in the observation.

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/proxy"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
)

// TestEnvironmentForScanEmbedsRunTokenUserInfo pins the subprocess env
// contract: every proxy var carries the token as userinfo WITH the fixed
// placeholder password (this python runtime only sends Basic for user:pass
// userinfo), inherited proxy settings are still replaced, and unrelated env
// survives untouched.
func TestEnvironmentForScanEmbedsRunTokenUserInfo(t *testing.T) {
	_ = proxy.Close()
	if err := Configure(true, "", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })

	t.Cleanup(func() { UnregisterRun("env-tok", "env-scan") })
	RegisterRun("env-tok", "env-inst", "env-scan", scopeguard.Config{})

	env, err := EnvironmentForScan([]string{"PATH=/usr/bin", "http_proxy=http://old:1", "NO_PROXY=*"}, "env-scan")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "http://old:1") || strings.Contains(joined, "NO_PROXY=*") {
		t.Fatal("inherited proxy settings were not replaced")
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatal("unrelated environment setting lost")
	}
	plain, err := Environment([]string{})
	if err != nil {
		t.Fatal(err)
	}
	var plainURL string
	for _, kv := range plain {
		if strings.HasPrefix(kv, "http_proxy=") {
			plainURL = strings.TrimPrefix(kv, "http_proxy=")
		}
	}
	if plainURL == "" {
		t.Fatal("Environment lost the loopback URL")
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		want := key + "=" + "http://env-tok:xalgorix@" + strings.TrimPrefix(plainURL, "http://")
		if !strings.Contains(joined, want+"\n") && !strings.HasSuffix(joined, want) {
			t.Fatalf("env %s = userinfo form missing: want %q in\n%s", key, want, joined)
		}
	}
	// The userinfo form must parse so consumers see user=token with the
	// placeholder password, host/port identical to the plain form. The
	// proxy's Basic attribution reads the username only.
	u, err := url.Parse("http://env-tok:xalgorix@" + strings.TrimPrefix(plainURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.User.Username(); got != "env-tok" {
		t.Fatalf("userinfo user = %q, want the run token", got)
	}
	if pw, _ := u.User.Password(); pw != "xalgorix" {
		t.Fatalf("userinfo password = %q, want the fixed placeholder", pw)
	}
}

// TestEnvironmentForScanWithoutTokenStaysSystemForm pins the honest
// degradation: no registered token (mode off mint failure / stale session /
// unattributed context) → byte-identical to the plain Environment form.
func TestEnvironmentForScanWithoutTokenStaysSystemForm(t *testing.T) {
	_ = proxy.Close()
	if err := Configure(true, "", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })

	base := []string{"PATH=/usr/bin"}
	env, err := EnvironmentForScan(base, "no-such-scan")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Environment(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(env, "\n") != strings.Join(plain, "\n") {
		t.Fatalf("unregistered scan changed the environment:\n%s\nvs\n%s", strings.Join(env, "\n"), strings.Join(plain, "\n"))
	}
	if strings.Contains(strings.Join(env, "\n"), "%40") || strings.Contains(strings.Join(env, "\n"), "@") {
		t.Fatalf("userinfo appeared without a registered token:\n%s", strings.Join(env, "\n"))
	}
}

// TestMITMObservesSubprocessBasicAttribution is the end-to-end subprocess
// channel proof: a request shaped like curl's (Proxy-Authorization: Basic
// <token>:) is attributed to the Run, produces an observed InfoItem in the
// domain journal, and leaks neither the token nor any header value — the
// mitm_test.go:162 assertion shape, replayed for the Basic channel.
func TestMITMObservesSubprocessBasicAttribution(t *testing.T) {
	scanDir := t.TempDir()
	sc := scanctx.New("r2b-basic-scan", scanDir)
	scanctx.Activate(sc)
	t.Cleanup(func() {
		scanctx.Deactivate(sc.ID)
		sc.Close()
	})
	token := "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	RegisterRun(token, "instance-basic", sc.ID, scopeguard.Config{AllowLocalTargets: true})
	t.Cleanup(func() { UnregisterRun(token, sc.ID) })

	const secret = "basic-secret-header-value"
	var sawHeader, sawUpstreamProxyAuth sync.Map
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Probe") == secret {
			sawHeader.Store("probe", true)
		}
		// The run token must NEVER reach the upstream target: the Basic
		// credentials are hop-by-hop and stripped after attribution.
		if pa := r.Header.Get("Proxy-Authorization"); pa != "" {
			sawUpstreamProxyAuth.Store("leak", pa)
		}
		_, _ = io.WriteString(w, "basic-ok")
	}))
	defer target.Close()

	_ = proxy.Close() // no upstream pool: direct forwarding
	SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	if err := Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })
	var mu sync.Mutex
	var got []TrafficObservation
	SetObservationSink(func(o TrafficObservation) {
		mu.Lock()
		got = append(got, o)
		mu.Unlock()
		observeTraffic(o)
	})
	collect := func() []TrafficObservation {
		mu.Lock()
		defer mu.Unlock()
		out := make([]TrafficObservation, len(got))
		copy(out, got)
		return out
	}
	client := proxiedClient(t)

	req, err := http.NewRequest(http.MethodPost, target.URL+"/admin/config?token=hunter3", strings.NewReader("role=admin"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Probe", secret)
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(token+":")))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("subprocess-attributed request: %v", err)
	}
	resp.Body.Close()

	Close() // drain sink + stop
	if _, leak := sawUpstreamProxyAuth.Load("leak"); leak {
		t.Fatal("run token Basic credentials reached the upstream target")
	}
	if _, seen := sawHeader.Load("probe"); !seen {
		t.Fatal("target did not receive the request header")
	}

	var attributed *TrafficObservation
	for i := range collect() {
		o := collect()[i]
		if o.Method == http.MethodPost && o.ScanCtxID == sc.ID {
			attributed = &o
		}
	}
	if attributed == nil {
		t.Fatalf("no subprocess-attributed POST observation among %+v", collect())
	}
	if attributed.InstanceID != "instance-basic" {
		t.Fatalf("instance = %q", attributed.InstanceID)
	}
	if attributed.HighRisk == "" {
		t.Fatalf("POST /admin/config was not flagged high-risk: %+v", attributed)
	}
	if !strings.Contains(attributed.URL, "token=%2A%2A%2A%2A") && !strings.Contains(attributed.URL, "token=****") {
		t.Fatalf("sensitive query value not masked in %q", attributed.URL)
	}

	// Observation payload carries no token material.
	raw, err := json.Marshal(attributed)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{secret, token, "Basic", "role=admin"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("observation leaked %q: %s", leaked, raw)
		}
	}

	// The domain journal carries the observed InfoItem for the subprocess
	// channel too.
	journalBytes, err := os.ReadFile(filepath.Join(scanDir, "domain_journal.jsonl"))
	if err != nil {
		t.Fatalf("domain journal: %v", err)
	}
	journal := string(journalBytes)
	if !strings.Contains(journal, "observe_traffic") {
		t.Fatal("journal has no observe_traffic record")
	}
	if !strings.Contains(journal, `"nature":"observed"`) {
		t.Fatal("InfoItem is not observed")
	}
	if !strings.Contains(journal, `"kind":"trafficproxy"`) {
		t.Fatal("InfoItem source is not trafficproxy")
	}
	if strings.Contains(journal, secret) || strings.Contains(journal, token) {
		t.Fatal("journal leaked a header value or the run token")
	}
}
