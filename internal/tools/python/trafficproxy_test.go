package python

// R2b traffic-proxy consumer acceptance: the python subprocess environment
// carries the traffic-proxy trust bundle via parent-process injection.

import (
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

	env := trafficProxyTLSBundleEnv([]string{"PATH=/usr/bin"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "REQUESTS_CA_BUNDLE="+trafficproxy.BundlePath()) {
		t.Fatalf("REQUESTS_CA_BUNDLE missing from %q", joined)
	}
	if !strings.Contains(joined, "SSL_CERT_FILE="+trafficproxy.BundlePath()) {
		t.Fatalf("SSL_CERT_FILE missing from %q", joined)
	}
	if trafficproxy.BundlePath() == "" {
		t.Fatal("bundle path empty")
	}
}

func TestTrafficProxyTLSBundleEnvOffIsNoop(t *testing.T) {
	_ = proxy.Close()
	if err := trafficproxy.Configure(false, "", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })
	base := []string{"PATH=/usr/bin"}
	env := trafficProxyTLSBundleEnv(base)
	if len(env) != len(base) {
		t.Fatalf("off mode injected env: %v", env)
	}
}
