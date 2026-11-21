package terminal

// R2b traffic-proxy consumer acceptance: the curl/wget rate-limit wrapper
// embeds the traffic-proxy CA flag (written by the parent — XALGORIX_* env
// vars never reach subprocesses), and the HOME-scoped trust config covers
// the default posture. Off mode writes nothing.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/proxy"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
	"github.com/xalgord/xalgorix/v4/internal/trafficproxy"
)

func terminalTestNeedsCurlWget(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	if _, err := exec.LookPath("wget"); err != nil {
		t.Skip("wget not available")
	}
}

func configureMITMForTerminal(t *testing.T) string {
	t.Helper()
	_ = proxy.Close()
	caDir := filepath.Join(t.TempDir(), "ca")
	if err := trafficproxy.Configure(true, caDir, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })
	if trafficproxy.CAPath() == "" {
		t.Fatal("CA not initialized")
	}
	return caDir
}

func TestRateLimitedWrapperEmbedsCACert(t *testing.T) {
	terminalTestNeedsCurlWget(t)
	caDir := configureMITMForTerminal(t)
	binDir := t.TempDir()
	if err := writeRateLimitedBinaryWrapper(binDir, "curl"); err != nil {
		t.Fatal(err)
	}
	if err := writeRateLimitedBinaryWrapper(binDir, "wget"); err != nil {
		t.Fatal(err)
	}
	curlScript, err := os.ReadFile(filepath.Join(binDir, "curl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(curlScript), "--cacert \""+trafficproxy.CAPath()+"\"") {
		t.Fatalf("curl wrapper missing --cacert with CA path %s:\n%s", trafficproxy.CAPath(), curlScript)
	}
	wgetScript, err := os.ReadFile(filepath.Join(binDir, "wget"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wgetScript), "--ca-certificate \""+trafficproxy.CAPath()+"\"") {
		t.Fatalf("wget wrapper missing --ca-certificate:\n%s", wgetScript)
	}
	_ = caDir
}

func TestRateLimitedWrapperOffModeHasNoCAFlag(t *testing.T) {
	terminalTestNeedsCurlWget(t)
	_ = proxy.Close()
	if err := trafficproxy.Configure(false, "", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })
	binDir := t.TempDir()
	if err := writeRateLimitedBinaryWrapper(binDir, "curl"); err != nil {
		t.Fatal(err)
	}
	curlScript, err := os.ReadFile(filepath.Join(binDir, "curl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(curlScript), "--cacert") || strings.Contains(string(curlScript), "extra_args+=(--") {
		t.Fatalf("off-mode wrapper embeds a CA flag:\n%s", curlScript)
	}
}

func TestPrepareTrafficProxyClientConfig(t *testing.T) {
	configureMITMForTerminal(t)
	workDir := t.TempDir()
	if err := prepareTrafficProxyClientConfig(workDir, "test-scan"); err != nil {
		t.Fatal(err)
	}
	curlrc, err := os.ReadFile(filepath.Join(workDir, ".curlrc"))
	if err != nil {
		t.Fatalf(".curlrc: %v", err)
	}
	localCA := filepath.Join(workDir, ".xalgorix-traffic-ca.pem")
	if !strings.Contains(string(curlrc), "cacert = "+localCA) {
		t.Fatalf(".curlrc missing workspace CA: %q", curlrc)
	}
	if _, err := os.Stat(localCA); err != nil {
		t.Fatalf("workspace CA: %v", err)
	}
	wgetrc, err := os.ReadFile(filepath.Join(workDir, ".wgetrc"))
	if err != nil {
		t.Fatalf(".wgetrc: %v", err)
	}
	if !strings.Contains(string(wgetrc), "ca_certificate = "+localCA) {
		t.Fatalf(".wgetrc missing workspace CA: %q", wgetrc)
	}

	// Idempotent rewrite does not churn the file.
	info1, err := os.Stat(filepath.Join(workDir, ".curlrc"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareTrafficProxyClientConfig(workDir, "test-scan"); err != nil {
		t.Fatal(err)
	}
	info2, err := os.Stat(filepath.Join(workDir, ".curlrc"))
	if err != nil {
		t.Fatal(err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatal("unchanged config file was rewritten")
	}
}

func TestPrepareTrafficProxyClientConfigOffWritesNothing(t *testing.T) {
	_ = proxy.Close()
	if err := trafficproxy.Configure(false, "", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })
	workDir := t.TempDir()
	if err := prepareTrafficProxyClientConfig(workDir, "test-scan"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".curlrc", ".wgetrc"} {
		if _, err := os.Stat(filepath.Join(workDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s written in off mode", name)
		}
	}
}

// TestPrepareTrafficProxyClientConfigEmbedsRunToken pins the subprocess
// credential wiring (R2b approval closed loop): a registered run token lands
// in the 0600 HOME-rooted config files as proxy credentials — curl
// `proxy-user <token>:` and wget `proxy_user=<token>` + `proxy_password=`
// (wget needs both lines to send Proxy-Authorization: Basic) — never in the
// 0755 rate-limit wrapper. An unregistered context keeps the CA-only config.
func TestPrepareTrafficProxyClientConfigEmbedsRunToken(t *testing.T) {
	configureMITMForTerminal(t)
	const token = "4a3b2c1d5e6f70819203a4b5c6d7e8f9"
	t.Cleanup(func() { trafficproxy.UnregisterRun(token, "token-scan") })
	trafficproxy.RegisterRun(token, "token-inst", "token-scan", scopeguard.Config{})

	workDir := t.TempDir()
	if err := prepareTrafficProxyClientConfig(workDir, "token-scan"); err != nil {
		t.Fatal(err)
	}
	curlrc, err := os.ReadFile(filepath.Join(workDir, ".curlrc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(curlrc), "proxy-user = "+token+":") {
		t.Fatalf(".curlrc missing proxy-user credentials: %q", curlrc)
	}
	wgetrc, err := os.ReadFile(filepath.Join(workDir, ".wgetrc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wgetrc), "proxy_user="+token) || !strings.Contains(string(wgetrc), "proxy_password=") {
		t.Fatalf(".wgetrc missing proxy_user/proxy_password pair: %q", wgetrc)
	}
	// Config files holding the token stay operator/agent-private (0600 via
	// storage.WriteAtomic), unlike the 0755 wrapper.
	for _, name := range []string{".curlrc", ".wgetrc"} {
		info, err := os.Stat(filepath.Join(workDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", name, info.Mode().Perm())
		}
	}

	// Unregistered context: CA-only config, no credential lines.
	plainDir := t.TempDir()
	if err := prepareTrafficProxyClientConfig(plainDir, "unregistered-scan"); err != nil {
		t.Fatal(err)
	}
	curlrc, err = os.ReadFile(filepath.Join(plainDir, ".curlrc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(curlrc), "proxy-user") {
		t.Fatalf("credential lines written without a registered token: %q", curlrc)
	}
	wgetrc, err = os.ReadFile(filepath.Join(plainDir, ".wgetrc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wgetrc), "proxy_user") || strings.Contains(string(wgetrc), "proxy_password") {
		t.Fatalf("credential lines written without a registered token: %q", wgetrc)
	}
}
