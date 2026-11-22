package agent

import "testing"

// TestObjectiveClassRefUsesCanonicalClasses pins the matching rule: the
// operator writes natural language ("command injection"), the engine works
// with canonical class IDs (cmdi).
func TestObjectiveClassRefUsesCanonicalClasses(t *testing.T) {
	obj := &ObjectiveSpec{Criteria: []ObjectiveCriterion{
		{ID: "C1", Kind: "finding_class", VulnClass: "command injection"},
		{ID: "C2", Kind: "finding_class", VulnClass: "SQL injection"},
	}}
	cases := []struct {
		class string
		want  string
	}{
		{"cmdi", "C1"},
		{"sqli", "C2"},
		{"idor", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := objectiveClassRef(obj, tc.class); got != tc.want {
			t.Errorf("objectiveClassRef(%q) = %q, want %q", tc.class, got, tc.want)
		}
	}
}

// TestObjectiveClassRefDoesNotOverMatch makes sure a broad criterion does
// not capture a neighboring class ("xss" must not capture "dom-xss").
func TestObjectiveClassRefDoesNotOverMatch(t *testing.T) {
	obj := &ObjectiveSpec{Criteria: []ObjectiveCriterion{
		{ID: "C1", Kind: "finding_class", VulnClass: "xss"},
	}}
	if got := objectiveClassRef(obj, "dom-xss"); got != "" {
		t.Fatalf("objectiveClassRef(dom-xss) = %q, want \"\"", got)
	}
	if got := objectiveClassRef(obj, "xss"); got != "C1" {
		t.Fatalf("objectiveClassRef(xss) = %q, want C1", got)
	}
}

// TestObjectiveNonFindingClassCriteriaAreIgnored: capability/coverage
// criteria are carried to the model through the prompt section, never
// mapped onto a class by guesswork.
func TestObjectiveNonFindingClassCriteriaAreIgnored(t *testing.T) {
	obj := &ObjectiveSpec{Criteria: []ObjectiveCriterion{
		{ID: "C1", Kind: "capability", Capability: "shell"},
	}}
	if got := objectiveClassRef(obj, "cmdi"); got != "" {
		t.Fatalf("objectiveClassRef(capability) = %q, want \"\"", got)
	}
}

// TestApplyObjectiveRefsOnlyMarks pins the contract: marking only, never
// adding, removing or reordering tasks.
func TestApplyObjectiveRefsOnlyMarks(t *testing.T) {
	p := NewPlan()
	p.add(&Task{ID: "t1", Title: "recon", Phase: 1})
	p.add(&Task{ID: "t2", Title: "sql injection", Phase: 6, VulnClass: "sqli"})
	p.add(&Task{ID: "t3", Title: "command injection", Phase: 6, VulnClass: "cmdi"})
	before := len(p.Tasks)
	order := []string{p.Tasks[0].ID, p.Tasks[1].ID, p.Tasks[2].ID}

	applyObjectiveRefs(p, &ObjectiveSpec{Criteria: []ObjectiveCriterion{
		{ID: "C1", Kind: "finding_class", VulnClass: "command injection"},
	}})
	if len(p.Tasks) != before {
		t.Fatalf("task count changed: %d -> %d", before, len(p.Tasks))
	}
	for i, id := range order {
		if p.Tasks[i].ID != id {
			t.Fatalf("task order changed at %d: %q != %q", i, p.Tasks[i].ID, id)
		}
	}
	if p.Get("t3").ObjectiveRef != "C1" {
		t.Fatalf("cmdi task ref = %q, want C1", p.Get("t3").ObjectiveRef)
	}
	if p.Get("t2").ObjectiveRef != "" {
		t.Fatalf("sqli task ref = %q, want \"\"", p.Get("t2").ObjectiveRef)
	}
}

// TestApplyObjectiveRefsIsNoOpWithoutObjective keeps the golden scheduling
// parity: without an objective the plan is untouched.
func TestApplyObjectiveRefsIsNoOpWithoutObjective(t *testing.T) {
	p := NewPlan()
	p.add(&Task{ID: "t1", Title: "recon", Phase: 1})
	applyObjectiveRefs(p, nil)
	applyObjectiveRefs(nil, &ObjectiveSpec{})
	if p.Get("t1").ObjectiveRef != "" {
		t.Fatalf("objective ref = %q, want \"\"", p.Get("t1").ObjectiveRef)
	}
}
