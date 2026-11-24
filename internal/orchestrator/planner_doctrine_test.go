package orchestrator

import (
	"strings"
	"testing"
)

func TestPlannerPromptCarriesDecisionDiscipline(t *testing.T) {
	prompt := plannerPromptFor(nil)
	for _, marker := range []string{
		"Decision discipline",
		"FRONTIER FLOOR",
		"DONE vs EXHAUSTED",
		"CONTINUATION DISCIPLINE",
		"NEGATIVE OBSERVATIONS ARE WORKER CLAIMS",
		"ANOMALY IS A CLUE",
		"AFTER A CONFIRMED FINDING, HUNT THE VARIANT",
		"DEPTH vs BREADTH ARBITRATION",
		"BAD INTENT EXAMPLES",
	} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("planner prompt missing %q", marker)
		}
	}
}

func TestPlannerNoiseRulesPerProfile(t *testing.T) {
	quiet := plannerNoiseRules("quiet")
	if !strings.Contains(quiet, "QUIET") || !strings.Contains(quiet, "NEVER plan a wordlist") {
		t.Fatal("quiet noise rules must forbid wordlist planning")
	}
	if strings.Contains(quiet, "A bounded wordlist content-discovery pass is admissible") {
		t.Fatal("quiet must not inherit loud allowances")
	}
	balanced := plannerNoiseRules("")
	if !strings.Contains(balanced, "BALANCED") {
		t.Fatal("empty profile must default to balanced rules")
	}
	// R1: balanced states that discovery is proposed, not granted.
	for _, want := range []string{
		"NO automatic wordlist pass",
		"admitted only while the concrete route inventory is thin",
		"no API description",
		"concrete host:port or CIDR",
		"request budget is configured",
	} {
		if !strings.Contains(balanced, want) {
			t.Errorf("balanced rules missing R1 clause %q", want)
		}
	}
	loud := plannerNoiseRules("loud")
	if !strings.Contains(loud, "LOUD") {
		t.Fatal("loud rules missing")
	}
}

func TestScopePromptUnaffectedByDisciplineSuffix(t *testing.T) {
	if strings.Contains(plannerPromptFor(nil), "target_kind=scope") {
		t.Fatal("generic prompt must not advertise scope intents")
	}
	if !strings.Contains(plannerPromptFor(nil, true), "target_kind=scope") {
		t.Fatal("scoped prompt must advertise scope intents")
	}
}
