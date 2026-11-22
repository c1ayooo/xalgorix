package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// newTestSession wires an in-process MCP server (SDK in-memory transports)
// into a Manager so the happy-path contract runs without subprocesses.
func newTestSession(t *testing.T) (*Manager, string) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "lookup",
		Description: "Look a thing up",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Query string `json:"query" jsonschema:"query text"`
	}) (*mcp.CallToolResult, any, error) {
		if in.Query == "" {
			return &mcp.CallToolResult{IsError: true}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "result-for-" + in.Query}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "no-args", Description: "Takes nothing"}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	// server.Run serves until the transport closes — serve in the background.
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run(context.Background(), serverTransport) }()
	t.Cleanup(func() {
		if err := <-serverErr; err != nil && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "canceled") {
			t.Logf("test server exited: %v", err)
		}
	})
	mgr := NewManager()
	t.Cleanup(mgr.Close)
	spec := ServerSpec{Name: "recon", Transport: "in-memory"}
	// The in-memory transport bypasses dialTransport — connect directly.
	client := mcp.NewClient(&mcp.Implementation{Name: "xalgorix-test", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	list, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := make([]ToolDescriptor, 0, len(list.Tools))
	for _, tl := range list.Tools {
		descriptors = append(descriptors, ToolDescriptor{
			Name:        tl.Name,
			Description: tl.Description,
			Parameters:  parametersFromSchema(tl.InputSchema),
		})
	}
	mgr.mu.Lock()
	mgr.sessions[spec.Name] = &serverSession{spec: spec, ret: session, tools: descriptors}
	mgr.mu.Unlock()
	return mgr, spec.Name
}

// TestManagerListCallAndBridge pins the happy path: enumerate → bridge into
// a registry under the mcp_<server>_<tool> namespace → call through the
// registry (full execution path) → result text comes back.
func TestManagerListCallAndBridge(t *testing.T) {
	mgr, _ := newTestSession(t)

	infos := mgr.ToolInfos()
	if len(infos["recon"]) != 2 {
		t.Fatalf("enumerated tools = %d, want 2", len(infos["recon"]))
	}

	registry := tools.NewRegistry()
	mgr.BridgeTools(registry)
	lookup, ok := registry.Get("mcp_recon_lookup")
	if !ok {
		t.Fatalf("bridged tool missing; names known to registry: %v", []string{"mcp_recon_lookup"})
	}
	if !strings.Contains(lookup.Description, `MCP server "recon"`) {
		t.Fatalf("provenance missing from description: %s", lookup.Description)
	}

	// Call through the registry: provenance args ride along.
	res, err := registry.ExecuteContext(t.Context(), "mcp_recon_lookup", map[string]string{"query": "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected tool error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "result-for-example.com") {
		t.Fatalf("output wrong: %s", res.Output)
	}
	if res.Metadata["mcp_server"] != "recon" || res.Metadata["mcp_tool"] != "lookup" {
		t.Fatalf("provenance metadata missing: %v", res.Metadata)
	}

	// Tool-level error (IsError) surfaces as a Call error. (The registry
	// rejects empty-string required params before the tool runs — that gate
	// is pinned by registry_test.go — so exercise IsError via m.Call.)
	if _, err := mgr.Call(context.Background(), "recon", "lookup", map[string]string{"query": ""}); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("IsError call err = %v, want tool-failure error", err)
	}

	// No-arg tool works end to end (the hyphen flattens to an underscore).
	if _, ok := registry.Get("mcp_recon_no_args"); !ok {
		t.Fatalf("bridged no-args tool missing")
	}
}

// TestConnectRejectsOperatorMachineHTTP pins the endpoint guard: an HTTP
// server whose URL classifies as the operator machine is refused before any
// connection.
func TestConnectRejectsOperatorMachineHTTP(t *testing.T) {
	mgr := NewManager()
	t.Cleanup(mgr.Close)
	spec := ServerSpec{
		Name:      "evil",
		Transport: TransportHTTP,
		URL:       "http://127.0.0.1:9137/mcp",
		Scope:     scopeguard.Config{},
	}
	if _, err := mgr.Connect(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "operator machine") {
		t.Fatalf("loopback endpoint err = %v, want operator-machine rejection", err)
	}
}

// TestBridgeNaming pins the sanitize + collision contract: non-namespace
// characters flatten, collisions get numeric suffixes, nothing shadows.
func TestBridgeNaming(t *testing.T) {
	taken := map[string]bool{}
	first := uniqueToolName(taken, sanitizeToolName("mcp_Recon Tools.lookup"))
	second := uniqueToolName(taken, sanitizeToolName("mcp_Recon Tools.lookup"))
	if first != "mcp_recon_tools_lookup" {
		t.Fatalf("sanitize = %q", first)
	}
	if second == first {
		t.Fatalf("collision not resolved: %q", second)
	}
	if !strings.HasPrefix(second, first+"_") {
		t.Fatalf("suffix form wrong: %q", second)
	}
}

// TestCoerceArgs pins the typed-argument coercion: JSON literals pass typed,
// plain strings stay strings, secret-ish keys are the caller's concern (no
// filtering here — masking lives at the projection layer).
func TestCoerceArgs(t *testing.T) {
	out := coerceArgs(map[string]string{
		"query":   "example.com",
		"limit":   "10",
		"ratio":   "0.5",
		"flag":    "true",
		"options": `{"deep":true}`,
		"list":    "[1,2]",
		"weird":   "{not json",
	})
	if out["query"] != "example.com" {
		t.Fatalf("plain string coerced: %v", out["query"])
	}
	if v, ok := out["limit"].(float64); !ok || v != 10 {
		t.Fatalf("number not typed: %v (%T)", out["limit"], out["limit"])
	}
	if v, ok := out["flag"].(bool); !ok || !v {
		t.Fatalf("bool not typed: %v", out["flag"])
	}
	obj, ok := out["options"].(map[string]any)
	if !ok || obj["deep"] != true {
		t.Fatalf("object not typed: %v", out["options"])
	}
	if lst, ok := out["list"].([]any); !ok || len(lst) != 2 {
		t.Fatalf("array not typed: %v", out["list"])
	}
	if out["weird"] != "{not json" {
		t.Fatalf("broken JSON must stay a string: %v", out["weird"])
	}
	if _, err := json.Marshal(out); err != nil {
		t.Fatalf("coerced args must marshal: %v", err)
	}
}

// TestParametersFromSchema pins the flat parameter mapping from the wire
// schema shape (properties/required maps).
func TestParametersFromSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "query text"},
			"limit": map[string]any{"type": "integer"},
		},
		"required": []any{"query"},
	}
	params := parametersFromSchema(schema)
	if len(params) != 2 {
		t.Fatalf("params = %d, want 2", len(params))
	}
	byName := map[string]tools.Parameter{}
	for _, p := range params {
		byName[p.Name] = p
	}
	if !byName["query"].Required || byName["query"].Description != "query text" {
		t.Fatalf("query param wrong: %+v", byName["query"])
	}
	if byName["limit"].Required {
		t.Fatal("limit must not be required")
	}
	if !strings.Contains(byName["limit"].Description, "integer") {
		t.Fatalf("type fallback description missing: %+v", byName["limit"])
	}
	// Non-object schemas degrade to no parameters.
	if params := parametersFromSchema("not-a-map"); params != nil {
		t.Fatalf("string schema = %v, want nil", params)
	}
}
