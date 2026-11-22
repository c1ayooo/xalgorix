package web

// Agent-profile handler tests (API_CONTRACT §integration-agent-profiles):
// route lockstep, nil-store 503, CRUD contract, builtin read-only guard.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func newProfilesTestServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	return s
}

func TestAgentProfileRoutesRegistered(t *testing.T) {
	want := []string{"/api/agent-profiles", "/api/agent-profiles/"}
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

func TestAgentProfileHandlersNilStore503(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
	}{
		{"GET profiles", s.handleAgentProfiles},
		{"POST profiles", s.handleAgentProfiles},
		{"GET detail", s.handleAgentProfileDetail},
		{"PATCH detail", s.handleAgentProfileDetail},
		{"DELETE detail", s.handleAgentProfileDetail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/agent-profiles/x", strings.NewReader(`{}`))
			tc.call(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("code = %d, want 503; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAgentProfileHandlersContract(t *testing.T) {
	s := newProfilesTestServer(t)

	// Create.
	createBody := `{"role":"api-fuzzing","focus":"REST/GraphQL surface fuzzing","vuln_classes":["api-auth"],"evidence_contract":"a proven differential or a rejected hypothesis with its control","stopping_rule":"stop when the assigned matrix is exhausted","tool_scope_hidden":["browser_action"],"enabled":true}`
	rr := httptest.NewRecorder()
	s.handleAgentProfiles(rr, httptest.NewRequest(http.MethodPost, "/api/agent-profiles", strings.NewReader(createBody)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created platform.AgentProfile
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Builtin {
		t.Fatalf("created profile wrong: %+v", created)
	}

	// Reserved builtin role → 409.
	rr = httptest.NewRecorder()
	s.handleAgentProfiles(rr, httptest.NewRequest(http.MethodPost, "/api/agent-profiles",
		strings.NewReader(`{"role":"authz-logic","focus":"f","evidence_contract":"e","stopping_rule":"s"}`)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("reserved role = %d, want 409", rr.Code)
	}

	// Duplicate role → 409 (store).
	rr = httptest.NewRecorder()
	s.handleAgentProfiles(rr, httptest.NewRequest(http.MethodPost, "/api/agent-profiles", strings.NewReader(createBody)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate role = %d, want 409", rr.Code)
	}

	// Missing contract triple → 400.
	rr = httptest.NewRecorder()
	s.handleAgentProfiles(rr, httptest.NewRequest(http.MethodPost, "/api/agent-profiles",
		strings.NewReader(`{"role":"no-stop-rule","focus":"f","evidence_contract":"e"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing stopping_rule = %d, want 400", rr.Code)
	}

	// Merged list: builtin rows first, then user rows.
	rr = httptest.NewRecorder()
	s.handleAgentProfiles(rr, httptest.NewRequest(http.MethodGet, "/api/agent-profiles", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d", rr.Code)
	}
	var list struct {
		Profiles []platform.AgentProfile `json:"profiles"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	builtinCount := 0
	for _, p := range list.Profiles {
		if p.Builtin {
			builtinCount++
		}
	}
	if builtinCount != len(agent.BuiltinSpecialistRoles()) {
		t.Fatalf("builtin rows = %d, want %d", builtinCount, len(agent.BuiltinSpecialistRoles()))
	}
	if len(list.Profiles) != builtinCount+1 {
		t.Fatalf("merged list = %d rows, want %d builtin + 1 user", len(list.Profiles), builtinCount)
	}

	// Builtin pseudo-id is not mutable (404).
	rr = httptest.NewRecorder()
	s.handleAgentProfileDetail(rr, httptest.NewRequest(http.MethodDelete, "/api/agent-profiles/builtin:authz-logic", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("builtin delete = %d, want 404", rr.Code)
	}

	// PATCH: disable user profile.
	rr = httptest.NewRecorder()
	s.handleAgentProfileDetail(rr, httptest.NewRequest(http.MethodPatch, "/api/agent-profiles/"+created.ID, strings.NewReader(`{"enabled":false}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rr.Code, rr.Body.String())
	}

	// DELETE then 404.
	rr = httptest.NewRecorder()
	s.handleAgentProfileDetail(rr, httptest.NewRequest(http.MethodDelete, "/api/agent-profiles/"+created.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete = %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	s.handleAgentProfileDetail(rr, httptest.NewRequest(http.MethodDelete, "/api/agent-profiles/"+created.ID, nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("double delete = %d, want 404", rr.Code)
	}
}
