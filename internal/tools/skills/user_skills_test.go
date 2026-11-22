package skills

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xalgord/xalgorix/v4/internal/tools"
)

func userSkillsTestFS() fstest.MapFS {
	return fstest.MapFS{
		"web-application-security/testing-for-xss-vulnerabilities/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: Testing for XSS Vulnerabilities\ndescription:\n  - Cross-site testing guidance\n---\n\n# XSS methodology\n\nEmbedded body."),
		},
	}
}

func userSkillsTestState(list []UserSkill) *agentSkillState {
	state := newAgentSkillState()
	attachUserSkills(state, list)
	return state
}

// TestUserSkillReadShadowsEmbedded pins the precedence contract: an
// operator-authored skill with an existing corpus name wins, carries an
// explicit provenance banner, and flows through the same duplicate-
// suppression state machine (skill_name metadata tagged).
func TestUserSkillReadShadowsEmbedded(t *testing.T) {
	fs := userSkillsTestFS()
	state := userSkillsTestState([]UserSkill{{
		Name:        "testing-for-xss-vulnerabilities",
		Description: "operator engagement methodology",
		Body:        "# Operator XSS playbook",
	}})
	r := tools.NewRegistry()
	readSkill := makeReadSkillWithState(fs, r, state)

	res, err := readSkill(map[string]string{"name": "testing-for-xss-vulnerabilities"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "Operator XSS playbook") {
		t.Fatalf("user body missing: %.120s", res.Output)
	}
	if !strings.Contains(res.Output, "operator-authored") {
		t.Fatalf("provenance banner missing: %.160s", res.Output)
	}
	if strings.Contains(res.Output, "Embedded body") {
		t.Fatalf("embedded corpus leaked through shadowed skill")
	}
	if res.Metadata["skill_name"] != "testing-for-xss-vulnerabilities" {
		t.Fatalf("skill_name metadata missing: %v", res.Metadata)
	}

	// A state without user skills keeps reading the corpus even for the
	// same name — per-scan isolation (one scan's set never leaks to another).
	bare := newAgentSkillState()
	res, err = makeReadSkillWithState(fs, tools.NewRegistry(), bare)(map[string]string{"name": "testing-for-xss-vulnerabilities"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Embedded body") {
		t.Fatalf("corpus read broken without user skills: %.160s", res.Output)
	}
}

// TestUserSkillReadExactNameOnly pins that aliases apply only to the corpus:
// a corpus alias must not resolve a user skill, and a user skill must not
// hijack an alias.
func TestUserSkillReadExactNameOnly(t *testing.T) {
	fs := userSkillsTestFS()
	state := userSkillsTestState([]UserSkill{{Name: "custom-engagement-checklist", Body: "# Checklist"}})
	r := tools.NewRegistry()
	readSkill := makeReadSkillWithState(fs, r, state)

	res, err := readSkill(map[string]string{"name": "custom-engagement-checklist"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || !strings.Contains(res.Output, "# Checklist") {
		t.Fatalf("exact user skill failed: %+v", res)
	}

	// Unknown name falls through to the corpus error path with the fuzzy hint.
	res, err = readSkill(map[string]string{"name": "no-such-skill-anywhere"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "skill not found") {
		t.Fatalf("corpus error path lost: %+v", res)
	}
}

// TestUserSkillListAndSearch pins the list/search merge: USER category in
// list output, user skills compete in search scoring, and a state without
// user skills renders no USER section.
func TestUserSkillListAndSearch(t *testing.T) {
	fs := userSkillsTestFS()
	list := []UserSkill{{
		Name:        "testing-payment-price-tampering",
		Description: "prove a server accepts a manipulated price",
		Tags:        []string{"payment", "business-logic"},
		Intent:      "offensive",
		Body:        "# Price tampering",
	}}
	state := userSkillsTestState(list)
	r := tools.NewRegistry()

	listSkills := makeListSkillsWithState(fs, r, state)
	res, err := listSkills(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "### USER (1 skills)") || !strings.Contains(res.Output, "testing-payment-price-tampering") {
		t.Fatalf("user section missing from list output:\n%s", res.Output)
	}
	// Category filter isolates the user section too.
	res, err = listSkills(map[string]string{"category": UserCategory})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "testing-payment-price-tampering") {
		t.Fatalf("category=user filter lost user skills:\n%s", res.Output)
	}

	search := makeSearchSkills(fs, state)
	res, err = search(map[string]string{"query": "payment price tampering"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "testing-payment-price-tampering  [user]") {
		t.Fatalf("user skill missing from search results:\n%s", res.Output)
	}

	// A bare state renders no USER section.
	res, err = makeListSkillsWithState(fs, tools.NewRegistry(), newAgentSkillState())(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "### USER") {
		t.Fatalf("bare state must not render a USER section:\n%s", res.Output)
	}
}
