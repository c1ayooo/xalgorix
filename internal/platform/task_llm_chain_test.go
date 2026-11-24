package platform

// task_llm_chain_test.go — task-level model chain (4zreco task_llm_profiles
// port): ordered "<provider>:<profileId>" entries with the active head used by
// the next run. Stored in the task context; order is priority.

import (
	"context"
	"testing"
)

func TestNormalizeTaskLLMChain(t *testing.T) {
	chain, active := NormalizeTaskLLMChain(
		[]string{" openai:p1 ", "anthropic:p2", "openai:p1", "", "bad-key"},
		"anthropic:p2",
	)
	if len(chain) != 2 || chain[0] != "openai:p1" || chain[1] != "anthropic:p2" {
		t.Fatalf("chain = %#v", chain)
	}
	if active != "anthropic:p2" {
		t.Fatalf("active = %q", active)
	}
	// Active outside the chain falls back to the head.
	if _, active := NormalizeTaskLLMChain([]string{"openai:p1", "anthropic:p2"}, "openai:missing"); active != "openai:p1" {
		t.Fatalf("out-of-chain active did not fall back: %q", active)
	}
	// Empty chain clears both.
	if chain, active := NormalizeTaskLLMChain(nil, "openai:p1"); chain != nil || active != "" {
		t.Fatalf("empty chain must clear: %#v %q", chain, active)
	}
}

func TestTaskLLMChainPersistenceAndPriority(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}

	task, err := store.CreateTask(ctx, project.ID, TaskInput{
		Name:      "chained",
		Goal:      "g",
		LLMChain:  []string{"openai:p1", "anthropic:p2"},
		ActiveLLM: "anthropic:p2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.LLMChain) != 2 || task.ActiveLLM != "anthropic:p2" {
		t.Fatalf("created chain = %#v active=%q", task.LLMChain, task.ActiveLLM)
	}

	// Reorder (priority switch): anthropic first, then openai.
	reordered := []string{"anthropic:p2", "openai:p1"}
	updated, err := store.UpdateTask(ctx, task.ID, TaskUpdate{LLMChain: &reordered})
	if err != nil {
		t.Fatal(err)
	}
	if updated.LLMChain[0] != "anthropic:p2" || updated.ActiveLLM != "anthropic:p2" {
		t.Fatalf("update lost the order/active: %#v %q", updated.LLMChain, updated.ActiveLLM)
	}

	// Reload: persisted in the task context, hydrated on read.
	reloaded, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LLMChain) != 2 || got.LLMChain[0] != "anthropic:p2" || got.ActiveLLM != "anthropic:p2" {
		t.Fatalf("reloaded chain = %#v active=%q", got.LLMChain, got.ActiveLLM)
	}

	// Clearing the chain removes both keys from the context.
	empty := []string{}
	cleared, err := store.UpdateTask(ctx, task.ID, TaskUpdate{LLMChain: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.LLMChain) != 0 || cleared.ActiveLLM != "" {
		t.Fatalf("clear left residue: %#v %q", cleared.LLMChain, cleared.ActiveLLM)
	}
	if _, ok := cleared.Context["llm_chain"]; ok {
		t.Fatal("context still carries llm_chain after clear")
	}
}
