package web

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/intelaccess"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

// TestCIDRScopeMatchesAgentSemantics pins XALG020: the web layer keeps the
// CIDR mask and the shared agent matcher (the ONLY matcher now) judges
// containment — web no longer has a second implementation to drift.
func TestCIDRScopeMatchesAgentSemantics(t *testing.T) {
	if host, port := targetHostPort("10.20.30.0/24"); host != "10.20.30.0/24" || port != "" {
		t.Fatalf("targetHostPort CIDR = %q %q", host, port)
	}
	scope := &agent.EngagementScope{Targets: []string{"10.20.30.0/24"}}
	if ok, why := scope.DestinationAllowed("10.20.30.4"); !ok {
		t.Fatalf("address inside the CIDR rejected: %s", why)
	}
	if ok, _ := scope.DestinationAllowed("10.20.31.4"); ok {
		t.Fatal("address outside the CIDR accepted")
	}
	v6 := &agent.EngagementScope{Targets: []string{"2001:db8::/32"}}
	if ok, why := v6.DestinationAllowed("2001:db8::5"); !ok {
		t.Fatalf("IPv6 address inside the CIDR rejected: %s", why)
	}
	if ok, _ := scope.DestinationAllowed("app.example.com"); ok {
		t.Fatal("hostname matched a CIDR entry")
	}
	// A whole range is authorized only when the allow list contains it (a /24
	// is not authorized because its base address is).
	if ok, _ := scope.DestinationAllowed("10.20.0.0/16"); ok {
		t.Fatal("range wider than the allow entry was accepted")
	}
}

func TestValidateTargetsInScopeAcceptsCIDR(t *testing.T) {
	scope := platform.Scope{Targets: []string{"10.20.30.0/24"}}
	if err := validateTargetsInScope([]string{"10.20.30.4"}, scope); err != nil {
		t.Fatalf("in-scope CIDR target rejected: %v", err)
	}
	if err := validateTargetsInScope([]string{"10.20.31.4"}, scope); err == nil {
		t.Fatal("out-of-scope address accepted")
	}
}

// TestApplyProjectScopeToLiveRuns pins XALG019 end-to-end wiring: the PUT scope
// handler replaces the snapshot of a live run mounted on that project.
func TestApplyProjectScopeToLiveRuns(t *testing.T) {
	s := &Server{}
	sctx := scanctx.New("live-scope-ctx", "")
	sctx.ProjectID = "proj-live"
	ag := agent.NewAgent(&config.Config{}, "live-scope-agent", make(chan agent.Event, 8), scopeguard.Config{}, sctx)
	t.Cleanup(ag.Stop)
	s.registerLiveRun(&liveRunHandles{instanceID: "inst-live", scanCtx: sctx, agent: ag})
	t.Cleanup(func() { s.unregisterLiveRun("inst-live") })

	s.applyProjectScopeToLiveRuns("proj-live", platform.Scope{
		Targets:    []string{"10.20.30.0/24"},
		AllowPorts: []int{443},
		Fence:      &platform.Fence{Enforce: true, DenyTargets: []string{"10.20.30.9"}},
	})
	scope := ag.EngagementScope()
	if scope == nil || len(scope.Targets) != 1 || scope.Targets[0] != "10.20.30.0/24" ||
		len(scope.Deny) != 1 || len(scope.AllowPorts) != 1 {
		t.Fatalf("live agent scope = %+v", scope)
	}

	// Removing the fence clears the live snapshot (explicit operator PUT).
	s.applyProjectScopeToLiveRuns("proj-live", platform.Scope{})
	if ag.EngagementScope() != nil {
		t.Fatal("live agent kept a stale scope after the fence was removed")
	}
}

// TestScopeSamplesAgreeAcrossPaths pins INTEL_HANDOFF_SPEC acceptance #8: one
// sample set must get the SAME verdict from the four places that decide scope —
// task creation, dispatch authorization, the approval fence and the agent
// guard. The dispatch judge and the agent guard share agent.EngagementScope;
// creation and the fence now delegate their host matching to it as well, so a
// future second implementation fails here.
func TestScopeSamplesAgreeAcrossPaths(t *testing.T) {
	ctx := context.Background()
	scope := platform.Scope{
		Targets:    []string{"acme.example", "*.acme.example", "10.20.30.0/24"},
		AllowPorts: []int{443, 8443},
		Fence:      &platform.Fence{Enforce: true, DenyTargets: []string{"billing.acme.example"}},
	}
	engagement := &agent.EngagementScope{
		Targets:    scope.Targets,
		Deny:       scope.Fence.DenyTargets,
		AllowPorts: scope.AllowPorts,
	}
	judge := intelaccess.NewJudge(intelaccess.NewMemoryStore(), engagement, true)

	cases := []struct {
		target string
		inside bool
	}{
		{"https://api.acme.example:8443/x", true},
		{"acme.example:443", true},
		{"10.20.30.4:443", true},
		{"https://10.20.30.4:443/x", true},
		{"10.20.31.4:443", false},
		{"https://evil.example:443/x", false},
		{"https://acme.example:9000/x", false},
		{"http://acme.example:80/x", false}, // port outside allow_ports
	}
	for _, tc := range cases {
		creation := validateTargetsInScope([]string{tc.target}, scope) == nil
		shared, why := engagement.DestinationAllowed(tc.target)
		decision, err := judge.Decide(ctx, intelaccess.DecisionInput{ScanID: "scope-samples", Target: tc.target})
		if err != nil {
			t.Fatalf("%s: judge error: %v", tc.target, err)
		}
		fenceInside := fenceHostPortReason(scope, *scope.Fence, tc.target) == ""
		if creation != tc.inside {
			t.Errorf("%s: creation verdict = %v, want %v", tc.target, creation, tc.inside)
		}
		if shared != tc.inside {
			t.Errorf("%s: shared matcher verdict = %v (%s), want %v", tc.target, shared, why, tc.inside)
		}
		if decision.Allow != tc.inside {
			t.Errorf("%s: dispatch decision = %+v, want allow=%v", tc.target, decision, tc.inside)
		}
		if fenceInside != tc.inside {
			t.Errorf("%s: fence verdict = %v, want %v", tc.target, fenceInside, tc.inside)
		}
	}

	// A deny target is refused by every runtime path. Task creation has no deny
	// concept (the deny list is a runtime fence), so it is deliberately absent.
	deny := "https://billing.acme.example:443/x"
	if ok, _ := engagement.DestinationDenied(deny); !ok {
		t.Fatal("deny target not recognized by the shared matcher")
	}
	if decision, _ := judge.Decide(ctx, intelaccess.DecisionInput{ScanID: "scope-samples", Target: deny}); decision.Allow || decision.Reason != intelaccess.ReasonDenyTarget {
		t.Fatalf("dispatch decision for deny target = %+v", decision)
	}
	if reason := fenceHostPortReason(scope, *scope.Fence, deny); !strings.Contains(reason, "excluded by the project fence") {
		t.Fatalf("fence reason for deny target = %q", reason)
	}
}
