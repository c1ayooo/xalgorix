package web

// Handler-level tests for the goal intent analysis endpoint (REDESIGN §3.1,
// R-goal): POST /api/projects/{id}/analyze-goal is a pure preview — one LLM
// call, no persistence — with the project scope shown to the analyzer as the
// authorization boundary. The LLM function is injected so tests never touch a
// real provider.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func newAnalyzeGoalTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := newPlatformTestServer(t)
	rr := doJSON(t, s, "POST", "/api/projects", `{"name":"goal-analysis","scope":{"targets":["https://acme.com","10.0.0.0/24"]}}`)
	if rr.Code < 200 || rr.Code >= 300 {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	id, _ := decodeBody(t, rr)["id"].(string)
	if id == "" {
		t.Fatal("project id missing")
	}
	return s, id
}

func TestAnalyzeGoalEndpointReturnsProposal(t *testing.T) {
	s, projectID := newAnalyzeGoalTestServer(t)

	reply := `{"statement":"Obtain verified SQL injection on acme.com","criteria":[{"kind":"finding_class","vuln_class":"sqli","rationale":"goal names injection"}],"constraints":["Only test https://acme.com"],"notes":"rewrote"}`
	var gotSystem, gotUser string
	s.goalAnalyzeLLM = func(_ context.Context, systemPrompt, userMessage string) (string, error) {
		gotSystem, gotUser = systemPrompt, userMessage
		return reply, nil
	}

	rr := doJSON(t, s, "POST", "/api/projects/"+projectID+"/analyze-goal",
		`{"goal":"看看 acme.com 有没有注入","instruction":"优先测登录口"}`)
	if rr.Code != 200 {
		t.Fatalf("analyze-goal: %d %s", rr.Code, rr.Body.String())
	}
	body := decodeBody(t, rr)
	proposal, _ := body["proposal"].(map[string]any)
	if proposal == nil {
		t.Fatalf("proposal missing: %s", rr.Body.String())
	}
	if proposal["statement"] != "Obtain verified SQL injection on acme.com" {
		t.Errorf("statement = %v", proposal["statement"])
	}
	criteria, _ := proposal["criteria"].([]any)
	if len(criteria) != 1 {
		t.Fatalf("criteria = %v", proposal["criteria"])
	}
	c0, _ := criteria[0].(map[string]any)
	if c0["vuln_class"] != "sqli" || c0["required"] != true {
		t.Errorf("criterion 0 = %v (verify_by/required defaults must be applied)", c0)
	}
	if c0["verify_by"] != "verifier" {
		t.Errorf("verify_by default = %v, want verifier", c0["verify_by"])
	}
	if cons, _ := proposal["constraints"].([]any); len(cons) != 1 || cons[0] != "Only test https://acme.com" {
		t.Errorf("constraints = %v", proposal["constraints"])
	}

	// The analyzer must see the goal, the notes and the authorized scope.
	if !strings.Contains(gotUser, "看看 acme.com 有没有注入") {
		t.Errorf("user prompt misses goal: %q", gotUser)
	}
	if !strings.Contains(gotUser, "优先测登录口") {
		t.Errorf("user prompt misses instruction: %q", gotUser)
	}
	if !strings.Contains(gotUser, "https://acme.com") || !strings.Contains(gotUser, "10.0.0.0/24") {
		t.Errorf("user prompt misses project scope: %q", gotUser)
	}
	if gotSystem == "" {
		t.Error("system prompt empty")
	}
}

