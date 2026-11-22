package agent

// objective.go — the operator-stated objective as the engine sees it.
//
// The engine keeps its own view of the objective (no dependency on the
// platform package): the operator's statement plus the criteria that name a
// vulnerability class. The objective is INTENT for the loop — it never changes
// what counts as a finding and never relaxes the methodology phases (charter
// §9). Its one engine-side effect is on CLOSING (R-goal, goal_gate.go): while
// a required, engine-decidable criterion is unmet, the root agent may not
// voluntarily finish (hookGoalFinishGate) and the plan-complete tail defers
// into a goal obligation (liveness.go) — bounded so termination stays
// guaranteed. The evaluator itself remains read-only over findings the
// report pipeline already accepted.

import "strings"

// ObjectiveSpec is the engine-side view of a task objective.
type ObjectiveSpec struct {
	Statement string
	Criteria  []ObjectiveCriterion
}

// ObjectiveCriterion is one acceptance line the engine can relate to plan
// work. Only finding-class criteria carry engine meaning here: a capability
// or coverage criterion is carried to the model through the prompt section,
// not turned into a class mapping by guesswork.
type ObjectiveCriterion struct {
	ID         string
	Kind       string
	VulnClass  string
	Capability string
	VerifyBy   string
	Required   bool
}

// objectiveCriterionKindFindingClass mirrors platform.CriterionKindFindingClass
// without importing the platform package.
const objectiveCriterionKindFindingClass = "finding_class"

// SetObjective attaches the operator's objective to the agent. It is called
// once per session before the loop starts; a nil spec clears it. The spec is
// copied so later mutations by the caller cannot change a running scan.
func (a *Agent) SetObjective(spec *ObjectiveSpec) {
	if a == nil {
		return
	}
	if spec == nil {
		a.objective = nil
		if a.state != nil {
			a.state.Objective = nil
		}
		return
	}
	cp := &ObjectiveSpec{Statement: spec.Statement}
	cp.Criteria = append(cp.Criteria, spec.Criteria...)
	a.objective = cp
	if a.state != nil {
		a.state.Objective = cp
	}
}

// Objective returns the attached objective spec (nil when the task has none).
func (a *Agent) Objective() *ObjectiveSpec {
	if a == nil {
		return nil
	}
	return a.objective
}

// objectiveClassRef returns the ID of the first finding-class criterion that
// matches the given vulnerability class, or "" when nothing matches. The
// match is token-based and conservative: a criterion must not capture
// unrelated classes.
func objectiveClassRef(obj *ObjectiveSpec, vulnClass string) string {
	if obj == nil {
		return ""
	}
	class := strings.ToLower(strings.TrimSpace(vulnClass))
	if class == "" {
		return ""
	}
	for _, c := range obj.Criteria {
		if c.Kind != objectiveCriterionKindFindingClass {
			continue
		}
		want := strings.ToLower(strings.TrimSpace(c.VulnClass))
		if want == "" {
			continue
		}
		if objectiveClassMatches(want, class) {
			return c.ID
		}
	}
	return ""
}

// objectiveClassMatches compares a criterion's class name with a plan task's
// class. Canonical resolution comes first ("command injection" -> cmdi), so
// the operator's natural language reaches the engine's class IDs; the token
// overlap is only a fallback for names the registry does not know. When both
// sides resolve, they must be the SAME class: "xss" must not capture
// "dom-xss".
func objectiveClassMatches(criterion, class string) bool {
	critCanonical := CanonicalVulnClassID(normalizeClassName(criterion))
	classCanonical := CanonicalVulnClassID(normalizeClassName(class))
	if critCanonical != "" && classCanonical != "" {
		return critCanonical == classCanonical
	}
	if critCanonical != "" && critCanonical == class {
		return true
	}
	return classTokensOverlap(criterion, class)
}

// normalizeClassName folds a free-form class name into the registry's alias
// spelling (lowercase, hyphen separated).
func normalizeClassName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// classTokensOverlap reports whether two vulnerability-class names share at
// least one meaningful token.
func classTokensOverlap(a, b string) bool {
	tokens := map[string]bool{}
	for _, tok := range splitClassTokens(a) {
		tokens[tok] = true
	}
	for _, tok := range splitClassTokens(b) {
		if tokens[tok] {
			return true
		}
	}
	return false
}

// splitClassTokens splits a class name into lowercase alphanumeric tokens.
func splitClassTokens(s string) []string {
	parts := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
