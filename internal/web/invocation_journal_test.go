package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/agent"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

// withInvocationTestJournals points the process-wide journal set at a fresh
// temp dir for one test. The global map makes parallel tests unsafe here —
// invocation tests stay sequential (no t.Parallel).
func withInvocationTestJournals(t *testing.T) {
	t.Helper()
	s := &Server{dataDir: t.TempDir()}
	s.initInvocationJournals()
	t.Cleanup(func() {
		invocationJournalsMu.Lock()
		invocationJournals = map[string]*invocationJournal{}
		invocationJournalsMu.Unlock()
	})
}

func invocationJournalPath(t *testing.T, module string) string {
	t.Helper()
	invocationJournalsMu.RLock()
	defer invocationJournalsMu.RUnlock()
	j := invocationJournals[module]
	if j == nil {
		t.Fatalf("journal %s not initialized", module)
	}
	return j.path
}

// TestInvocationJournalAppendAndRead pins the append → newest-first read →
// filter → pagination contract of the cross-scan projection.
func TestInvocationJournalAppendAndRead(t *testing.T) {
	withInvocationTestJournals(t)

	sess := &scanSession{id: "scan-1", instanceID: "inst-1"}
	deriveInvocationRecords(agent.Event{
		Type:       "tool_result",
		ToolName:   "read_skill",
		ToolArgs:   map[string]string{"name": "testing-xss", "api_key": "sekret"},
		ToolResult: tools.Result{Output: "Loaded skill"},
	}, sess)
	deriveInvocationRecords(agent.Event{
		Type:       "tool_result",
		ToolName:   "read_skill",
		ToolArgs:   map[string]string{"name": "testing-sqli"},
		ToolResult: tools.Result{Error: "unknown skill"},
	}, sess)
	deriveInvocationRecords(agent.Event{
		Type:       "tool_result",
		ToolName:   "mcp_recon_lookup",
		ToolArgs:   map[string]string{"query": "example.com"},
		ToolResult: tools.Result{Output: "{}"},
	}, sess)
	deriveInvocationRecords(agent.Event{
		Type:       "tool_result",
		ToolName:   "spawn_agent",
		ToolArgs:   map[string]string{"name": "authz-logic", "task": "test idor"},
		ToolResult: tools.Result{Output: "Sub-agent spawned", Metadata: map[string]any{"agent_id": "sub_1_1"}},
	}, sess)
	deriveInvocationRecords(agent.Event{
		Type:    "finished",
		AgentID: "sub_1_1",
		Content: "done: 2 findings",
	}, sess)
	// Root finish must NOT create a delegation record.
	deriveInvocationRecords(agent.Event{Type: "finished", Content: "done"}, sess)
	// Non-matching tool names must not record anything.
	deriveInvocationRecords(agent.Event{
		Type:       "tool_result",
		ToolName:   "terminal",
		ToolResult: tools.Result{Output: "ok"},
	}, sess)

	items, total, err := listInvocations(InvocationModuleSkills, 1, 50, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("skills records = %d/%d, want 2/2", len(items), total)
	}
	// Newest first: the second (failed) load comes back first.
	if items[0].Status != InvocationStatusError || items[0].Name != "testing-sqli" {
		t.Fatalf("newest-first order broken: %+v", items[0])
	}
	if items[1].Name != "testing-xss" {
		t.Fatalf("oldest record wrong: %+v", items[1])
	}
	// Secret-ish arg keys masked.
	if strings.Contains(items[1].Args, "sekret") {
		t.Fatalf("secret leaked into args digest: %q", items[1].Args)
	}

	// Status + name filters.
	okOnly, total, err := listInvocations(InvocationModuleSkills, 1, 50, "", "", InvocationStatusOK)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || okOnly[0].Name != "testing-xss" {
		t.Fatalf("status filter wrong: %+v", okOnly)
	}
	byName, total, err := listInvocations(InvocationModuleSkills, 1, 50, "", "xss", "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || byName[0].Name != "testing-xss" {
		t.Fatalf("name filter wrong: %+v", byName)
	}
	_, total, err = listInvocations(InvocationModuleSkills, 1, 50, "inst-1", "", "")
	if err != nil || total != 2 {
		t.Fatalf("instance filter wrong: %d/%v", total, err)
	}
	empty, _, err := listInvocations(InvocationModuleSkills, 1, 50, "other", "", "")
	if err != nil || len(empty) != 0 {
		t.Fatalf("unknown instance must filter to empty, got %v/%v", empty, err)
	}

	// MCP module: server segment extraction.
	mcpItems, total, err := listInvocations(InvocationModuleMCP, 1, 50, "", "", "")
	if err != nil || total != 1 || mcpItems[0].Server != "recon" {
		t.Fatalf("mcp record wrong: %v/%v %+v", total, err, mcpItems)
	}

	// Agents module: spawn + completion, root finish excluded.
	agentItems, total, err := listInvocations(InvocationModuleAgents, 1, 50, "", "", "")
	if err != nil || total != 2 {
		t.Fatalf("agent records = %d/%v, want 2", total, err)
	}
	if agentItems[0].Name != "delegate_result" || agentItems[0].AgentID != "sub_1_1" {
		t.Fatalf("completion record wrong: %+v", agentItems[0])
	}
	if agentItems[1].Name != "authz-logic" || agentItems[1].Args == "" {
		t.Fatalf("spawn record wrong: %+v", agentItems[1])
	}

	// Pagination: page 2 of size 1 returns the second-newest record.
	page2, total, err := listInvocations(InvocationModuleAgents, 2, 1, "", "", "")
	if err != nil || total != 2 || len(page2) != 1 || page2[0].Name != "authz-logic" {
		t.Fatalf("pagination wrong: %v %v %+v", total, err, page2)
	}
}

