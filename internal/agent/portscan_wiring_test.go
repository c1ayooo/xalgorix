package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

// port_scan is a first-class tool, so every guard that keys off a terminal
// command string must know about it explicitly — there is no command text to
// pattern-match. These tests pin the three wirings that otherwise fail
// silently: attempt attribution, coverage completion, and the scope gate.

func TestPortScan_AttemptsServiceAndTechDimensions(t *testing.T) {
	dims := reconDimensionsForCommand("port_scan")
	want := map[string]bool{reconDimServices: false, reconDimTech: false}
	for _, d := range dims {
		if _, ok := want[d]; ok {
			want[d] = true
		}
	}
	for dim, seen := range want {
		if !seen {
			t.Fatalf("port_scan must attempt %q, got %v", dim, dims)
		}
	}
}

func TestPortScan_WorkTrackerRecordsAttemptNotCompletion(t *testing.T) {
	state := NewScanState()
	hookWorkTracker(state, map[string]string{"tool_name": "port_scan", "target": "10.0.0.5"})

	if !state.ReconCoverage.Attempted[reconDimServices] {
		t.Fatal("port_scan must record a service_discovery attempt")
	}
	if state.ReconCoverage.ServicesProbed {
		t.Fatal("an attempt must never complete coverage before the result is validated")
	}
}

func TestPortScan_ResultCompletesServiceDiscovery(t *testing.T) {
	state := NewScanState()
	hookReconResultTracker(state, map[string]string{
		"tool_name": "port_scan",
		"output":    "port_scan target=10.0.0.5 ports=top2 | hosts=1 open_ports=2 frameworks=0\nHOST | PORT\n",
	})

	if !state.ReconCoverage.ServicesProbed {
		t.Fatal("a successful port_scan must complete service_discovery")
	}
	if state.ReconCoverage.TechFingerprinted {
		t.Fatal("frameworks=0 must not complete tech_fingerprint (nothing was identified)")
	}
}

func TestPortScan_ResultCompletesTechWhenFrameworksFound(t *testing.T) {
	state := NewScanState()
	hookReconResultTracker(state, map[string]string{
		"tool_name": "port_scan",
		"output":    "port_scan target=10.0.0.5 ports=top2 | hosts=1 open_ports=2 frameworks=3\n",
	})

	if !state.ReconCoverage.TechFingerprinted {
		t.Fatal("frameworks=3 must complete tech_fingerprint")
	}
}

func TestPortScan_MissingBinaryIsFailedAttemptNotCoverage(t *testing.T) {
	state := NewScanState()
	hookReconResultTracker(state, map[string]string{
		"tool_name": "port_scan",
		"output":    "gogo binary not found — port_scan needs the chainreactors/gogo CLI.",
	})

	if state.ReconCoverage.ServicesProbed {
		t.Fatal("a missing binary must not satisfy service_discovery")
	}
	if state.ReconCoverage.FailedAttempts[reconDimServices] != 1 {
		t.Fatalf("expected one failed attempt, got %d", state.ReconCoverage.FailedAttempts[reconDimServices])
	}
}

func TestPortScan_TimeoutIsFailedAttempt(t *testing.T) {
	state := NewScanState()
	hookReconResultTracker(state, map[string]string{
		"tool_name": "port_scan",
		"output":    "[TIMEOUT] gogo exceeded 600s; results below are partial.\n",
	})

	if state.ReconCoverage.ServicesProbed {
		t.Fatal("a timed-out sweep must not complete service_discovery")
	}
	if state.ReconCoverage.FailedAttempts[reconDimServices] != 1 {
		t.Fatal("a timeout must be recorded as a failed attempt")
	}
}

func TestPortScan_ErrorResultIsFailedAttempt(t *testing.T) {
	state := NewScanState()
	hookReconResultTracker(state, map[string]string{
		"tool_name": "port_scan",
		"error":     "port_scan could not run: egress credential",
	})

	if state.ReconCoverage.ServicesProbed {
		t.Fatal("an errored run must not complete service_discovery")
	}
	if state.ReconCoverage.FailedAttempts[reconDimServices] != 1 {
		t.Fatal("an errored run must be recorded as a failed attempt")
	}
}

func TestPortScan_TargetScopeHonorsPortPin(t *testing.T) {
	// A target pinned to one port must not be swept on every other port by a
	// tool whose port set never appears in a command string.
	a := &Agent{targets: []string{"https://app.example.com:8443"}}

	blocked, reason := a.shouldBlockForTargetScope("port_scan", map[string]string{
		"target": "app.example.com",
		"ports":  "top2",
	})
	if !blocked {
		t.Fatalf("a top2 sweep of a port-pinned host must be refused, got reason %q", reason)
	}

	blocked, reason = a.shouldBlockForTargetScope("port_scan", map[string]string{
		"target": "app.example.com",
		"ports":  "8443",
	})
	if blocked {
		t.Fatalf("scanning only the pinned port must be allowed, got: %s", reason)
	}
}

func TestPortScan_TargetScopeBlockCIDRCoveringPinnedHost(t *testing.T) {
	a := &Agent{targets: []string{"https://10.0.0.5:8443"}}

	blocked, reason := a.shouldBlockForTargetScope("port_scan", map[string]string{
		"target": "10.0.0.0/24",
		"ports":  "top2",
	})
	if !blocked {
		t.Fatalf("a subnet sweep covering a port-pinned host must be refused, got reason %q", reason)
	}
}

func TestPortScan_ScopeGateBlocksOperatorMachine(t *testing.T) {
	a := &Agent{
		localGuard: scopeguard.Config{BindAddr: "127.0.0.1", Port: 9000},
	}
	a.SetActivityPolicy("active", "active", []string{"https://pentest-ground.com"})

	blocked, reason := a.shouldBlockForOutOfScope("port_scan", map[string]string{
		"target": "127.0.0.1",
		"ports":  "top2",
	})
	if !blocked {
		t.Fatal("port_scan must refuse a loopback target")
	}
	if !strings.Contains(reason, "operator's machine") {
		t.Fatalf("unexpected reason: %s", reason)
	}

	blocked, reason = a.shouldBlockForOutOfScope("port_scan", map[string]string{
		"target": "203.0.113.10",
		"ports":  "top2",
	})
	if blocked {
		t.Fatalf("a public target must pass the scope gate, got: %s", reason)
	}
}
