package web

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func TestTaskConfigDiffTracksRunnableFieldsOnly(t *testing.T) {
	base := &platform.Task{
		ID:          "t1",
		Name:        "edge",
		Goal:        "assess the edge",
		Instruction: "focus auth",
		AuditMode:   "inherit",
		LLMChain:    []string{"openai:a"},
		ActiveLLM:   "openai:a",
		Proxy:       "1",
	}
	if diff := taskConfigDiff(base, base); len(diff) != 0 {
		t.Fatalf("identical tasks diffed: %v", diff)
	}

	cases := map[string]func(*platform.Task){
		"goal":          func(tk *platform.Task) { tk.Goal = "assess admin" },
		"instruction":   func(tk *platform.Task) { tk.Instruction = "focus SSO" },
		"objective":     func(tk *platform.Task) { tk.Objective = &platform.Objective{Statement: "x"} },
		"phase_policy":  func(tk *platform.Task) { tk.PhasePolicy = &platform.PhasePolicy{ScanMode: "single"} },
		"context_paths": func(tk *platform.Task) { tk.ContextPaths = []string{"a.md"} },
		"llm_chain":     func(tk *platform.Task) { tk.LLMChain = []string{"openai:b"} },
		"active_llm":    func(tk *platform.Task) { tk.ActiveLLM = "openai:b" },
		"proxy":         func(tk *platform.Task) { tk.Proxy = "direct" },
		"audit_mode":    func(tk *platform.Task) { tk.AuditMode = "enforce" },
	}
	for field, mutate := range cases {
		next := *base
		mutate(&next)
		diff := taskConfigDiff(base, &next)
		found := false
		for _, f := range diff {
			if f == field {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s change not detected: %v", field, diff)
		}
	}

	// Name/schedule are not run-relevant.
	renamed := *base
	renamed.Name = "renamed"
	renamed.Schedule = &platform.TaskSchedule{Interval: "daily"}
	if diff := taskConfigDiff(base, &renamed); len(diff) != 0 {
		t.Fatalf("name/schedule must not diff: %v", diff)
	}
}

func TestTaskConfigUpdateSummaryNamesChangedFields(t *testing.T) {
	oldTask := &platform.Task{ID: "t1", Goal: "old goal", Proxy: "1"}
	newTask := &platform.Task{ID: "t1", Goal: "new goal", Proxy: "direct"}
	summary := taskConfigUpdateSummary([]string{"goal", "proxy"}, oldTask, newTask)
	for _, want := range []string{"goal", "proxy", "new goal", "direct"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}
