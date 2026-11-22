package web

// handlers_mcp.go — REST surface for operator-registered MCP servers
// (docs/API_CONTRACT.md §integration-mcp-servers):
//
//	GET    /api/mcp-servers        → {servers: [...]}   (env/headers masked)
//	POST   /api/mcp-servers        {name, transport, command?, args?, env?, url?, headers?, enabled, require_approval, timeout_seconds?} → McpServer
//	GET    /api/mcp-servers/{id}             → McpServer (masked)
//	PATCH  /api/mcp-servers/{id}             → McpServer (masked)
//	DELETE /api/mcp-servers/{id}             → {ok: true}
//	POST   /api/mcp-servers/{id}/refresh     → McpServer (connect + tools/list probe; stamps tool_count/last_healthy_at/last_error)
//
// Secret boundary (charter §7 / API invariant): env/headers are NEVER
// returned in clear — every read masks with maskAuthCredential, and a
// submission whose value is "****"-prefixed means "unchanged"
// (isMaskedSettingValue). The route is machine-token denied (session-only)
// because it carries credential material.
//
// The refresh probe is an operator-initiated diagnostic: it applies the same
// operator-machine endpoint rejection as the scan path (scopeguard), but
// runs outside a scan so the per-scan dial/request guards do not apply —
// the scan-time connection (integration_inputs → agent) is always fully
// guarded. Errors map through writePlatformError; nil store → 503.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
	mcptool "github.com/xalgord/xalgorix/v4/internal/tools/mcp"
)

// handleMcpServers serves GET (list) and POST (create) on /api/mcp-servers.
func (s *Server) handleMcpServers(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleMcpServersList(w, r, store)
	case http.MethodPost:
		s.handleMcpServersCreate(w, r, store)
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleMcpServersList(w http.ResponseWriter, r *http.Request, store platform.Repository) {
	servers, err := store.ListMcpServers(r.Context())
	if err != nil {
		writePlatformError(w, err)
		return
	}
	masked := make([]platform.McpServer, len(servers))
	for i, m := range servers {
		masked[i] = maskMcpServer(m)
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"servers": masked})
}

