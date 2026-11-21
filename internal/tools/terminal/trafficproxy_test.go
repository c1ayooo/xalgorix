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
	if err := prepareTrafficProxyClientConfig(workDir); err != nil {
		t.Fatal(err)
	}
	curlrc, err := os.ReadFile(filepath.Join(workDir, ".curlrc"))
	if err != nil {
		t.Fatalf(".curlrc: %v", err)
	}
	if !strings.Contains(string(curlrc), "cacert = "+trafficproxy.CAPath()) {
		t.Fatalf(".curlrc missing CA: %q", curlrc)
	}
	wgetrc, err := os.ReadFile(filepath.Join(workDir, ".wgetrc"))
	if err != nil {
		t.Fatalf(".wgetrc: %v", err)
	}
	if !strings.Contains(string(wgetrc), "ca_certificate = "+trafficproxy.CAPath()) {
		t.Fatalf(".wgetrc missing CA: %q", wgetrc)
	}

	// Idempotent rewrite does not churn the file.
	info1, err := os.Stat(filepath.Join(workDir, ".curlrc"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareTrafficProxyClientConfig(workDir); err != nil {
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
	if err := prepareTrafficProxyClientConfig(workDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".curlrc", ".wgetrc"} {
		if _, err := os.Stat(filepath.Join(workDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s written in off mode", name)
		}
	}
}
