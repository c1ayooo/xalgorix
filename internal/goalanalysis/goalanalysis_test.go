package goalanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func fmtJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	return string(b)
}

// TestAnalyzeReturnsProposalAndEmbedsGoal verifies the happy path: a valid JSON
// reply maps onto the proposal, and the prompts actually carry the operator's
// goal, execution notes and authorized scope — the model must not analyze blind.
func TestAnalyzeReturnsProposalAndEmbedsGoal(t *testing.T) {
	reply := `{
		"statement": "Obtain independently verified SQL injection on acme.com",
		"criteria": [
			{"kind": "finding_class", "vuln_class": "sqli", "verify_by": "verifier", "required": true, "rationale": "goal names injection on acme.com"},
			{"kind": "capability", "capability": "shell", "verify_by": "deterministic", "required": false, "rationale": "goal names server control"}
		],
		"constraints": ["Only test https://acme.com", "Do not brute force credentials"],
		"notes": "rewrote the vague wording into a verifiable form"
	}`
	var sys, user string
	llm := func(_ context.Context, systemPrompt, userMessage string) (string, error) {
		sys, user = systemPrompt, userMessage
		return reply, nil
	}

	proposal, err := Analyze(context.Background(), AnalyzeInput{
		Goal:         "把 acme.com 打穿",
		Instruction:  "优先测试登录接口",
		ScopeTargets: []string{"https://acme.com"},
	}, llm)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if proposal.Statement != "Obtain independently verified SQL injection on acme.com" {
		t.Errorf("statement = %q", proposal.Statement)
	}
	if len(proposal.Criteria) != 2 {
		t.Fatalf("criteria len = %d, want 2", len(proposal.Criteria))
	}
	c0, c1 := proposal.Criteria[0], proposal.Criteria[1]
	if c0.Kind != "finding_class" || c0.VulnClass != "sqli" || c0.VerifyBy != "verifier" || !c0.Required {
		t.Errorf("criterion 0 = %+v", c0)
	}
	if c0.Rationale == "" {
		t.Errorf("criterion 0 rationale dropped")
	}
	if c1.Kind != "capability" || c1.Capability != "shell" || c1.VerifyBy != "deterministic" || c1.Required {
		t.Errorf("criterion 1 = %+v", c1)
	}
	if len(proposal.Constraints) != 2 || proposal.Constraints[1] != "Do not brute force credentials" {
		t.Errorf("constraints = %q", proposal.Constraints)
	}
	if proposal.Notes == "" {
		t.Errorf("notes dropped")
	}

	// Prompt contract: the analyzer must show the model what it is analyzing.
	if !strings.Contains(user, "把 acme.com 打穿") {
		t.Errorf("user prompt misses the goal text: %q", user)
	}
	if !strings.Contains(user, "优先测试接口") && !strings.Contains(user, "优先测试登录接口") {
		t.Errorf("user prompt misses the instruction text: %q", user)
	}
	if !strings.Contains(user, "https://acme.com") {
		t.Errorf("user prompt misses the authorized scope: %q", user)
	}
	if !strings.Contains(sys, "JSON") {
		t.Errorf("system prompt does not demand JSON output: %q", sys)
	}
}

// TestAnalyzeToleratesFencedAndPreambleOutput: real models wrap JSON in code
// fences or precede it with prose (and sometimes think tags). The extractor
// must still find the outermost JSON object.
func TestAnalyzeToleratesFencedAndPreambleOutput(t *testing.T) {
	for name, reply := range map[string]string{
		"fenced":         "```json\n{\"statement\":\"s\",\"criteria\":[{\"kind\":\"coverage\",\"verify_by\":\"verifier\",\"required\":true}]}\n```",
		"preamble":       "Sure, here is the analysis:\n\n{\"statement\":\"s\",\"criteria\":[{\"kind\":\"coverage\",\"verify_by\":\"verifier\",\"required\":true}]}",
		"think-and-note": "<think>let me reason about this goal…</think>{\"statement\":\"s\",\"criteria\":[{\"kind\":\"coverage\",\"verify_by\":\"verifier\",\"required\":true}]} Hope this helps!",
	} {
		llm := func(_ context.Context, _, _ string) (string, error) { return reply, nil }
		proposal, err := Analyze(context.Background(), AnalyzeInput{Goal: "g"}, llm)
		if err != nil {
			t.Fatalf("%s: Analyze returned error: %v", name, err)
		}
		if proposal.Statement != "s" || len(proposal.Criteria) != 1 {
			t.Errorf("%s: proposal = %+v", name, proposal)
		}
	}
}