// TestInvocationJournalPaginationEnvelope checks the HTTP surface: envelope
// shape, method guard, invalid status 400.
func TestInvocationJournalPaginationEnvelope(t *testing.T) {
	withInvocationTestJournals(t)
	recordInvocation(InvocationRecord{Module: InvocationModuleSkills, Name: "a", Status: InvocationStatusOK, ScanID: "s"})
	s := &Server{}

	rec := httptest.NewRecorder()
	s.handleSkillInvocations(rec, httptest.NewRequest(http.MethodGet, "/api/skills/invocations?page=1&size=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Items []InvocationRecord `json:"items"`
		Total int                `json:"total"`
		Page  int                `json:"page"`
		Size  int                `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Total != 1 || len(envelope.Items) != 1 || envelope.Page != 1 || envelope.Size != 10 {
		t.Fatalf("envelope wrong: %+v", envelope)
	}

	rec = httptest.NewRecorder()
	s.handleSkillInvocations(rec, httptest.NewRequest(http.MethodPost, "/api/skills/invocations", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.handleSkillInvocations(rec, httptest.NewRequest(http.MethodGet, "/api/skills/invocations?status=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status = %d, want 400", rec.Code)
	}

	// Empty journal (no records for module) is an honest empty list.
	rec = httptest.NewRecorder()
	s.handleAgentInvocations(rec, httptest.NewRequest(http.MethodGet, "/api/agent-profiles/invocations", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("empty module = %d %s", rec.Code, rec.Body.String())
	}
}

// TestInvocationJournalRotation pins the bounded-disk rotation: once the
// live file crosses the cap, the next append moves it to .old and keeps
// appending; reads span both files and skip torn lines.
func TestInvocationJournalRotation(t *testing.T) {
	withInvocationTestJournals(t)
	path := invocationJournalPath(t, InvocationModuleMCP)
	// Pre-grow the live file to the cap so the next append triggers rotation.
	if err := os.WriteFile(path, []byte(strings.Repeat("x", invocationMaxFileBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	recordInvocation(InvocationRecord{Module: InvocationModuleMCP, Name: "after-rotate", Status: InvocationStatusOK})
	if _, err := os.Stat(path + ".old"); err != nil {
		t.Fatalf("rotation did not produce .old: %v", err)
	}
	items, total, err := listInvocations(InvocationModuleMCP, 1, 500, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// The pre-grown file is garbage lines — skipped by the reader. Only the
	// post-rotation record survives.
	if total != 1 || items[0].Name != "after-rotate" {
		t.Fatalf("post-rotation read wrong: %d %+v", total, items)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live file not recreated: %v", err)
	}
}

// TestInvocationRoutesRegistered mirrors the route-lockstep contract for the
// invocation endpoints.
func TestInvocationRoutesRegistered(t *testing.T) {
	want := []string{
		"/api/mcp-servers/invocations",
		"/api/skills/invocations",
		"/api/agent-profiles/invocations",
	}
	for _, route := range want {
		count := 0
		for _, registered := range dashboardRoutes {
			if registered == route {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("route %s appears %d times in dashboardRoutes, want exactly 1", route, count)
		}
	}
}
