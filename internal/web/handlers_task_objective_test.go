package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// createTaskWithBody POSTs a task and returns the decoded task.
func createTaskWithBody(t *testing.T, s *Server, projectID string, body string) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", strings.NewReader(body))
	s.handleProjectTasks(rr, req, s.currentPlatformStore(), projectID)
	var out map[string]any
	if rr.Body.Len() > 0 {
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
	}
	return rr.Code, out
}

// TestTaskObjectiveLifecycle covers the structured objective contract: create
// with an objective, read it back normalized, evaluate it, and clear it.
func TestTaskObjectiveLifecycle(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Objective Project", platform.Scope{Targets: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}

	// A task without an objective is "not structured" — never inferred.
	code, task := createTaskWithBody(t, s, project.ID, `{"name":"plain","goal":"g"}`)
	if code != http.StatusCreated {
		t.Fatalf("create code = %d, want 201; body=%v", code, task)
	}
	plainID, _ := task["id"].(string)
	if obj, ok := task["objective"]; ok && obj != nil {
		t.Fatalf("objective = %v, want absent", obj)
	}

	rr := httptest.NewRecorder()
	s.handleTaskObjective(rr, httptest.NewRequest(http.MethodGet, "/api/tasks/"+plainID+"/objective", nil), store, plainID)
	if rr.Code != http.StatusOK {
		t.Fatalf("objective code = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if structured, _ := view["structured"].(bool); structured {
		t.Fatalf("structured = true, want false for a task without an objective")
	}

	// Create with a structured objective: the server assigns criterion IDs.
	code, task = createTaskWithBody(t, s, project.ID, `{"name":"shell","goal":"g","objective":{"kind":"capability","statement":"obtain a server shell","criteria":[{"kind":"capability","capability":"shell","verify_by":"deterministic","required":true}]}}`)
	if code != http.StatusCreated {
		t.Fatalf("create code = %d, want 201; body=%v", code, task)
	}
	obj, _ := task["objective"].(map[string]any)
	if obj == nil {
		t.Fatal("objective must be persisted and returned")
	}
	criteria, _ := obj["criteria"].([]any)
	if len(criteria) != 1 {
		t.Fatalf("criteria = %v, want 1", criteria)
	}
	first, _ := criteria[0].(map[string]any)
	if first["id"] != "C1" {
		t.Fatalf("criterion id = %v, want C1", first["id"])
	}

	structuredID, _ := task["id"].(string)
	rr = httptest.NewRecorder()
	s.handleTaskObjective(rr, httptest.NewRequest(http.MethodGet, "/api/tasks/"+structuredID+"/objective", nil), store, structuredID)
	if rr.Code != http.StatusOK {
		t.Fatalf("objective code = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if structured, _ := view["structured"].(bool); !structured {
		t.Fatalf("structured = false, want true; body=%s", rr.Body.String())
	}
	report, _ := view["report"].(map[string]any)
	// No findings yet: the capability criterion is undetermined, not met.
	if state := report["undetermined"]; state != float64(1) {
		t.Fatalf("undetermined = %v, want 1; report=%v", state, report)
	}
	if met := report["met"]; met != float64(0) {
		t.Fatalf("met = %v, want 0", met)
	}
	if ring, _ := view["ring"].(string); ring != "unknown" {
		t.Fatalf("ring = %q, want unknown (no run recorded)", ring)
	}

	// PATCH with an explicit null clears the objective.
	rr = httptest.NewRecorder()
	s.handleTaskDetail(rr, httptest.NewRequest(http.MethodPatch, "/api/tasks/"+structuredID, strings.NewReader(`{"objective":null}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch code = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var patched map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if obj, ok := patched["objective"]; ok && obj != nil {
		t.Fatalf("objective = %v, want cleared", obj)
	}
}

// TestTaskObjectiveValidationRejectsBadInput keeps invalid objectives out of
// the store (400 with a readable reason, never a silent drop).
func TestTaskObjectiveValidationRejectsBadInput(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	project, err := store.CreateProject(context.Background(), "Objective Validation", platform.Scope{Targets: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		body string
	}{
		{"unknown kind", `{"name":"x","goal":"g","objective":{"kind":"pwn","statement":"s"}}`},
		{"missing statement", `{"name":"x","goal":"g","objective":{"kind":"engagement"}}`},
		{"criterion without class", `{"name":"x","goal":"g","objective":{"kind":"finding_class","statement":"s","criteria":[{"kind":"finding_class","verify_by":"verifier"}]}}`},
		{"criterion without verify_by", `{"name":"x","goal":"g","objective":{"kind":"coverage","statement":"s","criteria":[{"kind":"coverage"}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := createTaskWithBody(t, s, project.ID, tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400; body=%v", code, body)
			}
		})
	}
}

// TestBuildObjectiveInstruction pins the prompt contract: the objective is an
// independent section that states it is intent, not authorization.
func TestBuildObjectiveInstruction(t *testing.T) {
	if got := buildObjectiveInstruction(nil); got != "" {
		t.Fatalf("instruction = %q, want empty without an objective", got)
	}
	section := buildObjectiveInstruction(&platform.Objective{
		Kind:      platform.ObjectiveKindCapability,
		Statement: "obtain a server shell",
		Criteria: []platform.Criterion{
			{ID: "C1", Kind: platform.CriterionKindCapability, Capability: "shell", VerifyBy: platform.CriterionVerifyDeterministic},
		},
	})
	for _, want := range []string{
		"## OBJECTIVE (operator-stated)",
		"obtain a server shell",
		"capability: shell",
		"report_vulnerability",
		"never widen the authorized scope",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("objective section missing %q:\n%s", want, section)
		}
	}
}