// TestAnalyzeDefaultsAndDropsInvalidCriteria: missing verify_by defaults to
// verifier, missing required defaults to true (the operator asked for the
// outcome, so unproven is unmet); criteria that cannot be decided are dropped,
// and the list is capped at platform.MaxObjectiveCriteria.
func TestAnalyzeDefaultsAndDropsInvalidCriteria(t *testing.T) {
	var criteria []map[string]any
	for i := 0; i < platform.MaxObjectiveCriteria+2; i++ {
		criteria = append(criteria, map[string]any{
			"kind": "finding_class", "vuln_class": "cls" + strings.Repeat("x", i+1), "verify_by": "verifier", "required": true,
		})
	}
	criteria = append(criteria,
		map[string]any{"kind": "wormhole", "verify_by": "verifier"},                                   // invalid kind → dropped
		map[string]any{"kind": "finding_class", "verify_by": "verifier"},                              // missing vuln_class → dropped
		map[string]any{"kind": "capability", "verify_by": "psychic"},                                  // invalid verify_by → dropped
		map[string]any{"kind": "finding_class", "vuln_class": "kept-no-verify"},                       // verify_by defaults to verifier
		map[string]any{"kind": "finding_class", "vuln_class": "kept-not-required", "required": false}, // required respected
	)
	reply := fmtJSON(t, map[string]any{
		"statement": "s", "criteria": criteria, "constraints": []string{"  ", "only port 443"},
	})
	llm := func(_ context.Context, _, _ string) (string, error) { return reply, nil }

	proposal, err := Analyze(context.Background(), AnalyzeInput{Goal: "g"}, llm)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(proposal.Criteria) != platform.MaxObjectiveCriteria {
		t.Fatalf("criteria len = %d, want capped at %d", len(proposal.Criteria), platform.MaxObjectiveCriteria)
	}
	if proposal.Criteria[0].VulnClass != "clsx" {
		t.Errorf("first criterion = %+v, want the first valid one kept", proposal.Criteria[0])
	}
	if len(proposal.Constraints) != 1 || proposal.Constraints[0] != "only port 443" {
		t.Errorf("constraints = %q, want blank entries dropped", proposal.Constraints)
	}
	// The two trailing default probes were appended after the cap window, so run
	// a dedicated small reply to assert the defaults.
	reply = `{"statement":"s","criteria":[{"kind":"finding_class","vuln_class":"kept-no-verify"},{"kind":"finding_class","vuln_class":"kept-not-required","required":false}]}`
	llm = func(_ context.Context, _, _ string) (string, error) { return reply, nil }
	proposal, err = Analyze(context.Background(), AnalyzeInput{Goal: "g"}, llm)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if proposal.Criteria[0].VerifyBy != "verifier" || !proposal.Criteria[0].Required {
		t.Errorf("defaults not applied: %+v", proposal.Criteria[0])
	}
	if proposal.Criteria[1].Required {
		t.Errorf("explicit required=false overridden: %+v", proposal.Criteria[1])
	}
}

// TestAnalyzeFailsOpen: every degenerate reply is an error so the caller can
// fall back to running the raw goal — the analyzer never invents a goal.
func TestAnalyzeFailsOpen(t *testing.T) {
	tests := map[string]struct {
		reply  string
		llmErr error
	}{
		"llm error":     {llmErr: errors.New("provider down")},
		"empty reply":   {reply: ""},
		"no json":       {reply: "I cannot analyze that."},
		"empty subject": {reply: `{"statement":"  ","criteria":[{"kind":"coverage","verify_by":"verifier"}]}`},
		"no criteria":   {reply: `{"statement":"s","criteria":[]}`},
		"all invalid":   {reply: `{"statement":"s","criteria":[{"kind":"nope"}]}`},
	}
	for name, tc := range tests {
		llm := func(_ context.Context, _, _ string) (string, error) {
			if tc.llmErr != nil {
				return "", tc.llmErr
			}
			return tc.reply, nil
		}
		if _, err := Analyze(context.Background(), AnalyzeInput{Goal: "g"}, llm); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

// TestProposalObjective: the proposal converts into a platform objective that
// survives the server-side validator unchanged in meaning (engagement kind,
// criteria carry no client-chosen IDs).
func TestProposalObjective(t *testing.T) {
	p := &Proposal{
		Statement: "s",
		Criteria: []Criterion{
			{Kind: "finding_class", VulnClass: "sqli", VerifyBy: "verifier", Required: true, Rationale: "r"},
			{Kind: "capability", Capability: "shell", VerifyBy: "deterministic", Required: false},
		},
		Constraints: []string{"only port 443"},
	}
	obj := p.Objective()
	if obj == nil || obj.Kind != platform.ObjectiveKindEngagement || obj.Statement != "s" {
		t.Fatalf("objective = %+v", obj)
	}
	if len(obj.Criteria) != 2 || obj.Criteria[0].ID != "" {
		t.Errorf("criteria = %+v, want no server-assigned IDs", obj.Criteria)
	}
	if len(obj.Constraints) != 1 || obj.Constraints[0] != "only port 443" {
		t.Errorf("constraints = %q", obj.Constraints)
	}
	norm, err := platform.NormalizeObjective(obj)
	if err != nil {
		t.Fatalf("NormalizeObjective rejected the proposal objective: %v", err)
	}
	if len(norm.Criteria) != 2 || norm.Criteria[0].ID != "C1" {
		t.Errorf("normalized criteria = %+v", norm.Criteria)
	}
	if len(norm.Constraints) != 1 {
		t.Errorf("NormalizeObjective dropped constraints: %+v", norm)
	}
}

// TestAnalyzeEmptyGoalFailsFast: nothing to analyze is a caller bug, not an
// LLM round trip.
func TestAnalyzeEmptyGoalFailsFast(t *testing.T) {
	called := false
	llm := func(context.Context, string, string) (string, error) { called = true; return "{}", nil }
	if _, err := Analyze(context.Background(), AnalyzeInput{Goal: "   "}, llm); err == nil {
		t.Fatal("expected error for empty goal")
	}
	if called {
		t.Error("LLM called for empty goal")
	}
}
