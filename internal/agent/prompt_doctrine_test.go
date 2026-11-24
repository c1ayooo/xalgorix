package agent

import (
	"regexp"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
	skillstool "github.com/c1ayooo/xalgorix/v4/internal/tools/skills"
)

var skillIndexCategoryLine = regexp.MustCompile(`^- [a-z0-9-]+ \(\d+\):`)

func isSkillIndexCategoryLine(line string) bool {
	return skillIndexCategoryLine.MatchString(line)
}

func doctrinePrompt(t *testing.T, cfg *config.Config, delegated, instruction string) string {
	t.Helper()
	a := &Agent{
		cfg:      cfg,
		registry: tools.NewRegistry(),
	}
	if delegated != "" {
		a.delegatedAgentID = delegated
	}
	return a.buildSystemPrompt([]string{"https://example.test"}, instruction, scanctx.RequestRatePolicy{MaxRPS: 2, Source: "test"})
}

func TestLeanChecklistIsDefault(t *testing.T) {
	prompt := doctrinePrompt(t, &config.Config{RateLimitRPS: 2}, "", "Assess the target.")
	if !strings.Contains(prompt, "Mission loop (not a phase checklist)") {
		t.Fatal("default prompt must use the lean mission loop")
	}
	if strings.Contains(prompt, "CRITICAL INSTRUCTIONS - READ CAREFULLY") {
		t.Fatal("default prompt must not include the 22-phase answer sheet")
	}
}

func TestFullChecklistIsOptIn(t *testing.T) {
	prompt := doctrinePrompt(t, &config.Config{RateLimitRPS: 2, Checklist: "full"}, "", "Assess the target.")
	if !strings.Contains(prompt, "CRITICAL INSTRUCTIONS - READ CAREFULLY") {
		t.Fatal("XALGORIX_CHECKLIST=full must still be available")
	}
}

func TestDelegatedWorkerGetsHypothesisProtocol(t *testing.T) {
	prompt := doctrinePrompt(t, &config.Config{RateLimitRPS: 2}, "agent_child", "Probe /login for auth bypass.")
	if !strings.Contains(prompt, "WORKER EXECUTION PROTOCOL") {
		t.Fatal("delegated worker must get the hypothesis protocol")
	}
	if !strings.Contains(prompt, "Restate the hypothesis") {
		t.Fatal("worker protocol must require restating the hypothesis")
	}
	if !strings.Contains(prompt, "Adversarial self-check before any finding") {
		t.Fatal("worker protocol must require an adversarial self-check")
	}
	if !strings.Contains(prompt, "Probe /login for auth bypass.") {
		t.Fatal("worker prompt must still carry its intent instruction")
	}
}

func TestNoisePostureBlock(t *testing.T) {
	quiet := doctrinePrompt(t, &config.Config{RateLimitRPS: 2, NoiseProfile: "quiet"}, "", "Assess.")
	if !strings.Contains(quiet, "NOISE POSTURE — QUIET") {
		t.Fatal("quiet posture block missing")
	}
	if !strings.Contains(quiet, "wordlist content discovery") {
		t.Fatal("quiet posture must name the declined actions")
	}
	balanced := doctrinePrompt(t, &config.Config{RateLimitRPS: 2}, "", "Assess.")
	if !strings.Contains(balanced, "NOISE POSTURE — BALANCED") {
		t.Fatal("default posture must be balanced")
	}
}

func TestSkillIndexIsBoundedAndIncludesUserSkills(t *testing.T) {
	prompt := doctrinePrompt(t, &config.Config{RateLimitRPS: 2}, "", "Assess.")
	if !strings.Contains(prompt, "SKILL INDEX (built-in corpus, bounded)") {
		t.Fatal("skill index block missing")
	}
	lines := 0
	idx := strings.Index(prompt, "SKILL INDEX (built-in corpus, bounded)")
	if idx < 0 {
		t.Fatal("skill index block missing")
	}
	for _, line := range strings.Split(prompt[idx:], "\n") {
		if isSkillIndexCategoryLine(line) {
			lines++
		}
	}
	if lines > skillIndexMaxCategories {
		t.Fatalf("skill index injected %d category lines, want <= %d", lines, skillIndexMaxCategories)
	}

	a := &Agent{
		cfg:        &config.Config{RateLimitRPS: 2},
		registry:   tools.NewRegistry(),
		userSkills: []skillstool.UserSkill{{Name: "operator-playbook", Body: "custom"}},
	}
	block := skillIndexPromptBlock(a.userSkills)
	if !strings.Contains(block, "operator-playbook") {
		t.Fatal("operator-authored skills must be listed in the index")
	}
}
