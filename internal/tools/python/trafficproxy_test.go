package python

// R2b traffic-proxy consumer acceptance: the python subprocess environment
// carries the traffic-proxy trust bundle via parent-process injection.

import (
	"os"
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/proxy"
	"github.com/xalgord/xalgorix/v4/internal/trafficproxy"
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
