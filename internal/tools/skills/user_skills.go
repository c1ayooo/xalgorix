package skills

// user_skills.go — operator-authored skill injection. The embedded corpus is
// read-only (//go:embed); this layer lets one scan carry the platform-
// persisted user skills so list_skills / read_skill / search_skills see
// them alongside the corpus with the fixed category "user".
//
// The set rides on agentSkillState (one per Register call, one registry per
// scan) — deliberately NOT a package global: concurrent scans must not see
// each other's skill sets (charter invariant: per-scan state routes through
// per-scan structures, never package-level mutable globals). Precedence: a
// user skill shadows an embedded skill with the same name — explicit
// operator configuration is the more specific intent.

import (
	"sort"
	"strings"
)

// UserCategory is the fixed category every user-authored skill appears
// under in list/search output.
const UserCategory = "user"

// UserSkill is one operator-authored skill document (platform.Skill without
// its persistence fields). Name is kebab-case (validated upstream).
type UserSkill struct {
	Name        string
	Description string
	Tags        []string
	Intent      string
	Body        string
}

// attachUserSkills computes the derived views (lookup map + search index)
// for one scan's user skill set. Called once at state construction — the
// set is immutable for the state's lifetime, so the derived views need no
// locks of their own.
func attachUserSkills(st *agentSkillState, list []UserSkill) {
	st.userSkills = append([]UserSkill(nil), list...)
	st.userByName = make(map[string]UserSkill, len(st.userSkills))
	for _, s := range st.userSkills {
		st.userByName[s.Name] = s
	}
	st.userIndex = buildUserSkillIndexFor(st.userSkills)
}

// stateUserSkillByName resolves a sanitized skill name against this scan's
// user set (exact match; aliases apply only to the embedded corpus).
func stateUserSkillByName(st *agentSkillState, name string) (UserSkill, bool) {
	if st == nil || name == "" {
		return UserSkill{}, false
	}
	s, ok := st.userByName[name]
	return s, ok
}

// buildUserSkillIndexFor maps user skills onto the shared search record.
func buildUserSkillIndexFor(list []UserSkill) []skillMeta {
	idx := make([]skillMeta, 0, len(list))
	for _, s := range list {
		intent := strings.ToLower(strings.TrimSpace(s.Intent))
		if intent != "defensive" {
			// Autonomous pentest agents search for attack methodology; an
			// unclassified user skill follows the corpus default.
			intent = "offensive"
		}
		haystack := strings.ToLower(strings.Join([]string{
			s.Name, s.Description, strings.Join(s.Tags, " "), UserCategory,
		}, " "))
		idx = append(idx, skillMeta{
			category:    UserCategory,
			name:        s.Name,
			description: s.Description,
			intent:      intent,
			haystack:    haystack,
		})
	}
	sort.Slice(idx, func(i, j int) bool { return idx[i].name < idx[j].name })
	return idx
}

// userSkillBody renders one user skill for read_skill output. Provenance is
// explicit: the model must see that this is operator-authored methodology,
// not corpus content (charter §3.5 — everything the model reads is input to
// verify, and its origin matters).
func userSkillBody(s UserSkill) string {
	var b strings.Builder
	b.WriteString("> User skill (category: user, operator-authored). The sections below are the operator's methodology for this engagement.\n\n")
	b.WriteString(strings.TrimRight(s.Body, "\n"))
	b.WriteString("\n")
	return b.String()
}
