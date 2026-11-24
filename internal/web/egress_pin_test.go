package web

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// TestInstallSessionEgressPinKeysByScanContext pins XALG021/XALG022: the pin is
// installed on every start path and the registry key is the ScanContext id the
// egress consumers actually read, not the instance id.
func TestInstallSessionEgressPinKeysByScanContext(t *testing.T) {
	sctx := scanctx.New("ctx-pin", "")
	sess := &scanSession{instanceID: "inst-pin", proxyPin: "direct"}
	s := &Server{}
	s.installSessionEgressPin(sess, sctx)

	if up, ok := proxy.ScanUpstreamFor("ctx-pin"); !ok || !up.Direct {
		t.Fatalf("scan-context pin missing: %+v ok=%v", up, ok)
	}
	if _, ok := proxy.ScanUpstreamFor("inst-pin"); ok {
		t.Fatal("pin was installed under the instance id")
	}

	// "inherit" clears a previous pin for the same run.
	sess.proxyPin = "inherit"
	s.installSessionEgressPin(sess, sctx)
	if _, ok := proxy.ScanUpstreamFor("ctx-pin"); ok {
		t.Fatal("inherit did not clear the run pin")
	}
}
