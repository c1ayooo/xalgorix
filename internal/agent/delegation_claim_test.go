package agent

// delegation_claim_test.go — pins the spawn dedup slice of the WorkUnit
// protocol: unit-key extraction, active-claim refusal (completed agents
// release), and control-plane exclusion from auto-delegation.

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/agentsgraph"
)

func TestDelegationUnitKey(t *testing.T) {
	cases := []struct {
		task, target, want string
	}{
		{"只攻这一个容器：a-17，unique_code=\"a-17\"，container_addr=http://10.0.179.226:80", "", "challenge:a-17"},
		{"Own ONLY the recon lane against http://10.0.179.225", "https://10.0.179.225:8443", "endpoint:10.0.179.225"},
		{"unique_code: c-09 (Tomcat)", "https://10.0.179.224:8443", "challenge:c-09"},
		{"generic recon with no unit", "", ""},
	}
	for _, tc := range cases {
		if got := delegationUnitKey(tc.task, tc.target); got != tc.want {
			t.Fatalf("unit key(%q,%q)=%q want %q", tc.task, tc.target, got, tc.want)
		}
	}
}

func TestDuplicateSpawnBlocked(t *testing.T) {
	agents := []agentsgraph.AgentState{
		{AgentID: "a1", Status: "running", Task: "只攻 a-17，unique_code=a-17"},
		{AgentID: "a2", Status: "queued", Task: "c-09 solver，unique_code=c-09", Target: "https://10.0.179.224:8443"},
		{AgentID: "a3", Status: "completed", Task: "unique_code=a-16"},
	}
	if owner, blocked := duplicateSpawnBlocked("challenge:a-17", agents); !blocked || owner != "a1" {
		t.Fatalf("running claim not detected: owner=%q blocked=%v", owner, blocked)
	}
	if _, blocked := duplicateSpawnBlocked("challenge:c-09", agents); !blocked {
		t.Fatal("queued claim must block too")
	}
	if _, blocked := duplicateSpawnBlocked("challenge:a-16", agents); blocked {
		t.Fatal("completed agents must release their claim")
	}
	if _, blocked := duplicateSpawnBlocked("challenge:a-01", agents); blocked {
		t.Fatal("unclaimed unit must pass")
	}
	if _, blocked := duplicateSpawnBlocked("", agents); blocked {
		t.Fatal("empty key must never block")
	}
}

func TestAllTargetsControlPlane(t *testing.T) {
	ctx, state := newTestCtxState(t)
	a := NewAgent(&config.Config{MaxIterations: 10, ControlPlaneHosts: []string{"tsecbench.zc.tencent.com", "10.0.100.58"}},
		"cp-guard", make(chan Event, 16), scopeguard.Config{}, ctx)
	t.Cleanup(a.Stop)
	a.state = state
	if !a.allTargetsControlPlane([]string{"https://tsecbench.zc.tencent.com", "http://10.0.100.58"}) {
		t.Fatal("all control-plane targets must be detected")
	}
	if a.allTargetsControlPlane([]string{"https://tsecbench.zc.tencent.com", "http://10.0.179.226"}) {
		t.Fatal("mixed targets must not be treated as control-plane-only")
	}
	if a.allTargetsControlPlane(nil) {
		t.Fatal("empty targets must not match")
	}
	b := NewAgent(&config.Config{MaxIterations: 10}, "cp-none", make(chan Event, 16), scopeguard.Config{}, ctx)
	t.Cleanup(b.Stop)
	if b.allTargetsControlPlane([]string{"https://tsecbench.zc.tencent.com"}) {
		t.Fatal("unset control-plane list must never match")
	}
}
