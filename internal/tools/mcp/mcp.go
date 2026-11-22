// Package mcp bridges operator-registered MCP (Model Context Protocol)
// servers into the engine's tool registry. A connected server's tools become
// ordinary registry tools named mcp_<server>_<tool>, so they inherit the
// full execution chain unchanged — circuit breaker, parameter validation,
// approval gate, BeforeTool scope/phase gates and the security guards
// (charter: a new tool source is an Operation-ring entry, never a bypass).
//
// Per-scan posture: a Manager owns exactly one scan's sessions (created by
// the agent at scan launch, closed when the scan ends). There is no
// package-global mutable state — concurrent scans never share sessions.
//
// Security:
//   - HTTP servers are refused when the endpoint classifies as the
//     operator's machine (scopeguard.IsLocalOrListener), and every dial is
//     re-checked post-DNS through the scan's DialGuard with redirect hops
//     re-checked through the scan's RequestGuard (same semantics as the
//     HTTP tools).
//   - stdio servers spawn their command directly (no shell); the inherited
//     environment passes through SanitizeEnviron (operator secrets stripped)
//     before the server's explicitly configured Env entries are added —
//     those are the operator's intended credentials for THIS server.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

// Transports.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// ServerSpec is one server's connection configuration (adapted from the
// platform definition; credential fields ride along for the connection only
// and are never echoed back).
type ServerSpec struct {
	Name           string
	Transport      string
	Command        string
	Args           []string
	Env            map[string]string
	URL            string
	Headers        map[string]string
	TimeoutSeconds int

	// Scope rejects operator-machine endpoints (HTTP transport). Zero value
	// = default scope config.
	Scope scopeguard.Config
	// RequestGuard checks every redirect hop; DialGuard re-checks resolved
	// addresses. Both are the scan's installed guards (nil = unchecked, same
	// fallback posture as the registry).
	RequestGuard tools.RequestGuard
	DialGuard    tools.DialGuard
}

// ToolDescriptor is one enumerated remote tool.
type ToolDescriptor struct {
	Name        string
	Description string
	Parameters  []tools.Parameter
}

// serverSession is one live connection plus its enumerated tools.
type serverSession struct {
	spec  ServerSpec
	ret   *mcp.ClientSession
	tools []ToolDescriptor
}

// Manager owns one scan's MCP sessions. All methods are safe for concurrent
// use; the zero value is not usable — construct with NewManager.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*serverSession // key: server name
}

// NewManager builds an empty per-scan manager.
func NewManager() *Manager {
	return &Manager{sessions: map[string]*serverSession{}}
}

// Connect dials one server, enumerates its tools and caches the session.
// A failure returns an error and leaves the manager unchanged — the scan
// continues without this server's tools.
func (m *Manager) Connect(ctx context.Context, spec ServerSpec) ([]ToolDescriptor, error) {
	if spec.TimeoutSeconds <= 0 {
		spec.TimeoutSeconds = 120
	}
	transport, err := dialTransport(ctx, spec)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "xalgorix", Version: "1.0"}, nil)
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	session, err := client.Connect(connectCtx, transport, nil)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("mcp: connect %q: %w", spec.Name, err)
	}

	listCtx, listCancel := context.WithTimeout(ctx, 30*time.Second)
	result, err := session.ListTools(listCtx, nil)
	listCancel()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("mcp: list tools %q: %w", spec.Name, err)
	}
	descriptors := make([]ToolDescriptor, 0, len(result.Tools))
	for _, t := range result.Tools {
		descriptors = append(descriptors, ToolDescriptor{
			Name:        t.Name,
			Description: toolDescription(t),
			Parameters:  parametersFromSchema(t.InputSchema),
		})
	}
	m.mu.Lock()
	m.sessions[spec.Name] = &serverSession{spec: spec, ret: session, tools: descriptors}
	m.mu.Unlock()
	return descriptors, nil
}

// ToolInfos returns the enumerated tools of every connected server.
func (m *Manager) ToolInfos() map[string][]ToolDescriptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]ToolDescriptor, len(m.sessions))
	for name, s := range m.sessions {
		out[name] = append([]ToolDescriptor(nil), s.tools...)
	}
	return out
}

