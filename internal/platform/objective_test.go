package platform

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestNormalizeObjective(t *testing.T) {
	t.Run("nil stays nil", func(t *testing.T) {
		obj, err := NormalizeObjective(nil)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if obj != nil {
			t.Fatalf("objective = %+v, want nil (not structured)", obj)
		}
	})

	t.Run("assigns criterion ids and trims", func(t *testing.T) {
		obj, err := NormalizeObjective(&Objective{
			Kind:      "  capability ",
			Statement: "  obtain a server shell ",
			Criteria: []Criterion{
				{Kind: "capability", Capability: " shell ", VerifyBy: "deterministic", Required: true},
				{Kind: "finding_class", VulnClass: " command injection ", VerifyBy: "verifier"},
			},
		})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if obj.Kind != ObjectiveKindCapability {
			t.Fatalf("kind = %q", obj.Kind)
		}
		if obj.Statement != "obtain a server shell" {
			t.Fatalf("statement = %q", obj.Statement)
		}
		if obj.Criteria[0].ID != "C1" || obj.Criteria[1].ID != "C2" {
			t.Fatalf("criterion ids = %q/%q, want C1/C2", obj.Criteria[0].ID, obj.Criteria[1].ID)
		}
		if obj.Criteria[0].Capability != "shell" {
			t.Fatalf("capability = %q", obj.Criteria[0].Capability)
		}
	})

	t.Run("rejects invalid input", func(t *testing.T) {
		cases := []struct {
			name string
			obj  *Objective
		}{
			{"missing kind", &Objective{Statement: "x"}},
			{"unknown kind", &Objective{Kind: "pwn", Statement: "x"}},
			{"missing statement", &Objective{Kind: "engagement"}},
			{"criterion without kind", &Objective{Kind: "engagement", Statement: "x", Criteria: []Criterion{{VerifyBy: "verifier"}}}},
			{"criterion without vuln class", &Objective{Kind: "engagement", Statement: "x", Criteria: []Criterion{{Kind: CriterionKindFindingClass, VerifyBy: "verifier"}}}},
			{"criterion without capability", &Objective{Kind: "engagement", Statement: "x", Criteria: []Criterion{{Kind: CriterionKindCapability, VerifyBy: "verifier"}}}},
			{"criterion without verify_by", &Objective{Kind: "engagement", Statement: "x", Criteria: []Criterion{{Kind: CriterionKindCoverage}}}},
			{"unknown verify_by", &Objective{Kind: "engagement", Statement: "x", Criteria: []Criterion{{Kind: CriterionKindCoverage, VerifyBy: "vibes"}}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := NormalizeObjective(tc.obj); !errors.Is(err, ErrValidation) {
					t.Fatalf("err = %v, want ErrValidation", err)
				}
			})
		}
	})

	t.Run("rejects too many criteria", func(t *testing.T) {
		obj := &Objective{Kind: "engagement", Statement: "x"}
		for i := 0; i <= MaxObjectiveCriteria; i++ {
			obj.Criteria = append(obj.Criteria, Criterion{Kind: CriterionKindCoverage, VerifyBy: CriterionVerifyDeterministic})
		}
		if _, err := NormalizeObjective(obj); !errors.Is(err, ErrValidation) {
			t.Fatalf("err = %v, want ErrValidation", err)
		}
	})
}

func TestFileStoreTaskObjectiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestFileStore(t)
	project, err := store.CreateProject(ctx, "Objective Project", Scope{Targets: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}

	// Absent objective = "not structured".
	plain, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Objective != nil {
		t.Fatalf("objective = %+v, want nil", plain.Objective)
	}

	// Structured objective is stored, normalized and returned.
	task, err := store.CreateTask(ctx, project.ID, TaskInput{
		Name: "shell",
		Goal: "get a shell",
		Objective: &Objective{
			Kind:      ObjectiveKindCapability,
			Statement: "obtain a server shell",
			Criteria: []Criterion{
				{Kind: CriterionKindCapability, Capability: "shell", VerifyBy: CriterionVerifyDeterministic, Required: true},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Objective == nil {
		t.Fatal("objective must be persisted")
	}
	if task.Objective.Criteria[0].ID != "C1" {
		t.Fatalf("criterion id = %q, want C1", task.Objective.Criteria[0].ID)
	}

	// Update replaces the objective.
	updated, err := store.UpdateTask(ctx, task.ID, TaskUpdate{
		Objective: &Objective{
			Kind:      ObjectiveKindFindingClass,
			Statement: "prove a command injection",
			Criteria: []Criterion{
				{Kind: CriterionKindFindingClass, VulnClass: "command injection", VerifyBy: CriterionVerifyVerifier},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Objective == nil || updated.Objective.Kind != ObjectiveKindFindingClass {
		t.Fatalf("objective = %+v, want finding_class", updated.Objective)
	}

	// ClearObjective removes it (explicit "objective": null on the wire).
	cleared, err := store.UpdateTask(ctx, task.ID, TaskUpdate{ClearObjective: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Objective != nil {
		t.Fatalf("objective = %+v, want nil after clear", cleared.Objective)
	}

	// An invalid objective is rejected at the store boundary.
	if _, err := store.UpdateTask(ctx, task.ID, TaskUpdate{
		Objective: &Objective{Kind: "nonsense", Statement: "x"},
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}

	// Reopening the store keeps the objective state (file snapshot round trip).
	dataDir := filepath.Dir(filepath.Dir(store.path))
	reopened, err := NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Objective != nil {
		t.Fatalf("objective = %+v, want nil (cleared before reopen)", got.Objective)
	}
}
