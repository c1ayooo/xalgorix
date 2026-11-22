package web

// Approval-rules tests ("approve & remember", REDESIGN §5.2, contract §3.2):
// REST CRUD contract + the gate exemption both layers consume.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// TestApprovalRuleRoutesRegistered extends the lockstep invariant to the
// rules surface (contract §3.2).
func TestApprovalRuleRoutesRegistered(t *testing.T) {
	want := []string{"/api/approval-rules", "/api/approval-rules/"}
	counts := make(map[string]int, len(dashboardRoutes))
	for _, route := range dashboardRoutes {
		counts[route]++
	}
	for _, pattern := range want {
		if counts[pattern] != 1 {
			t.Errorf("dashboardRoutes contains %q %d time(s), want exactly 1", pattern, counts[pattern])
		}
	}
}

// TestApprovalRuleHandlersNilStore503 preserves the platform 503 posture.
func TestApprovalRuleHandlersNilStore503(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name   string
		method string
		path   string
		call   func(w http.ResponseWriter, r *http.Request)
	}{
		{"GET rules", http.MethodGet, "/api/approval-rules", s.handleApprovalRules},
		{"POST rules", http.MethodPost, "/api/approval-rules", s.handleApprovalRules},
		{"DELETE rule", http.MethodDelete, "/api/approval-rules/x", s.handleApprovalRuleDetail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			tc.call(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("code = %d, want 503; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestApprovalRuleHandlersContract pins the CRUD wire contract: create →
// list → delete → 404, with validation and method guards along the way.
func TestApprovalRuleHandlersContract(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("create command rule", func(t *testing.T) {
		rr := httptest.NewRecorder()
		body := `{"project_id":"` + project.ID + `","kind":"command","match":{"program":"curl","contains":"target-x.com"},"reason":"remembered"}`
		req := httptest.NewRequest(http.MethodPost, "/api/approval-rules", strings.NewReader(body))
		s.handleApprovalRules(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code = %d, body=%s", rr.Code, rr.Body.String())
		}
		var rule platform.ApprovalRule
		if err := json.Unmarshal(rr.Body.Bytes(), &rule); err != nil {
			t.Fatal(err)
		}
		if rule.Kind != "command" || rule.Match.Program != "curl" || rule.CreatedBy == "" {
			t.Fatalf("created rule = %+v", rule)
		}
	})

	t.Run("create http rule with expiry", func(t *testing.T) {
		rr := httptest.NewRecorder()
		body := `{"project_id":"` + project.ID + `","kind":"http","match":{"method":"post","host":"api.target-x.com","path_prefix":"api/login"},"expires_at":"2027-01-01T00:00:00Z"}`
		req := httptest.NewRequest(http.MethodPost, "/api/approval-rules", strings.NewReader(body))
		s.handleApprovalRules(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code = %d, body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("validation rejects incoherent rule", func(t *testing.T) {
		rr := httptest.NewRecorder()
		body := `{"project_id":"` + project.ID + `","kind":"command","match":{}}`
		req := httptest.NewRequest(http.MethodPost, "/api/approval-rules", strings.NewReader(body))
		s.handleApprovalRules(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400", rr.Code)
		}
	})

	t.Run("bad kind filter rejected", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/approval-rules?kind=weird", nil)
		s.handleApprovalRules(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400", rr.Code)
		}
	})

	t.Run("list carries projection", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/approval-rules?kind=command", nil)
		s.handleApprovalRules(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code = %d", rr.Code)
		}
		var resp struct {
			Rules []platform.ApprovalRule `json:"rules"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Rules) != 1 || resp.Rules[0].ProjectName != project.Name {
			t.Fatalf("list = %+v, want 1 rule with project_name %q", resp.Rules, project.Name)
		}
	})

	var ruleID string
	t.Run("delete then 404", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/approval-rules?kind=http", nil)
		s.handleApprovalRules(rr, req)
		var resp struct {
			Rules []platform.ApprovalRule `json:"rules"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Rules) != 1 {
			t.Fatalf("http rules = %d, want 1", len(resp.Rules))
		}
		ruleID = resp.Rules[0].ID

		rr = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodDelete, "/api/approval-rules/"+ruleID, nil)
		s.handleApprovalRuleDetail(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("delete code = %d", rr.Code)
		}
		rr = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodDelete, "/api/approval-rules/"+ruleID, nil)
		s.handleApprovalRuleDetail(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("second delete code = %d, want 404", rr.Code)
		}
	})

	t.Run("method guards", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/approval-rules", strings.NewReader(`{}`))
		s.handleApprovalRules(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("PUT list code = %d, want 405", rr.Code)
		}
	})
}

// TestGateRuleExemptsCommandApproval pins the command-gate half of the
// exemption: a remembered rule lets the eino broker allow the call with NO
// pending row, increments the hit counter, and broadcasts approval_rule_hit.
func TestGateRuleExemptsCommandApproval(t *testing.T) {
	s, store := newEnforceGateServer(t, 5, 1)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inst := registerGateInstance(t, s, "inst-rule")

	sctx := scanctx.New("inst-rule", t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(func() { scanctx.Deactivate(sctx.ID) })

	if _, err := store.CreateApprovalRule(ctx, platform.ApprovalRuleInput{
		ProjectID: project.ID,
		Kind:      platform.ApprovalKindCommand,
		Match:     platform.ApprovalRuleMatch{Program: "curl", Contains: "target-x.com"},
	}); err != nil {
		t.Fatal(err)
	}

	broker := s.einoApprovalBroker("inst-rule", sctx)
	id, err := broker.RequestApproval(ctx, engine.ApprovalRequest{
		ToolName: "terminal_execute",
		Args:     map[string]string{"command": "curl -d x=1 https://api.target-x.com/login"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatalf("rule must exempt the call, got approval %s", id)
	}
	rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "all"})
	if len(rows) != 0 {
		t.Fatalf("rule hit must not create a row, got %d", len(rows))
	}

	// Hit bookkeeping.
	rules, _ := store.ListApprovalRules(ctx, platform.ApprovalRuleFilter{ProjectID: project.ID})
	if len(rules) != 1 || rules[0].HitCount != 1 || rules[0].LastHitAt == nil {
		t.Fatalf("hit bookkeeping = %+v, want hit_count 1 + last_hit_at", rules)
	}

	// Audit projection: approval_rule_hit lands in the instance buffer.
	found := false
	for _, evt := range inst.events {
		if evt.Type == "approval_rule_hit" && evt.Rule != nil && evt.Rule.ID == rules[0].ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("approval_rule_hit missing from instance events, got %+v", inst.events)
	}

	// Outside the rule's contains constraint the call still pends.
	id, err = broker.RequestApproval(ctx, engine.ApprovalRequest{
		ToolName: "terminal_execute",
		Args:     map[string]string{"command": "curl -d x=1 https://other.example.com/login"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("contains mismatch must fall back to the approval flow")
	}
}

// TestGateRuleExemptsProxyVerdict pins the wire-gate half: the proxy adapter
// approves a subprocess-channel high-risk request covered by an HTTP rule
// without creating a row.
func TestGateRuleExemptsProxyVerdict(t *testing.T) {
	s, store := newEnforceGateServer(t, 5, 1)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registerGateInstance(t, s, "inst-proxy")

	sctx := scanctx.New("inst-proxy", t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(func() { scanctx.Deactivate(sctx.ID) })

	if _, err := store.CreateApprovalRule(ctx, platform.ApprovalRuleInput{
		ProjectID: project.ID,
		Kind:      platform.ApprovalKindHTTP,
		Match:     platform.ApprovalRuleMatch{Method: "POST", Host: "api.target-x.com", PathPrefix: "api/login"},
	}); err != nil {
		t.Fatal(err)
	}

	s.installProxyHighRiskGate()
	gate := &proxyHighRiskGate{svc: s.approvalService()}
	attr := trafficproxy.Attribution{Channel: trafficproxy.ChannelSubprocess, InstanceID: "inst-proxy", ScanCtxID: sctx.ID}
	verdict := approval.Verdict{
		Decision: approval.Require,
		Kind:     approval.KindHTTP,
		Reason:   "auth mutation endpoint or payload",
		Method:   "POST",
		URL:      "https://api.target-x.com/api/login",
		Body:     "password=x",
	}
	approved, denial, err := gate.Adjudicate(attr, verdict)
	if err != nil {
		t.Fatal(err)
	}
	if !approved || denial != "" {
		t.Fatalf("rule must exempt the wire request, got approved=%v denial=%q", approved, denial)
	}
	rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "all"})
	if len(rows) != 0 {
		t.Fatalf("rule hit must not create a row, got %d", len(rows))
	}

	// Uncovered host still pends a row (submit path). Adjudicate blocks until
	// a verdict exists, so run it async and poll for the pending row.
	verdict.URL = "https://other.example.com/api/login"
	adjudicateDone := make(chan bool, 1)
	go func() {
		ok, _, aerr := gate.Adjudicate(attr, verdict)
		if aerr != nil {
			t.Errorf("adjudicate: %v", aerr)
		}
		adjudicateDone <- ok
	}()
	var pending *platform.Approval
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending"})
		if len(rows) == 1 {
			pending = &rows[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pending == nil {
		t.Fatal("uncovered request must create one pending row")
	}
	if _, err := store.DecideApproval(ctx, pending.ID, platform.ApprovalDecision{Decision: platform.ApprovalRejected, DecidedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if ok := <-adjudicateDone; ok {
		t.Fatal("rejected row must surface as not approved")
	}
}

// TestGateRuleExpiryAndProjectScope pins the two negative consult paths:
// expired rules stop matching; another project's rules never apply.
func TestGateRuleExpiryAndProjectScope(t *testing.T) {
	s, store := newEnforceGateServer(t, 5, 1)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateProject(ctx, "Rules Scope", platform.Scope{Targets: []string{"other.com"}})
	if err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-time.Hour).UTC()
	if _, err := store.CreateApprovalRule(ctx, platform.ApprovalRuleInput{
		ProjectID: project.ID,
		Kind:      platform.ApprovalKindCommand,
		Match:     platform.ApprovalRuleMatch{Program: "curl"},
		ExpiresAt: &past,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApprovalRule(ctx, platform.ApprovalRuleInput{
		ProjectID: other.ID,
		Kind:      platform.ApprovalKindCommand,
		Match:     platform.ApprovalRuleMatch{Program: "curl"},
	}); err != nil {
		t.Fatal(err)
	}

	sctx := scanctx.New("inst-expiry", t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(func() { scanctx.Deactivate(sctx.ID) })

	broker := s.einoApprovalBroker("inst-expiry", sctx)
	id, err := broker.RequestApproval(ctx, engine.ApprovalRequest{
		ToolName: "terminal_execute",
		Args:     map[string]string{"command": "curl -d x=1 https://a.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expired rule must not exempt; cross-project rule must not apply")
	}
}
