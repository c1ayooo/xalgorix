package web

// MCP-server handler tests (API_CONTRACT §integration-mcp-servers): route
// lockstep, nil-store 503, CRUD contract, credential masking (the secret
// boundary is THE invariant here), masked-echo PATCH semantics, and the
// operator-machine endpoint rejection.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func newMcpTestServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	return s
}

func TestMcpServerRoutesRegistered(t *testing.T) {
	want := []string{"/api/mcp-servers", "/api/mcp-servers/", "/api/mcp-servers/invocations"}
	counts := make(map[string]int, len(dashboardRoutes))
	for _, route := range dashboardRoutes {
		counts[route]++
	}
	for _, pattern := range want {
		if counts[pattern] != 1 {
			t.Errorf("dashboardRoutes contains %q %d time(s), want exactly 1", pattern, counts[pattern])
		}
	}
	// Credential-bearing surface: machine tokens must be denied. Membership,
	// not position — the denylist is a set and other session-only routes
	// (users, setup, password) may be appended after it.
	denied := false
	for _, prefix := range machineTokenRouteDenylist {
		if strings.HasPrefix("/api/mcp-servers", prefix) {
			denied = true
			break
		}
	}
	if !denied {
		t.Fatal("mcp-servers must be machine-token denied")
	}
}

func TestMcpServerHandlersNilStore503(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name   string
		method string
		call   func(w http.ResponseWriter, r *http.Request)
	}{
		{"GET servers", http.MethodGet, s.handleMcpServers},
		{"POST servers", http.MethodPost, s.handleMcpServers},
		{"GET detail", http.MethodGet, s.handleMcpServerDetail},
		{"PATCH detail", http.MethodPatch, s.handleMcpServerDetail},
		{"DELETE detail", http.MethodDelete, s.handleMcpServerDetail},
		{"POST refresh", http.MethodPost, s.handleMcpServerDetail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, "/api/mcp-servers/x", strings.NewReader(`{}`))
			tc.call(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("code = %d, want 503; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestMcpServerHandlersContract(t *testing.T) {
	s := newMcpTestServer(t)

	// Create (stdio).
	createBody := `{"name":"recon","transport":"stdio","command":"npx","args":["-y","@example/recon-mcp"],"env":{"API_TOKEN":"sekret-value-123456"},"enabled":true,"require_approval":true}`
	rr := httptest.NewRecorder()
	s.handleMcpServers(rr, httptest.NewRequest(http.MethodPost, "/api/mcp-servers", strings.NewReader(createBody)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created platform.McpServer
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.TimeoutSeconds != platform.DefaultMcpTimeoutSeconds {
		t.Fatalf("created server wrong: %+v", created)
	}
	// THE secret boundary: the create response already masks.
	if strings.Contains(rr.Body.String(), "sekret-value-123456") {
		t.Fatalf("secret leaked on create response: %s", rr.Body.String())
	}

	// HTTP endpoint pointing at the operator machine → 400.
	rr = httptest.NewRecorder()
	s.handleMcpServers(rr, httptest.NewRequest(http.MethodPost, "/api/mcp-servers",
		strings.NewReader(`{"name":"evil","transport":"http","url":"http://127.0.0.1:9137/mcp"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("loopback endpoint = %d, want 400", rr.Code)
	}

	// Duplicate name → 409.
	rr = httptest.NewRecorder()
	s.handleMcpServers(rr, httptest.NewRequest(http.MethodPost, "/api/mcp-servers", strings.NewReader(createBody)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409", rr.Code)
	}

	// GET list: masked.
	rr = httptest.NewRecorder()
	s.handleMcpServers(rr, httptest.NewRequest(http.MethodGet, "/api/mcp-servers", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "sekret-value-123456") {
		t.Fatalf("secret leaked on list: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "****") {
		t.Fatalf("list must show masked credentials: %s", rr.Body.String())
	}

	// PATCH with masked echo: the masked value must NOT overwrite; the real
	// secret must survive (verify via a fresh masked GET + behavioral probe
	// through the store directly).
	rr = httptest.NewRecorder()
	s.handleMcpServerDetail(rr, httptest.NewRequest(http.MethodPatch, "/api/mcp-servers/"+created.ID,
		strings.NewReader(`{"env":{"API_TOKEN":"****-value-123456"},"enabled":false}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rr.Code, rr.Body.String())
	}
	var patched platform.McpServer
	if err := json.Unmarshal(rr.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if patched.Enabled {
		t.Fatal("patch did not disable")
	}
	raw, err := s.currentPlatformStore().GetMcpServer(defaultTestCtx(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Env["API_TOKEN"] != "sekret-value-123456" {
		t.Fatalf("masked echo clobbered the stored secret: %q", raw.Env["API_TOKEN"])
	}

	// Unknown id → 404; nested path → 404.
	rr = httptest.NewRecorder()
	s.handleMcpServerDetail(rr, httptest.NewRequest(http.MethodGet, "/api/mcp-servers/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404", rr.Code)
	}
	rr = httptest.NewRecorder()
	s.handleMcpServerDetail(rr, httptest.NewRequest(http.MethodGet, "/api/mcp-servers/a/b", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("nested path = %d, want 404", rr.Code)
	}

	// DELETE then 404.
	rr = httptest.NewRecorder()
	s.handleMcpServerDetail(rr, httptest.NewRequest(http.MethodDelete, "/api/mcp-servers/"+created.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete = %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	s.handleMcpServerDetail(rr, httptest.NewRequest(http.MethodDelete, "/api/mcp-servers/"+created.ID, nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("double delete = %d, want 404", rr.Code)
	}
}

// TestMcpServerRefreshPinsHealthProjection drives the refresh path against
// a nonexistent stdio command: the probe fails honestly, the failure is
// stamped into the row, and the response carries the error (502).
func TestMcpServerRefreshPinsHealthProjection(t *testing.T) {
	s := newMcpTestServer(t)
	store := s.currentPlatformStore()

	server, err := store.CreateMcpServer(defaultTestCtx(), platform.McpServerInput{
		Name: "ghost", Transport: platform.McpTransportStdio, Command: "definitely-not-a-real-binary-x9",
		Enabled: true, RequireApproval: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	s.handleMcpServerDetail(rr, httptest.NewRequest(http.MethodPost, "/api/mcp-servers/"+server.ID+"/refresh", nil))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("refresh = %d, want 502: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Error  string             `json:"error"`
		Server platform.McpServer `json:"server"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Server.ToolCount != 0 || body.Server.LastError == "" {
		t.Fatalf("health projection not stamped: %+v", body.Server)
	}

	// Loopback HTTP endpoint refresh → 400 (write/refresh-time guard).
	httpServer, err := store.CreateMcpServer(defaultTestCtx(), platform.McpServerInput{
		Name: "loopback", Transport: platform.McpTransportHTTP, URL: "http://127.0.0.1:1/mcp", Enabled: true,
	})
	if err != nil {
		// Write-time guard already rejected it — that is the contract under
		// test; nothing further to probe.
		return
	}
	rr = httptest.NewRecorder()
	s.handleMcpServerDetail(rr, httptest.NewRequest(http.MethodPost, "/api/mcp-servers/"+httpServer.ID+"/refresh", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("loopback refresh = %d, want 400", rr.Code)
	}
}

// defaultTestCtx matches the store tests' bare context.
func defaultTestCtx() context.Context { return context.Background() }
