package tools

import (
	"strconv"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	toolrt "github.com/c1ayooo/xalgorix/v4/internal/runtime"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

func TestResolveEgress(t *testing.T) {
	const caPath = "/tmp/xalgorix-ca.pem"
	const scanID = "egress-unit-scan"

	// The gateway path binds a real run-scoped listener (ephemeral port);
	// configure a manager so the port can be observed instead of guessed.
	if err := trafficproxy.Configure(true, t.TempDir(), false); err != nil {
		t.Fatalf("configure traffic proxy: %v", err)
	}
	t.Cleanup(trafficproxy.Reset)

	tests := []struct {
		name         string
		proxyReq     bool
		trafficProxy bool
		proxyURL     string
		bind         string
		wantErr      bool
		wantProfile  string
		wantProxy    string
		wantLivePort bool
		wantCA       string
	}{
		{
			name:        "proxy required with container URL",
			proxyReq:    true,
			proxyURL:    "http://xalgorix-proxy:8081",
			wantProfile: toolrt.NetProfileProxyOnly,
			wantProxy:   "http://xalgorix-proxy:8081",
			wantCA:      caPath,
		},
		{
			name:         "traffic proxy enabled with container URL",
			trafficProxy: true,
			proxyURL:     "http://xalgorix-proxy:8081",
			wantProfile:  toolrt.NetProfileProxyOnly,
			wantProxy:    "http://xalgorix-proxy:8081",
			wantCA:       caPath,
		},
		{
			// P0 fail-closed: no silent fallback to the loopback traffic proxy.
			name:     "proxy required without container URL",
			proxyReq: true,
			wantErr:  true,
		},
		{
			name:         "traffic proxy enabled without container URL",
			trafficProxy: true,
			wantErr:      true,
		},
		{
			name:         "gateway proxy when the proxy binds on 0.0.0.0",
			trafficProxy: true,
			bind:         "0.0.0.0:8081",
			wantProfile:  toolrt.NetProfileProxyOnly,
			wantLivePort: true,
			wantCA:       caPath,
		},
		{
			name:         "gateway proxy accepts IPv6 unspecified bind",
			proxyReq:     true,
			bind:         "[::]:8081",
			wantProfile:  toolrt.NetProfileProxyOnly,
			wantLivePort: true,
			wantCA:       caPath,
		},
		{
			name:     "loopback bind is not container-reachable",
			proxyReq: true,
			bind:     "127.0.0.1:8081",
			wantErr:  true,
		},
		{
			name:     "whitespace URL fails closed when proxy required",
			proxyReq: true,
			proxyURL: "   ",
			wantErr:  true,
		},
		{
			// An explicitly configured URL always means proxy-only, even when
			// nothing else demands a proxy.
			name:        "explicit URL without requirement",
			proxyURL:    "  http://xalgorix-proxy:8081  ",
			wantProfile: toolrt.NetProfileProxyOnly,
			wantProxy:   "http://xalgorix-proxy:8081",
			wantCA:      caPath,
		},
		{
			name:        "no proxy needed and none configured stays direct",
			wantProfile: toolrt.NetProfileDirect,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveEgress(scanID, tt.proxyReq, tt.trafficProxy, tt.proxyURL, tt.bind, caPath)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got plan %+v", got)
				}
				if !strings.Contains(err.Error(), "XALGORIX_TOOLBOX_PROXY_URL") {
					t.Fatalf("error must name XALGORIX_TOOLBOX_PROXY_URL, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.NetProfile != tt.wantProfile {
				t.Errorf("NetProfile = %q, want %q", got.NetProfile, tt.wantProfile)
			}
			if got.ProxyURL != tt.wantProxy {
				t.Errorf("ProxyURL = %q, want %q", got.ProxyURL, tt.wantProxy)
			}
			if tt.wantLivePort {
				port, ok := trafficproxy.ScanListenerPort(scanID)
				if !ok || port <= 0 {
					t.Fatalf("expected a run-scoped listener, got none")
				}
				if got.ProxyGatewayPort != port {
					t.Errorf("ProxyGatewayPort = %d, want the live listener port %d", got.ProxyGatewayPort, port)
				}
			} else if got.ProxyGatewayPort != 0 {
				t.Errorf("ProxyGatewayPort = %d, want 0", got.ProxyGatewayPort)
			}
			if got.CACertPath != tt.wantCA {
				t.Errorf("CACertPath = %q, want %q", got.CACertPath, tt.wantCA)
			}
		})
	}
}