// Close tears down every session (stdio children are killed by the
// transport's close path).
func (m *Manager) Close() {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = map[string]*serverSession{}
	m.mu.Unlock()
	for _, s := range sessions {
		_ = s.ret.Close()
	}
}

// Call executes one tool on the named server. The returned string is the
// text rendering of the result content; tool-level errors (IsError) and
// protocol errors both come back as errors — the registry turns them into
// Result.Error (circuit breaker accounting unchanged).
func (m *Manager) Call(ctx context.Context, server, tool string, args map[string]string) (string, error) {
	m.mu.Lock()
	s, ok := m.sessions[server]
	m.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("mcp: server %q is not connected", server)
	}
	timeoutSecs := s.spec.TimeoutSeconds
	if timeoutSecs <= 0 {
		timeoutSecs = 120
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()
	result, err := s.ret.CallTool(callCtx, &mcp.CallToolParams{
		Name:      tool,
		Arguments: coerceArgs(args),
	})
	if err != nil {
		return "", fmt.Errorf("mcp: call %s/%s: %w", server, tool, err)
	}
	text := renderCallResult(result)
	if result.IsError {
		return "", fmt.Errorf("mcp: %s/%s failed: %s", server, tool, truncate(text, 400))
	}
	return text, nil
}

// BridgeTools registers every connected server's tools into the registry.
// Naming: mcp_<server>_<tool>, sanitized to the registry's flat namespace;
// collisions get a numeric suffix so a server can never shadow another
// server's tools (and the mcp_ prefix keeps them discoverable).
func (m *Manager) BridgeTools(r *tools.Registry) {
	m.mu.Lock()
	sessions := make([]*serverSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].spec.Name < sessions[j].spec.Name })
	taken := map[string]bool{}
	for _, s := range sessions {
		for _, td := range s.tools {
			name := uniqueToolName(taken, sanitizeToolName("mcp_"+s.spec.Name+"_"+td.Name))
			td := td
			server := s.spec.Name
			r.Register(&tools.Tool{
				Name:        name,
				Description: bridgeDescription(server, td),
				Parameters:  td.Parameters,
				ExecuteContext: func(ctx context.Context, args map[string]string) (tools.Result, error) {
					// Provenance rides the call: hooks/events/approvals see the
					// originating server and the remote tool name even though
					// the registry namespace is flat.
					merged := make(map[string]string, len(args)+2)
					merged["mcp_server"] = server
					merged["mcp_tool"] = td.Name
					for k, v := range args {
						merged[k] = v
					}
					out, err := m.Call(ctx, server, td.Name, args)
					if err != nil {
						return tools.Result{Error: err.Error()}, nil
					}
					return tools.Result{Output: out, Success: true, Metadata: map[string]any{
						"mcp_server": server,
						"mcp_tool":   td.Name,
					}}, nil
				},
			})
		}
	}
}

// bridgeDescription annotates the remote description with provenance so the
// model knows the tool originates from an operator-registered MCP server.
func bridgeDescription(server string, td ToolDescriptor) string {
	desc := strings.TrimSpace(td.Description)
	if desc == "" {
		return fmt.Sprintf("MCP tool %q from operator-registered server %q.", td.Name, server)
	}
	return fmt.Sprintf("[MCP server %q] %s", server, desc)
}

// dialTransport builds the transport for one spec, applying the endpoint
// security checks.
func dialTransport(ctx context.Context, spec ServerSpec) (mcp.Transport, error) {
	switch spec.Transport {
	case TransportStdio:
		if strings.TrimSpace(spec.Command) == "" {
			return nil, fmt.Errorf("mcp: stdio server %q has no command", spec.Name)
		}
		cmd := execCommand(spec)
		return &mcp.CommandTransport{Command: cmd}, nil
	case TransportHTTP:
		url := strings.TrimSpace(spec.URL)
		if url == "" {
			return nil, fmt.Errorf("mcp: http server %q has no URL", spec.Name)
		}
		// Operator-machine rejection: an MCP endpoint pointing at the
		// dashboard/listener is refused before any connection is attempted.
		if scopeguard.IsLocalOrListener(spec.Scope, url) {
			return nil, fmt.Errorf("mcp: server %q endpoint %q resolves to the operator machine", spec.Name, url)
		}
		client := guardedHTTPClient(spec)
		return &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: client}, nil
	default:
		return nil, fmt.Errorf("mcp: unknown transport %q for server %q", spec.Transport, spec.Name)
	}
}