func TestAnalyzeGoalEndpointErrors(t *testing.T) {
	t.Run("empty goal is a 400 without an LLM call", func(t *testing.T) {
		s, projectID := newAnalyzeGoalTestServer(t)
		called := false
		s.goalAnalyzeLLM = func(context.Context, string, string) (string, error) { called = true; return "{}", nil }
		rr := doJSON(t, s, "POST", "/api/projects/"+projectID+"/analyze-goal", `{"goal":"   "}`)
		if rr.Code != 400 {
			t.Errorf("code = %d, want 400", rr.Code)
		}
		if called {
			t.Error("LLM called for empty goal")
		}
	})

	t.Run("LLM failure is a 502", func(t *testing.T) {
		s, projectID := newAnalyzeGoalTestServer(t)
		s.goalAnalyzeLLM = func(context.Context, string, string) (string, error) {
			return "", errors.New("provider down")
		}
		rr := doJSON(t, s, "POST", "/api/projects/"+projectID+"/analyze-goal", `{"goal":"g"}`)
		if rr.Code != 502 {
			t.Errorf("code = %d, want 502", rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "provider down") {
			t.Errorf("error body hides the cause: %s", rr.Body.String())
		}
	})

	t.Run("unparseable reply is a 502", func(t *testing.T) {
		s, projectID := newAnalyzeGoalTestServer(t)
		s.goalAnalyzeLLM = func(context.Context, string, string) (string, error) {
			return "I cannot analyze that.", nil
		}
		rr := doJSON(t, s, "POST", "/api/projects/"+projectID+"/analyze-goal", `{"goal":"g"}`)
		if rr.Code != 502 {
			t.Errorf("code = %d, want 502", rr.Code)
		}
	})

	t.Run("unknown project is a 404", func(t *testing.T) {
		s, _ := newAnalyzeGoalTestServer(t)
		s.goalAnalyzeLLM = func(context.Context, string, string) (string, error) { return "{}", nil }
		rr := doJSON(t, s, "POST", "/api/projects/nope/analyze-goal", `{"goal":"g"}`)
		if rr.Code != 404 {
			t.Errorf("code = %d, want 404", rr.Code)
		}
	})

	t.Run("GET is a 405", func(t *testing.T) {
		s, projectID := newAnalyzeGoalTestServer(t)
		rr := doJSON(t, s, "GET", "/api/projects/"+projectID+"/analyze-goal", "")
		if rr.Code != 405 {
			t.Errorf("code = %d, want 405", rr.Code)
		}
	})
}

// TestAnalyzeGoalNilLLMIs503: a server built without the default LLM wiring
// (and without an injected fake) must say so explicitly, not panic.
func TestAnalyzeGoalNilLLMIs503(t *testing.T) {
	s, projectID := newAnalyzeGoalTestServer(t)
	s.goalAnalyzeLLM = nil
	rr := doJSON(t, s, "POST", "/api/projects/"+projectID+"/analyze-goal", `{"goal":"g"}`)
	if rr.Code != 503 {
		t.Errorf("code = %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "unavailable") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

// ── round-0 auto analysis (R-goal, goal_analysis.go roundZeroGoalAnalysis) ──

func newRoundZeroTestServer(t *testing.T, goalAnalysis bool) (*Server, platform.Repository, string, string) {
	t.Helper()
	s := newTestServer(t, &config.Config{GoalAnalysis: goalAnalysis})
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	rr := doJSON(t, s, "POST", "/api/projects", `{"name":"round-zero","scope":{"targets":["https://acme.com"]}}`)
	if rr.Code < 200 || rr.Code >= 300 {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projectID, _ := decodeBody(t, rr)["id"].(string)
	rr = doJSON(t, s, "POST", "/api/projects/"+projectID+"/tasks",
		`{"name":"t","goal":"看看 acme.com 有没有问题","instruction":"优先测登录口"}`)
	if rr.Code < 200 || rr.Code >= 300 {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	taskID, _ := decodeBody(t, rr)["id"].(string)
	return s, store, projectID, taskID
}

func TestRoundZeroGoalAnalysisAnalyzesAndPersists(t *testing.T) {
	s, store, projectID, taskID := newRoundZeroTestServer(t, true)

	called := false
	s.goalAnalyzeLLM = func(_ context.Context, _, userMessage string) (string, error) {
		called = true
		if !strings.Contains(userMessage, "看看 acme.com 有没有问题") {
			t.Errorf("analyzer prompt misses the raw goal: %q", userMessage)
		}
		return `{"statement":"Deliver one verified finding on acme.com","criteria":[{"kind":"finding_class","vuln_class":"sqli"}],"constraints":["Only test https://acme.com"],"notes":"rewrote"}`, nil
	}

	req := &ScanRequest{
		ObjectiveProjectID:   projectID,
		ObjectiveTaskID:      taskID,
		ObjectiveGoal:        "看看 acme.com 有没有问题",
		ObjectiveInstruction: "优先测登录口",
	}
	s.roundZeroGoalAnalysis(req)
	if req.Objective == nil {
		t.Fatal("round-0 left the run without an objective")
	}
	if len(req.Objective.Criteria) != 1 || req.Objective.Criteria[0].VulnClass != "sqli" {
		t.Errorf("objective = %+v", req.Objective)
	}
	if len(req.Objective.Constraints) != 1 {
		t.Errorf("constraints dropped: %+v", req.Objective)
	}
	if !called {
		t.Error("analyzer was not called")
	}
	persisted, err := store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Objective == nil || len(persisted.Objective.Criteria) != 1 || persisted.Objective.Criteria[0].ID != "C1" {
		t.Errorf("persisted objective = %+v, want normalized C1", persisted.Objective)
	}
}

func TestRoundZeroGoalAnalysisAdoptsExistingObjective(t *testing.T) {
	s, store, projectID, _ := newRoundZeroTestServer(t, true)
	rr := doJSON(t, s, "POST", "/api/projects/"+projectID+"/tasks", `{
		"name":"structured",
		"goal":"raw goal text",
		"objective":{"kind":"engagement","statement":"operator wrote this","criteria":[{"kind":"coverage","verify_by":"verifier","required":true}]}}`)
	if rr.Code < 200 || rr.Code >= 300 {
		t.Fatalf("create structured task: %d %s", rr.Code, rr.Body.String())
	}
	taskID, _ := decodeBody(t, rr)["id"].(string)

	called := false
	s.goalAnalyzeLLM = func(context.Context, string, string) (string, error) {
		called = true
		return `{"statement":"invented","criteria":[{"kind":"coverage","verify_by":"verifier"}]}`, nil
	}
	req := &ScanRequest{ObjectiveProjectID: projectID, ObjectiveTaskID: taskID, ObjectiveGoal: "raw goal text"}
	s.roundZeroGoalAnalysis(req)
	if called {
		t.Error("analyzer ran even though the task already had a structured objective")
	}
	if req.Objective == nil || req.Objective.Statement != "operator wrote this" {
		t.Errorf("objective = %+v, want the operator's own objective adopted", req.Objective)
	}
	persisted, err := store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Objective == nil || persisted.Objective.Statement != "operator wrote this" {
		t.Errorf("persisted objective = %+v", persisted.Objective)
	}
}

func TestRoundZeroGoalAnalysisFailOpen(t *testing.T) {
	s, store, projectID, taskID := newRoundZeroTestServer(t, true)
	s.goalAnalyzeLLM = func(context.Context, string, string) (string, error) {
		return "", errors.New("provider down")
	}
	req := &ScanRequest{ObjectiveProjectID: projectID, ObjectiveTaskID: taskID, ObjectiveGoal: "g"}
	s.roundZeroGoalAnalysis(req)
	if req.Objective != nil {
		t.Errorf("objective = %+v, want nil on analyzer failure", req.Objective)
	}
	persisted, err := store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Objective != nil {
		t.Errorf("failed analysis persisted an objective: %+v", persisted.Objective)
	}
}

func TestRoundZeroGoalAnalysisNoOps(t *testing.T) {
	t.Run("disabled config", func(t *testing.T) {
		s, _, projectID, taskID := newRoundZeroTestServer(t, false)
		called := false
		s.goalAnalyzeLLM = func(context.Context, string, string) (string, error) { called = true; return "{}", nil }
		req := &ScanRequest{ObjectiveProjectID: projectID, ObjectiveTaskID: taskID, ObjectiveGoal: "g"}
		s.roundZeroGoalAnalysis(req)
		if called || req.Objective != nil {
			t.Errorf("called=%v objective=%+v, want a no-op under disabled config", called, req.Objective)
		}
	})
	t.Run("legacy request without task coordinates", func(t *testing.T) {
		s, _, _, _ := newRoundZeroTestServer(t, true)
		called := false
		s.goalAnalyzeLLM = func(context.Context, string, string) (string, error) { called = true; return "{}", nil }
		req := &ScanRequest{ObjectiveGoal: "g"}
		s.roundZeroGoalAnalysis(req)
		if called {
			t.Error("analyzer ran for a legacy request")
		}
	})
	t.Run("run that already carries an objective", func(t *testing.T) {
		s, _, projectID, taskID := newRoundZeroTestServer(t, true)
		called := false
		s.goalAnalyzeLLM = func(context.Context, string, string) (string, error) { called = true; return "{}", nil }
		req := &ScanRequest{
			Objective:          &platform.Objective{Kind: "engagement", Statement: "already set"},
			ObjectiveProjectID: projectID, ObjectiveTaskID: taskID, ObjectiveGoal: "g",
		}
		s.roundZeroGoalAnalysis(req)
		if called || req.Objective.Statement != "already set" {
			t.Errorf("called=%v objective=%+v, want the carried objective untouched", called, req.Objective)
		}
	})
}