// TestBuildProjectSpecProxyFailClosed covers the seam where the plan is
// assembled: when egress must be proxied but no container-reachable URL is
// configured, no ProjectSpec may be produced (never a direct-egress spec).
func TestBuildProjectSpecProxyFailClosed(t *testing.T) {
	sc := &scanctx.ScanContext{ID: "egress-scan", ScanDir: t.TempDir()}
	cfg := &config.Config{
		ProxyRequired:  true,
		ToolboxImage:   "ghcr.io/example/xalgorix-toolbox@sha256:" + strings.Repeat("a", 64),
		ToolboxProfile: "core",
		DataDir:        t.TempDir(),
	}

	spec, err := buildProjectSpec(sc, cfg)
	if err == nil {
		t.Fatalf("expected fail-closed error, got spec with profile %q proxy %q", spec.NetProfile, spec.Network.ProxyURL)
	}
	if !strings.Contains(err.Error(), "XALGORIX_TOOLBOX_PROXY_URL") {
		t.Fatalf("error must name XALGORIX_TOOLBOX_PROXY_URL, got %v", err)
	}
}

func TestBuildProjectSpecExplicitProxyURL(t *testing.T) {
	sc := &scanctx.ScanContext{ID: "egress-scan", ScanDir: t.TempDir()}
	cfg := &config.Config{
		ProxyRequired:   true,
		ToolboxProxyURL: "http://xalgorix-proxy:8081",
		ToolboxImage:    "ghcr.io/example/xalgorix-toolbox@sha256:" + strings.Repeat("a", 64),
		ToolboxProfile:  "core",
		DataDir:         t.TempDir(),
	}

	spec, err := buildProjectSpec(sc, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.NetProfile != toolrt.NetProfileProxyOnly {
		t.Fatalf("NetProfile = %q, want %q", spec.NetProfile, toolrt.NetProfileProxyOnly)
	}
	if spec.Network.ProxyURL != "http://xalgorix-proxy:8081" {
		t.Fatalf("Network.ProxyURL = %q", spec.Network.ProxyURL)
	}
	if !spec.Network.Internal {
		t.Fatal("proxy-only project network must be internal (no default route)")
	}
	if !envHas(spec.Env, "HTTPS_PROXY=http://xalgorix-proxy:8081") {
		t.Fatalf("container env does not carry the proxy URL: %v", spec.Env)
	}
}

func TestBuildProjectSpecDirectWithoutProxy(t *testing.T) {
	sc := &scanctx.ScanContext{ID: "egress-scan", ScanDir: t.TempDir()}
	cfg := &config.Config{
		ToolboxImage:   "ghcr.io/example/xalgorix-toolbox@sha256:" + strings.Repeat("a", 64),
		ToolboxProfile: "core",
		DataDir:        t.TempDir(),
	}

	spec, err := buildProjectSpec(sc, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.NetProfile != toolrt.NetProfileDirect {
		t.Fatalf("NetProfile = %q, want %q", spec.NetProfile, toolrt.NetProfileDirect)
	}
	if spec.Network.ProxyURL != "" {
		t.Fatalf("direct project must not carry a proxy URL, got %q", spec.Network.ProxyURL)
	}
}

func envHas(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// TestBuildProjectSpecHostNetworkUsesRunScopedListener pins the host-network
// wiring end to end: with the traffic proxy bound on 0.0.0.0 and container
// networking set to host, the spec handed to the driver must carry the
// run-scoped listener URL at loopback. Losing it silently disabled MITM capture
// for every in-container tool (no traffic archive, frozen live feed).
func TestBuildProjectSpecHostNetworkUsesRunScopedListener(t *testing.T) {
	if err := trafficproxy.Configure(true, t.TempDir(), false); err != nil {
		t.Fatalf("configure traffic proxy: %v", err)
	}
	t.Cleanup(trafficproxy.Reset)

	sc := &scanctx.ScanContext{ID: "host-egress-scan", ScanDir: t.TempDir()}
	cfg := &config.Config{
		TrafficProxyBind:   "0.0.0.0:8081",
		ToolboxNetworkMode: "host",
		ToolboxImage:       "ghcr.io/example/xalgorix-toolbox@sha256:" + strings.Repeat("a", 64),
		ToolboxProfile:     "core",
		DataDir:            t.TempDir(),
	}
	spec, err := buildProjectSpec(sc, cfg)
	if err != nil {
		t.Fatalf("buildProjectSpec: %v", err)
	}
	port, ok := trafficproxy.ScanListenerPort(sc.ID)
	if !ok || port <= 0 {
		t.Fatal("expected a run-scoped traffic listener")
	}
	want := "http://127.0.0.1:" + strconv.Itoa(port)
	if spec.Network.ProxyURL != want {
		t.Fatalf("Network.ProxyURL = %q, want %q", spec.Network.ProxyURL, want)
	}
	if spec.Network.ProxyGatewayPort != 0 {
		t.Fatalf("host network must not carry a bridge gateway port, got %d", spec.Network.ProxyGatewayPort)
	}
	if !envHas(spec.Env, "HTTP_PROXY="+want) || !envHas(spec.Env, "http_proxy="+want) {
		t.Fatalf("container env missing the run-scoped proxy: %v", spec.Env)
	}
}
