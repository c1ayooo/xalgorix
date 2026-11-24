package platform

import (
	"context"
	"errors"
	"testing"
)

// The task audit switch must round-trip through the store and survive a
// reload; an unknown value is rejected instead of silently changing the gate.
func TestTaskAuditModePersistence(t *testing.T) {
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

	task, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "audited", Goal: "g", AuditMode: TaskAuditEnforce})
	if err != nil {
		t.Fatal(err)
	}
	if task.AuditMode != TaskAuditEnforce {
		t.Fatalf("created audit mode = %q", task.AuditMode)
	}

	// Reload from disk: the value is persisted, not just in memory.
	reloaded, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.GetTask(ctx, task.ID)
	if err != nil || got.AuditMode != TaskAuditEnforce {
		t.Fatalf("reloaded audit mode = %q err=%v", got.AuditMode, err)
	}

	off := TaskAuditOff
	if _, err := reloaded.UpdateTask(ctx, task.ID, TaskUpdate{AuditMode: &off}); err != nil {
		t.Fatal(err)
	}
	got, _ = reloaded.GetTask(ctx, task.ID)
	if got.AuditMode != TaskAuditOff {
		t.Fatalf("updated audit mode = %q", got.AuditMode)
	}

	bad := "sometimes"
	if _, err := reloaded.UpdateTask(ctx, task.ID, TaskUpdate{AuditMode: &bad}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid update err = %v, want ErrValidation", err)
	}
	if _, err := reloaded.CreateTask(ctx, project.ID, TaskInput{Name: "bad", Goal: "g", AuditMode: "sometimes"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid create err = %v, want ErrValidation", err)
	}

	// Empty = inherit (the pre-feature posture).
	plain, err := reloaded.CreateTask(ctx, project.ID, TaskInput{Name: "plain", Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.AuditMode != TaskAuditInherit {
		t.Fatalf("default audit mode = %q, want inherit", plain.AuditMode)
	}
}

func TestNormalizeTaskAuditMode(t *testing.T) {
	cases := map[string]string{
		"":             TaskAuditInherit,
		"inherit":      TaskAuditInherit,
		"global":       TaskAuditInherit,
		"off":          TaskAuditOff,
		"false":        TaskAuditOff,
		"observe-only": TaskAuditOff,
		"enforce":      TaskAuditEnforce,
		"true":         TaskAuditEnforce,
		"hitl":         TaskAuditEnforce,
		"wat":          "",
	}
	for raw, want := range cases {
		if got := NormalizeTaskAuditMode(raw); got != want {
			t.Errorf("NormalizeTaskAuditMode(%q) = %q, want %q", raw, got, want)
		}
	}
}
