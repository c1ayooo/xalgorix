package python

// R2b traffic-proxy consumer acceptance: the python subprocess environment
// carries the traffic-proxy trust bundle via parent-process injection.

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

func TestTrafficProxyTLSBundleEnv(t *testing.T) {
	_ = proxy.Close()
	caDir := t.TempDir() + "/ca"
	if err := trafficproxy.Configure(true, caDir, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })

	workDir := t.TempDir()
	env, err := trafficProxyTLSBundleEnv([]string{"PATH=/usr/bin"}, workDir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	localBundle := workDir + "/.xalgorix-ca-bundle.pem"
	if !strings.Contains(joined, "REQUESTS_CA_BUNDLE="+localBundle) {
		t.Fatalf("REQUESTS_CA_BUNDLE missing from %q", joined)
	}
	if !strings.Contains(joined, "SSL_CERT_FILE="+localBundle) {
		t.Fatalf("SSL_CERT_FILE missing from %q", joined)
	}
	if _, err := os.Stat(localBundle); err != nil {
		t.Fatalf("workspace bundle: %v", err)
	}
}

func TestTrafficProxyTLSBundleEnvOffIsNoop(t *testing.T) {
	_ = proxy.Close()
	if err := trafficproxy.Configure(false, "", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })
	base := []string{"PATH=/usr/bin"}
	env, err := trafficProxyTLSBundleEnv(base, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != len(base) {
		t.Fatalf("off mode injected env: %v", env)
	}
}

// TestExecutePythonAttributesViaProxyUserinfo is the closed-loop end to end
// for the python channel: the child's proxy env carries the run token as
// userinfo (EnvironmentForScan); urllib turns it into a preemptive Proxy-
// Authorization: Basic, the loopback proxy attributes the traffic to the Run
// and journals an observed InfoItem, and the credentials never reach the
// target (stripped hop-by-hop after attribution).
func TestExecutePythonAttributesViaProxyUserinfo(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not available")
	}

	// MITM on, no upstream pool: direct forwarding through the loopback proxy.
	_ = proxy.Close()
	trafficproxy.SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	if err := trafficproxy.Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })

	sc := scanctx.New("python-proxy-attr", t.TempDir())
	scanctx.Activate(sc)
	t.Cleanup(func() {
		scanctx.Deactivate(sc.ID)
		sc.Close()
	})
	const token = "6e5d4c3b2a1908071625344352617f8e"
	trafficproxy.RegisterRun(token, "python-attr-inst", sc.ID, scopeguard.Config{AllowLocalTargets: true})
	t.Cleanup(func() { trafficproxy.UnregisterRun(token, sc.ID) })

	// The default sink installed by Configure IS the production journal
	// writer, so the assertions cover the whole chain: python child → env
	// userinfo → Basic header → proxy attribution → domain journal (Close
	// below drains the sink deterministically before the journal read).

	var sawTargetHit, sawLeakedCredentials atomic.Bool
	// The parent-injected NO_PROXY excludes localhost/127.0.0.1, so bind the
	// target to the loopback ALIAS range (127.0.0.2) — routable on Linux and
	// outside no_proxy, so the python child genuinely traverses the proxy.
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("loopback alias 127.0.0.2 not routable here: %v", err)
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTargetHit.Store(true)
		if r.Header.Get("Proxy-Authorization") != "" {
			sawLeakedCredentials.Store(true)
		}
		_, _ = io.WriteString(w, "py-attr-ok")
	}))
	target.Listener = ln
	target.Start()
	defer target.Close()

	code := "import urllib.request\n" +
		"r = urllib.request.urlopen('" + target.URL + "/py-attr', data=b'x=1', timeout=15)\n" +
		"print(r.status)\n"
	res, execErr := executePythonForContextCtx(context.Background(), sc.ID, map[string]string{
		"code":    code,
		"timeout": "30",
	})
	if execErr != nil {
		t.Fatal(execErr)
	}
	if res.Error != "" {
		t.Fatalf("python child failed: %s / %s", res.Error, res.Output)
	}
	if !strings.Contains(res.Output, "200") {
		t.Fatalf("python output missing the 200 response: %q", res.Output)
	}

	if !sawTargetHit.Load() {
		t.Fatal("target never received the python request")
	}
	if sawLeakedCredentials.Load() {
		t.Fatal("run-token credentials reached the target (not stripped after attribution)")
	}

	// Drain the sink deterministically, then assert the journal.
	if err := trafficproxy.Close(); err != nil {
		t.Fatal(err)
	}

	journalBytes, err := os.ReadFile(filepath.Join(sc.ScanDir, "domain_journal.jsonl"))
	if err != nil {
		t.Fatalf("domain journal: %v", err)
	}
	journal := string(journalBytes)
	if !strings.Contains(journal, "observe_traffic") {
		t.Fatal("journal has no observe_traffic record for python subprocess traffic")
	}
	if !strings.Contains(journal, `"kind":"trafficproxy"`) {
		t.Fatal("InfoItem source is not trafficproxy")
	}
	// Attribution proof: the InfoItem's source agent id is the owning Run.
	if !strings.Contains(journal, `"agent_id":"python-attr-inst"`) {
		t.Fatal("python traffic InfoItem is not attributed to the run")
	}
	if strings.Contains(journal, token) {
		t.Fatal("journal leaked the run token")
	}
}
