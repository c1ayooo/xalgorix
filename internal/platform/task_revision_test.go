package platform

import "testing"

func TestTaskContentRevisionTracksRunnableFieldsOnly(t *testing.T) {
	base := Task{
		ID:           "t1",
		Name:         "Edge review",
		Goal:         "Assess the edge",
		Instruction:  "Focus auth",
		PhasePolicy:  &PhasePolicy{ScanMode: "wildcard", SeverityFilter: []string{"critical"}},
		Schedule:     &TaskSchedule{Interval: "daily", Time: "08:00"},
		ContextPaths: []string{"notes/a.md"},
		Objective: &Objective{
			Kind:      "vuln_class",
			Statement: "find auth issues",
			Criteria:  []Criterion{{ID: "C1", Kind: "vuln_class", VulnClass: "auth", VerifyBy: "finding"}},
		},
		LLMChain:  []string{"openai:main"},
		ActiveLLM: "openai:main",
	}
	rev := TaskContentRevision(base)
	if rev == "" {
		t.Fatal("TaskContentRevision returned an empty digest")
	}
	if again := TaskContentRevision(base); again != rev {
		t.Fatalf("revision is not stable: %q != %q", rev, again)
	}

	// Non-content fields must NOT move the revision: renaming, retiming the
	// schedule, archiving, or flipping the HITL switch are not "new work".
	for label, mutate := range map[string]func(*Task){
		"name":       func(tk *Task) { tk.Name = "Renamed" },
		"schedule":   func(tk *Task) { tk.Schedule = &TaskSchedule{Interval: "weekly"} },
		"audit_mode": func(tk *Task) { tk.AuditMode = "enforce" },
		"updated_at": func(tk *Task) { now := tk.CreatedAt; tk.UpdatedAt = &now },
	} {
		mutated := base
		mutate(&mutated)
		if got := TaskContentRevision(mutated); got != rev {
			t.Errorf("%s change moved the revision (%q != %q)", label, got, rev)
		}
	}

	// Runnable fields MUST move it.
	for label, mutate := range map[string]func(*Task){
		"goal":          func(tk *Task) { tk.Goal = "Assess the admin edge" },
		"instruction":   func(tk *Task) { tk.Instruction = "Focus SSO" },
		"phase_policy":  func(tk *Task) { tk.PhasePolicy = &PhasePolicy{ScanMode: "single"} },
		"context_paths": func(tk *Task) { tk.ContextPaths = []string{"notes/b.md"} },
		"objective":     func(tk *Task) { tk.Objective = nil },
		"llm_chain":     func(tk *Task) { tk.LLMChain = []string{"anthropic:main"} },
		"active_llm":    func(tk *Task) { tk.ActiveLLM = "anthropic:main" },
	} {
		mutated := base
		mutate(&mutated)
		if got := TaskContentRevision(mutated); got == rev {
			t.Errorf("%s change did not move the revision", label)
		}
	}

	// A no-op edit (same text) keeps the same revision — content, not time.
	noop := base
	noop.Goal = "  Assess the edge  "
	if got := TaskContentRevision(noop); got != rev {
		t.Errorf("whitespace-only edit moved the revision")
	}
}

func TestDispatchedRunRevision(t *testing.T) {
	if got := DispatchedRunRevision(Task{}); got != "" {
		t.Fatalf("nil context revision = %q, want empty", got)
	}
	task := Task{Context: map[string]any{TaskRunRevisionKey: "abc123"}}
	if got := DispatchedRunRevision(task); got != "abc123" {
		t.Fatalf("revision = %q, want abc123", got)
	}
	// Non-string junk (hand-edited JSON) reads as unrecorded, never a panic.
	task.Context[TaskRunRevisionKey] = 42
	if got := DispatchedRunRevision(task); got != "" {
		t.Fatalf("non-string revision = %q, want empty", got)
	}
}
