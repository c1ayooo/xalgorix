package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

func newProfileTestAgent(t *testing.T, name string, profiles []specialistProfile) *Agent {
	t.Helper()
	cfg := &config.Config{RoleScopedTools: true}
	sctx := scanctx.New("user-profile-test", t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(sctx.Close)
	a := NewAgent(cfg, name, make(chan Event, 8), scopeguard.Config{}, sctx)
	a.delegatedAgentID = "delegated-1"
	a.userProfiles = profiles
	return a
}

// TestMergedSpecialistProfiles pins the merge contract: builtin specialists
// come first, operator-authored profiles append, and nil-agent reads fall
// back to the builtin set.
func TestMergedSpecialistProfiles(t *testing.T) {
	user := []specialistProfile{{
		Role:             "api-fuzzing",
		Focus:            "REST fuzzing",
		VulnClasses:      []string{"api-auth"},
		EvidenceContract: "prove a differential",
		StoppingRule:     "matrix exhausted",
	}}
	a := &Agent{userProfiles: user}
	merged := a.mergedSpecialistProfiles()
	if len(merged) != len(defaultSpecialistProfiles)+1 {
		t.Fatalf("merged = %d profiles, want %d+1", len(merged), len(defaultSpecialistProfiles))
	}
	if merged[0].Role != defaultSpecialistProfiles[0].Role || merged[len(merged)-1].Role != "api-fuzzing" {
		t.Fatalf("merge order wrong: first=%s last=%s", merged[0].Role, merged[len(merged)-1].Role)
	}
	// Builtin slice must not be mutated by merge.
	if len(defaultSpecialistProfiles) != 5 {
		t.Fatalf("builtin slice mutated: %d", len(defaultSpecialistProfiles))
	}

	if _, ok := a.lookupUserProfile("api-fuzzing"); !ok {
		t.Fatal("exact user role lookup failed")
	}
	if _, ok := a.lookupUserProfile("authz-logic"); ok {
		t.Fatal("builtin role must not resolve as a user profile")
	}
}

// TestEligibleSpecialistProfilesIncludesUser pins that operator profiles
// join the testing wave and honor the operator toggle.
func TestEligibleSpecialistProfilesIncludesUser(t *testing.T) {
	cfg := &config.Config{DisabledSpecialists: []string{"disabled-lane"}}
	sctx := scanctx.New("user-profile-eligible", t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(sctx.Close)
	a := NewAgent(cfg, "XalgorixAgent", make(chan Event, 8), scopeguard.Config{}, sctx)
	a.userProfiles = []specialistProfile{
		{Role: "active-lane", Focus: "f", VulnClasses: []string{"xss"}, EvidenceContract: "e", StoppingRule: "s"},
		{Role: "disabled-lane", Focus: "f", VulnClasses: []string{"sqli"}, EvidenceContract: "e", StoppingRule: "s"},
	}
	eligible := a.eligibleSpecialistProfiles()
	found := map[string]bool{}
	for _, p := range eligible {
		found[p.Role] = true
	}
	if !found["active-lane"] {
		t.Fatal("user profile missing from eligible wave")
	}
	if found["disabled-lane"] {
		t.Fatal("operator-disabled user profile must be excluded")
	}
	if found["recon-discovery"] {
		t.Fatal("discovery lane launches early, not with the wave")
	}
}

// TestUserProfileToolScope pins the tool-scope resolution order: an exact
// user-profile name wins with its own hide list; a name that only contains
// builtin keywords still resolves as a user profile (no keyword collision).
func TestUserProfileToolScope(t *testing.T) {
	user := []specialistProfile{{
		Role:             "client-api-fuzzing", // contains "client" → builtin keyword would misfire
		Focus:            "API fuzzing",
		VulnClasses:      []string{"api-auth"},
		EvidenceContract: "e",
		StoppingRule:     "s",
		ToolScopeHidden:  []string{"browser_action", "spawn_agent"},
	}}
	a := newProfileTestAgent(t, "client-api-fuzzing", user)
	a.applyRoleToolScope()
	hidden := map[string]bool{}
	for _, n := range a.registry.SchemaHiddenNames() {
		hidden[n] = true
	}
	if !hidden["browser_action"] || !hidden["spawn_agent"] {
		t.Fatalf("profile hide list not applied: %v", hidden)
	}
	// The builtin keyword map must NOT have leaked its own extras (e.g.
	// verify_sqli from the client-source lane).
	if hidden["verify_sqli"] {
		t.Fatalf("builtin keyword scope leaked into user profile: %v", hidden)
	}
	// Hidden tools stay registered and callable.
	if tool, ok := a.registry.Get("browser_action"); !ok || tool.Name != "browser_action" {
		t.Fatal("hidden tool must remain registered")
	}

	// A user profile WITHOUT a hide list falls back to the Other-lane default.
	plain := []specialistProfile{{
		Role: "plain-lane", Focus: "f", EvidenceContract: "e", StoppingRule: "s",
	}}
	a2 := newProfileTestAgent(t, "plain-lane", plain)
	a2.applyRoleToolScope()
	hidden2 := map[string]bool{}
	for _, n := range a2.registry.SchemaHiddenNames() {
		hidden2[n] = true
	}
	if !hidden2["spawn_agent"] {
		t.Fatalf("fallback (Other lane) scope not applied: %v", hidden2)
	}
}

// TestNudgeRendersUserProfiles pins that the delegation nudge carries the
// merged vocabulary (state.SpecialistProfiles), not just the builtin five.
func TestNudgeRendersUserProfiles(t *testing.T) {
	state := &ScanState{SpecialistProfiles: append(append([]specialistProfile{}, defaultSpecialistProfiles...), specialistProfile{
		Role: "api-fuzzing", Focus: "REST fuzzing", VulnClasses: []string{"api-auth"},
		EvidenceContract: "e", StoppingRule: "s",
	})}
	nudge := buildDelegationNudge(state)
	if !strings.Contains(nudge, "api-fuzzing") {
		t.Fatal("nudge missing user profile role")
	}
	// A nil-profiles state (older callers/tests) still renders the builtins.
	if n := buildDelegationNudge(&ScanState{}); !strings.Contains(n, "authz-logic") {
		t.Fatal("builtin fallback lost in nudge")
	}
}
