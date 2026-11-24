package web

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
)

func TestValidateTaskProxyPin(t *testing.T) {
	proxy.Reset()
	t.Cleanup(proxy.Reset)
	if err := proxy.InitWithPolicy(true, false, "http://127.0.0.1:18080", "", "roundrobin", 0); err != nil {
		t.Fatalf("init pool: %v", err)
	}
	s := &Server{}
	if err := s.validateTaskProxyPin(""); err != nil {
		t.Fatalf("inherit: %v", err)
	}
	if err := s.validateTaskProxyPin("direct"); err != nil {
		t.Fatalf("direct without required: %v", err)
	}
	if err := s.validateTaskProxyPin("1"); err != nil {
		t.Fatalf("entry 1: %v", err)
	}
	if err := s.validateTaskProxyPin("9"); err == nil {
		t.Fatal("missing pool entry must be rejected")
	}

	// Proxy-required mode forbids direct egress.
	if err := proxy.InitWithPolicy(true, true, "http://127.0.0.1:18080", "", "roundrobin", 0); err != nil {
		t.Fatalf("required init: %v", err)
	}
	if err := s.validateTaskProxyPin("direct"); err == nil {
		t.Fatal("direct must be rejected in proxy-required mode")
	}
	if err := s.validateTaskProxyPin("1"); err != nil {
		t.Fatalf("entry stays valid in required mode: %v", err)
	}
}