func (s *Server) handleMcpServersCreate(w http.ResponseWriter, r *http.Request, store platform.Repository) {
	limitJSONBody(w, r)
	var req struct {
		Name            string            `json:"name"`
		Transport       string            `json:"transport"`
		Command         string            `json:"command"`
		Args            []string          `json:"args"`
		Env             map[string]string `json:"env"`
		URL             string            `json:"url"`
		Headers         map[string]string `json:"headers"`
		Enabled         bool              `json:"enabled"`
		RequireApproval bool              `json:"require_approval"`
		TimeoutSeconds  int               `json:"timeout_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if err := s.validateMcpEndpoint(req.Transport, req.URL); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	server, err := store.CreateMcpServer(r.Context(), platform.McpServerInput{
		Name:            req.Name,
		Transport:       req.Transport,
		Command:         req.Command,
		Args:            req.Args,
		Env:             req.Env,
		URL:             req.URL,
		Headers:         req.Headers,
		Enabled:         req.Enabled,
		RequireApproval: req.RequireApproval,
		TimeoutSeconds:  req.TimeoutSeconds,
		CreatedBy:       s.operatorIdentity(),
	})
	if err != nil {
		writePlatformError(w, err)
		return
	}
	s.broadcastDashboard(WSEvent{
		Type:    "mcp_servers_changed",
		Content: "MCP server created: " + server.Name,
	})
	writeJSONStatus(w, http.StatusCreated, maskMcpServer(*server))
}

// handleMcpServerDetail is the /api/mcp-servers/ subtree dispatcher: {id}
// (GET/PATCH/DELETE) and {id}/refresh (POST).
func (s *Server) handleMcpServerDetail(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/mcp-servers/")
	if rest == "" {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "mcp server not found"})
		return
	}
	if strings.HasSuffix(rest, "/refresh") {
		id := strings.TrimSuffix(rest, "/refresh")
		if id == "" || strings.Contains(id, "/") {
			writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "mcp server not found"})
			return
		}
		s.handleMcpServerRefresh(w, r, store, id)
		return
	}
	if strings.Contains(rest, "/") {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "mcp server not found"})
		return
	}
	id := rest
	switch r.Method {
	case http.MethodGet:
		server, err := store.GetMcpServer(r.Context(), id)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, maskMcpServer(*server))
	case http.MethodPatch:
		s.handleMcpServerPatch(w, r, store, id)
	case http.MethodDelete:
		if err := store.DeleteMcpServer(r.Context(), id); err != nil {
			writePlatformError(w, err)
			return
		}
		s.broadcastDashboard(WSEvent{
			Type:    "mcp_servers_changed",
			Content: "MCP server deleted: " + id,
		})
		writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleMcpServerPatch(w http.ResponseWriter, r *http.Request, store platform.Repository, id string) {
	limitJSONBody(w, r)
	var req struct {
		Name            *string            `json:"name"`
		Command         *string            `json:"command"`
		Args            []string           `json:"args"`
		Env             *map[string]string `json:"env"`
		URL             *string            `json:"url"`
		Headers         *map[string]string `json:"headers"`
		Enabled         *bool              `json:"enabled"`
		RequireApproval *bool              `json:"require_approval"`
		TimeoutSeconds  *int               `json:"timeout_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	// Masked submissions mean "unchanged": every "****"-prefixed entry is
	// replaced by the server's current value so the masked echo can never
	// overwrite (or delete) the stored secret.
	current, err := store.GetMcpServer(r.Context(), id)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	env := mergeMaskedEntries(req.Env, current.Env)
	headers := mergeMaskedEntries(req.Headers, current.Headers)
	// Endpoint guard: transport is not PATCHable, so a submitted URL is
	// validated against the server's existing transport.
	if req.URL != nil {
		if current, err := store.GetMcpServer(r.Context(), id); err == nil && current.Transport == platform.McpTransportHTTP {
			if err := s.validateMcpEndpoint(current.Transport, *req.URL); err != nil {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}
	}
	server, err := store.UpdateMcpServer(r.Context(), id, platform.McpServerUpdate{
		Name:            req.Name,
		Command:         req.Command,
		Args:            req.Args,
		Env:             env,
		URL:             req.URL,
		Headers:         headers,
		Enabled:         req.Enabled,
		RequireApproval: req.RequireApproval,
		TimeoutSeconds:  req.TimeoutSeconds,
	})
	if err != nil {
		writePlatformError(w, err)
		return
	}
	s.broadcastDashboard(WSEvent{
		Type:    "mcp_servers_changed",
		Content: "MCP server updated: " + server.Name,
	})
	writeJSONStatus(w, http.StatusOK, maskMcpServer(*server))
}

// handleMcpServerRefresh serves POST {id}/refresh: connect, list tools,
// stamp the health projection. The update is best-effort audit bookkeeping
// (RecordApprovalRuleHit posture) — a failed stamp is logged, the probe
// result is still returned to the operator. The dispatcher has already
// validated the method (POST) and resolved the id.
func (s *Server) handleMcpServerRefresh(w http.ResponseWriter, r *http.Request, store platform.Repository, id string) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	server, err := store.GetMcpServer(r.Context(), id)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	if err := s.validateMcpEndpoint(server.Transport, server.URL); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	cfg := s.configSnapshot()
	scope := scopeguard.Config{AllowLocalTargets: cfg != nil && cfg.AllowLocalTargets}
	if cfg != nil {
		scope.BindAddr = cfg.BindAddr
		scope.Port = s.port
	}
	mgr := mcptool.NewManager()
	defer mgr.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	spec := mcptool.ServerSpec{
		Name:           server.Name,
		Transport:      server.Transport,
		Command:        server.Command,
		Args:           server.Args,
		Env:            server.Env,
		URL:            server.URL,
		Headers:        server.Headers,
		TimeoutSeconds: server.TimeoutSeconds,
		Scope:          scope,
	}
	tools, err := mgr.Connect(ctx, spec)

	now := time.Now().UTC()
	toolCount := len(tools)
	lastErr := ""
	if err != nil {
		lastErr = err.Error()
		toolCount = 0
		log.Printf("[mcp] refresh %q failed: %v", server.Name, err)
	}
	if _, uerr := store.UpdateMcpServer(r.Context(), id, platform.McpServerUpdate{
		ToolCount:     &toolCount,
		LastHealthyAt: &now,
		LastError:     &lastErr,
	}); uerr != nil {
		log.Printf("[mcp] refresh stamp for %q failed: %v", server.Name, uerr)
	}

	fresh, ferr := store.GetMcpServer(r.Context(), id)
	if ferr != nil {
		writePlatformError(w, ferr)
		return
	}
	if err != nil {
		// Probe failed: 502 with the stamped row so the operator sees both.
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{
			"error":  err.Error(),
			"server": maskMcpServer(*fresh),
		})
		return
	}
	writeJSONStatus(w, http.StatusOK, maskMcpServer(*fresh))
}

// validateMcpEndpoint applies the operator-machine rejection to an HTTP
// endpoint at write/refresh time (the scan path re-checks at connect time).
func (s *Server) validateMcpEndpoint(transport, url string) error {
	if transport != platform.McpTransportHTTP {
		return nil
	}
	cfg := s.configSnapshot()
	scope := scopeguard.Config{AllowLocalTargets: cfg != nil && cfg.AllowLocalTargets}
	if cfg != nil {
		scope.BindAddr = cfg.BindAddr
		scope.Port = s.port
	}
	if scopeguard.IsLocalOrListener(scope, strings.TrimSpace(url)) {
		return errOperatorMachineEndpoint
	}
	return nil
}

var errOperatorMachineEndpoint = &endpointError{"endpoint resolves to the operator machine — MCP HTTP servers must point at a remote host"}

type endpointError struct{ msg string }

func (e *endpointError) Error() string { return e.msg }

// maskMcpServer returns a copy with credential-bearing fields masked.
func maskMcpServer(m platform.McpServer) platform.McpServer {
	if len(m.Env) > 0 {
		env := make(map[string]string, len(m.Env))
		for k, v := range m.Env {
			env[k] = maskAuthCredential(v)
		}
		m.Env = env
	}
	if len(m.Headers) > 0 {
		headers := make(map[string]string, len(m.Headers))
		for k, v := range m.Headers {
			headers[k] = maskAuthCredential(v)
		}
		m.Headers = headers
	}
	return m
}

// mergeMaskedEntries replaces "****"-prefixed values (masked echoes) with
// the server's current values before the update; nil in → nil out (PATCH
// unchanged semantics preserved).
func mergeMaskedEntries(submitted *map[string]string, current map[string]string) map[string]string {
	if submitted == nil {
		return nil
	}
	out := make(map[string]string, len(*submitted))
	for k, v := range *submitted {
		if isMaskedSettingValue(v) {
			if cur, ok := current[k]; ok {
				out[k] = cur
			}
			continue
		}
		out[k] = v
	}
	return out
}