// execCommand builds the stdio child process: direct argv (no shell), the
// sanitized inherited environment plus the operator's explicitly configured
// Env entries, and the context wired for cancellation.
func execCommand(spec ServerSpec) *exec.Cmd {
	cmd := exec.Command(spec.Command, spec.Args...) //nolint:gosec // operator-configured server command; approval gate covers execution
	cmd.Env = tools.SanitizeEnviron(os.Environ())
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

// guardedHTTPClient wraps the scan's guards around a plain HTTP client: the
// request guard re-checks every redirect hop, the dial guard re-checks the
// post-DNS address (DNS rebinding).
func guardedHTTPClient(spec ServerSpec) *http.Client {
	transport := &http.Transport{}
	if spec.DialGuard != nil {
		dialGuard := spec.DialGuard
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if err := dialGuard(addr); err != nil {
				return nil, err
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, addr)
		}
	}
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second}
	if spec.RequestGuard != nil {
		requestGuard := spec.RequestGuard
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("mcp: stopped after 10 redirects")
			}
			if err := requestGuard(req.URL.String()); err != nil {
				return err
			}
			return nil
		}
	}
	return client
}

// coerceArgs maps registry string args to MCP JSON arguments: values that
// parse as JSON (numbers, bools, arrays, objects) are sent typed; plain
// strings stay strings.
func coerceArgs(args map[string]string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		if k == "" {
			continue
		}
		trimmed := strings.TrimSpace(v)
		var parsed any
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[' || trimmed[0] == '"' ||
			trimmed == "true" || trimmed == "false" || trimmed == "null" || isNumericLiteral(trimmed)) {
			if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
				out[k] = parsed
				continue
			}
		}
		out[k] = v
	}
	return out
}

// isNumericLiteral reports whether s looks like a JSON number (fast path
// before the parse attempt).
func isNumericLiteral(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	digits := 0
	for ; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			digits++
			continue
		}
		if c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			continue
		}
		return false
	}
	return digits > 0
}

// parametersFromSchema maps an MCP input schema (a map[string]any from the
// client-side unmarshal) onto the registry's flat parameter list.
func parametersFromSchema(schema any) []tools.Parameter {
	obj, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	props, _ := obj["properties"].(map[string]any)
	required := map[string]bool{}
	if reqList, ok := obj["required"].([]any); ok {
		for _, r := range reqList {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	params := make([]tools.Parameter, 0, len(names))
	for _, name := range names {
		desc := ""
		if prop, ok := props[name].(map[string]any); ok {
			if d, ok := prop["description"].(string); ok {
				desc = d
			}
			if desc == "" {
				if t, ok := prop["type"].(string); ok && t != "" {
					desc = "Type: " + t + "."
				}
			}
		}
		params = append(params, tools.Parameter{
			Name:        name,
			Description: desc,
			Required:    required[name],
		})
	}
	return params
}

// toolDescription renders the SDK tool's description (empty tolerated).
func toolDescription(t *mcp.Tool) string {
	return strings.TrimSpace(t.Description)
}

// renderCallResult flattens a call result into model-readable text: text
// content joined, structured content appended as JSON when present.
func renderCallResult(result *mcp.CallToolResult) string {
	var texts []string
	for _, c := range result.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			if v.Text != "" {
				texts = append(texts, v.Text)
			}
		default:
			if data, err := json.Marshal(c); err == nil && len(data) > 0 && string(data) != "null" {
				texts = append(texts, string(data))
			}
		}
	}
	if result.StructuredContent != nil {
		if data, err := json.Marshal(result.StructuredContent); err == nil {
			texts = append(texts, string(data))
		}
	}
	out := strings.Join(texts, "\n")
	if strings.TrimSpace(out) == "" {
		return "(empty result)"
	}
	return out
}

// sanitizeToolName flattens a bridged tool name into the registry's
// namespace: lowercase, [a-z0-9_] only.
func sanitizeToolName(name string) string {
	lower := strings.ToLower(name)
	var b strings.Builder
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "mcp_tool"
	}
	return out
}

// uniqueToolName resolves collisions with a numeric suffix so one server's
// tools can never shadow another's.
func uniqueToolName(taken map[string]bool, base string) string {
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	taken[name] = true
	return name
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
