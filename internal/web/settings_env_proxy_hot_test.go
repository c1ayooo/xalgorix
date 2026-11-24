package web

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
)

// TestApplyEnvironmentUpdatesReinitializesProxyPool pins the hot-apply
// contract: a proxy edit takes effect in the running process (new requests see
// the new pool) without a restart, and an invalid required-mode combination is
// rejected with the previous policy kept.
func TestApplyEnvironmentUpdatesReinitializesProxyPool(t *testing.T) {
	proxy.Reset()
	t.Cleanup(proxy.Reset)

	s := &Server{cfg: &config.Config{ProxyRotation: "roundrobin"}}

	if _, err := s.applyEnvironmentUpdates(map[string]string{
		"XALGORIX_USE_PROXY": "true",
		"XALGORIX_PROXY_URL": "http://127.0.0.1:18080",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !proxy.Enabled() {
		t.Fatal("proxy pool was not re-initialized by the settings write")
	}
	got := proxy.GetProxy()
	if got == nil || got.Raw != "http://127.0.0.1:18080" {
		t.Fatalf("pool address = %v, want the newly saved URL", got)
	}

	// A second write replaces the pool immediately.
	if _, err := s.applyEnvironmentUpdates(map[string]string{
		"XALGORIX_PROXY_URL": "http://127.0.0.1:18081",
	}); err != nil {
		t.Fatalf("apply second: %v", err)
	}
	if got := proxy.GetProxy(); got == nil || got.Raw != "http://127.0.0.1:18081" {
		t.Fatalf("pool did not follow the second write: %v", got)
	}

	// Required mode without a URL must be rejected and must not break the live
	// pool (rollback).
	if _, err := s.applyEnvironmentUpdates(map[string]string{
		"XALGORIX_PROXY_REQUIRED": "true",
		"XALGORIX_PROXY_URL":      "",
	}); err == nil {
		t.Fatal("expected required-mode validation error")
	}
	if !proxy.Enabled() {
		t.Fatal("failed write must roll back to the previous, working policy")
	}
	if got := proxy.GetProxy(); got == nil || got.Raw != "http://127.0.0.1:18081" {
		t.Fatalf("rollback lost the pool: %v", got)
	}
}
