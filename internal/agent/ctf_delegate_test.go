package agent

import (
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/config"
)

// O1.2 (CORE_LOOP_OPTIMIZATION): XALGORIX_ENABLE_CTF_DELEGATE unlocks
// specialist delegation for CTF/benchmark missions. Default off keeps every
// single-agent semantic byte-identical (hookSingleAgentSpawnGuard,
// hookDelegationCoordinator, maybeAutoDelegate all ride DelegationEnabled or
// ctfDelegationBlocked).

func newCTFAgent(t *testing.T, cfg *config.Config) *Agent {
	t.Helper()
	a := newDelegationTestAgent(t, cfg, NewScanState())
	a.ctfMission = true
	return a
}

func TestCTFDelegateDefaultOffKeepsSingleAgent(t *testing.T) {
	if newCTFAgent(t, &config.Config{}).delegationEnabled() {
		t.Fatal("CTF mission must stay single-agent with the switch off (default)")
	}
	if newCTFAgent(t, &config.Config{EnableCTFDelegate: false}).delegationEnabled() {
		t.Fatal("explicit switch off must stay single-agent")
	}
}

func TestCTFDelegateSwitchOnUnlocksDelegation(t *testing.T) {
	if !newCTFAgent(t, &config.Config{EnableCTFDelegate: true}).delegationEnabled() {
		t.Fatal("switch on must unlock specialist delegation for a CTF mission")
	}
	// The operator-wide off-switch still wins over the CTF unlock.
	if newCTFAgent(t, &config.Config{EnableCTFDelegate: true, DisableAutoDelegate: true}).delegationEnabled() {
		t.Fatal("DisableAutoDelegate must keep overriding the CTF unlock")
	}
}

func TestCTFDelegationBlockedGate(t *testing.T) {
	if !newCTFAgent(t, &config.Config{}).ctfDelegationBlocked() {
		t.Fatal("default CTF mission must be delegation-blocked")
	}
	if newCTFAgent(t, &config.Config{EnableCTFDelegate: true}).ctfDelegationBlocked() {
		t.Fatal("switch-on CTF mission must pass the delegation gate")
	}
	// The gate is CTF-only: professional missions are never blocked by it.
	a := newDelegationTestAgent(t, &config.Config{}, NewScanState())
	if a.ctfDelegationBlocked() {
		t.Fatal("professional mission must never be ctf-delegation-blocked")
	}
}

func TestCTFDelegateMaybeAutoDelegateStaysOffByDefault(t *testing.T) {
	// Engine-owned wave launcher must short-circuit on a default CTF mission
	// even when every recon/plan/ledger milestone is satisfied.
	state := NewScanState()
	satisfyComprehensiveRecon(state)
	state.Iteration = 8
	state.Plan = AutoPlanFromState(state)
	state.PlanBuilt = true
	state.LedgerSeeded = true
	a := newDelegationTestAgent(t, &config.Config{}, state)
	a.ctfMission = true
	if msg := a.maybeAutoDelegate([]string{"http://t.local"}); msg != "" {
		t.Fatalf("switch-off CTF mission must not launch a wave, got %q", msg)
	}
}

func TestCTFChildContractCarriesClaimAndFlagDiscipline(t *testing.T) {
	task := "own the authz-logic lane for challenges a-01..a-08"
	// Switch off (default): raw task, pinned byte-identical behavior.
	if got := buildDelegatedTaskInstruction(task, "child-1", true, false); got != task {
		t.Fatalf("switch-off CTF child must keep the raw task, got:\n%s", got)
	}
	got := buildDelegatedTaskInstruction(task, "child-1", true, true)
	if !strings.Contains(got, task) {
		t.Fatal("CTF child must keep the coordinator's task text")
	}
	for _, want := range []string{"claim_next_hypothesis", "report_vulnerability", "kind=finding_ref", "assigned lane is exhausted"} {
		if !strings.Contains(got, want) {
			t.Fatalf("benchmark contract missing %q:\n%s", want, got)
		}
	}
	// Empty task still carries the contract.
	if got := buildDelegatedTaskInstruction("", "child-1", true, true); !strings.Contains(got, "claim_next_hypothesis") {
		t.Fatalf("benchmark contract must survive an empty task:\n%s", got)
	}
}
