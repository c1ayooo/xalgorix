package engine

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	einoTool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// dynamicFixtureCore are representative tools that must never be hidden: they
// cover the finish/report gate, the evidence/notes ledger, plan + delegation,
// verification, and the execution/recon tools the prompt names.
var dynamicFixtureCore = []string{
	"finish",
	"report_vulnerability",
	"add_note",
	"read_notes",
	"read_ledger",
	"record_hypothesis",
	"add_hypothesis_evidence",
	"claim_next_hypothesis",
	"update_plan",
	"build_plan",
	"submit_verdict",
	"verify_oob",
	"oob_callback",
	"terminal_execute",
	"http_request",
	"browser_action",
	"read_tool_output",
	"read_skill",
	"list_skills",
	"search_skills",
}

// dynamicFixtureSearchable are the curated discovery-gated tools.
var dynamicFixtureSearchable = []string{
	"record_intelligence",
	"str_replace_editor",
}

// dynamicFixtureUnknown are tools the engine has never heard of (MCP/plugin/
// operator-authored). They must stay visible: deny-by-curation.
var dynamicFixtureUnknown = []string{
	"mcp__demo__echo",
	"operator_skill_tool",
}

func dynamicFixtureRegistry(t *testing.T, names ...string) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	for _, name := range names {
		name := name
		reg.Register(&tools.Tool{
			Name:        name,
			Description: "fixture tool " + name,
			Execute: func(map[string]string) (tools.Result, error) {
				return tools.Result{Success: true, Output: name}, nil
			},
		})
	}
	return reg
}

func dynamicTestConfig(reg *tools.Registry, native, dynamic bool) RuntimeConfig {
	return RuntimeConfig{
		Client:          &scriptedClient{},
		Registry:        reg,
		CheckpointStore: NewMemoryCheckpointStore(),
		Instruction:     "test instruction",
		NativeTools:     native,
		DynamicTools:    dynamic,
		Guard:           ToolGuardFunc(func(context.Context, string, map[string]string) GuardVerdict { return GuardVerdict{Allowed: true} }),
	}
}

func namesFromBaseTools(tools []einoTool.BaseTool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		info, err := tool.Info(context.Background())
		if err != nil {
			continue
		}
		out = append(out, info.Name)
	}
	sort.Strings(out)
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func containsAll(haystack, needles []string) (missing []string) {
	set := make(map[string]struct{}, len(haystack))
	for _, h := range haystack {
		set[h] = struct{}{}
	}
	for _, n := range needles {
		if _, ok := set[n]; !ok {
			missing = append(missing, n)
		}
	}
	return missing
}

// TestDynamicToolSurfaceOffKeepsEveryTool pins the default: with the flag off,
// the tool node is byte-for-byte the registry snapshot and no middleware is
// mounted.
func TestDynamicToolSurfaceOffKeepsEveryTool(t *testing.T) {
	all := append(append(append([]string{}, dynamicFixtureCore...), dynamicFixtureSearchable...), dynamicFixtureUnknown...)
	cfg := dynamicTestConfig(dynamicFixtureRegistry(t, all...), true, false)
	rt, err := NewRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rt.toolSearch != nil {
		t.Fatal("tool search middleware mounted while the flag is off")
	}
	if len(rt.dynamicSearchable) != 0 {
		t.Fatalf("dynamic searchable set = %v, want empty", rt.dynamicSearchable)
	}
	got := namesFromBaseTools(rt.ToolsNodeConfig().Tools)
	want := sortedCopy(all)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tool node names = %v, want %v", got, want)
	}
	// ExecuteSequentially is a load-bearing invariant; the split must not touch it.
	if !rt.ToolsNodeConfig().ExecuteSequentially {
		t.Fatal("ExecuteSequentially must stay true with the dynamic surface off")
	}
	for _, h := range rt.handlers {
		if h == nil {
			t.Fatal("nil handler in chain")
		}
	}
}

// TestDynamicToolSurfaceNativeMountsSearchMiddleware is the positive path: the
// core set stays in the static tool node, the searchable set is mounted in the
// middleware, and the middleware's visibility filtering hides exactly the
// searchable set (never a core tool).
func TestDynamicToolSurfaceNativeMountsSearchMiddleware(t *testing.T) {
	all := append(append(append([]string{}, dynamicFixtureCore...), dynamicFixtureSearchable...), dynamicFixtureUnknown...)
	cfg := dynamicTestConfig(dynamicFixtureRegistry(t, all...), true, true)
	rt, err := NewRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rt.toolSearch == nil {
		t.Fatal("tool search middleware was not constructed")
	}
	if !rt.ToolsNodeConfig().ExecuteSequentially {
		t.Fatal("ExecuteSequentially must stay true with the dynamic surface on")
	}

	// The static tool node carries only the core set. Searchable tools are
	// appended by the middleware at run time, so a hidden tool stays callable.
	nodeNames := namesFromBaseTools(rt.ToolsNodeConfig().Tools)
	wantCore := sortedCopy(append(append([]string{}, dynamicFixtureCore...), dynamicFixtureUnknown...))
	if strings.Join(nodeNames, ",") != strings.Join(wantCore, ",") {
		t.Fatalf("static tool node = %v, want core+unknown %v", nodeNames, wantCore)
	}
	if missing := containsAll(nodeNames, dynamicFixtureSearchable); len(missing) == 0 {
		t.Fatalf("searchable tools leaked into the always-visible node: %v", nodeNames)
	}
	if got, want := sortedCopy(rt.dynamicSearchable), sortedCopy(dynamicFixtureSearchable); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("dynamicSearchable = %v, want %v", got, want)
	}

	// BeforeAgent: core + every deferred tool + the tool_search meta-tool are
	// all registered in the tool node.
	runCtx := &adk.ChatModelAgentContext{Tools: append([]einoTool.BaseTool(nil), rt.ToolsNodeConfig().Tools...)}
	_, after, err := rt.toolSearch.BeforeAgent(context.Background(), runCtx)
	if err != nil {
		t.Fatal(err)
	}
	beforeNames := namesFromBaseTools(after.Tools)
	expected := append(append([]string{}, all...), "tool_search")
	if missing := containsAll(beforeNames, expected); len(missing) > 0 {
		t.Fatalf("BeforeAgent tools missing %v (got %v)", missing, beforeNames)
	}

	// BeforeModelRewriteState: the searchable tools disappear from ToolInfos,
	// the core tools and tool_search stay visible, and the reminder names the
	// deferred tools.
	allInfos := make([]*schema.ToolInfo, 0, len(beforeNames))
	for _, n := range beforeNames {
		allInfos = append(allInfos, &schema.ToolInfo{Name: n})
	}
	state := &adk.ChatModelAgentState{ToolInfos: allInfos}
	_, rewritten, err := rt.toolSearch.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	visible := make([]string, 0, len(rewritten.ToolInfos))
	for _, info := range rewritten.ToolInfos {
		visible = append(visible, info.Name)
	}
	if missing := containsAll(visible, append(append([]string{}, dynamicFixtureCore...), dynamicFixtureUnknown...)); len(missing) > 0 {
		t.Fatalf("core tools hidden from the model: %v (visible %v)", missing, visible)
	}
	if missing := containsAll(visible, []string{"tool_search"}); len(missing) > 0 {
		t.Fatal("tool_search must be visible to the model")
	}
	if leaked := containsAll(visible, dynamicFixtureSearchable); len(leaked) == 0 {
		t.Fatalf("searchable tools still visible before discovery: %v", visible)
	}
	var reminder string
	for _, msg := range rewritten.Messages {
		if strings.Contains(msg.Content, "<available-deferred-tools>") {
			reminder = msg.Content
		}
	}
	if reminder == "" {
		t.Fatal("missing <available-deferred-tools> reminder")
	}
	for _, name := range dynamicFixtureSearchable {
		if !strings.Contains(reminder, name) {
			t.Fatalf("reminder does not name deferred tool %q: %s", name, reminder)
		}
	}
}

// TestDynamicToolSurfaceXMLFallbackIsInert pins the guard: the XML fallback
// mode sends no tool array to the provider (chatmodel.go sets wireTools nil and
// the schema travels in the prompt), so mounting the filter would be a lie.
func TestDynamicToolSurfaceXMLFallbackIsInert(t *testing.T) {
	all := append(append(append([]string{}, dynamicFixtureCore...), dynamicFixtureSearchable...), dynamicFixtureUnknown...)
	rt, err := NewRuntime(context.Background(), dynamicTestConfig(dynamicFixtureRegistry(t, all...), false, true))
	if err != nil {
		t.Fatal(err)
	}
	if rt.toolSearch != nil {
		t.Fatal("tool search middleware mounted in XML fallback mode")
	}
	got := namesFromBaseTools(rt.ToolsNodeConfig().Tools)
	if strings.Join(got, ",") != strings.Join(sortedCopy(all), ",") {
		t.Fatalf("XML mode tool node = %v, want every tool", got)
	}
}

// TestDynamicToolSurfaceEmptySearchableIsInert: a lane with nothing curated as
// searchable (e.g. the verifier registry) must not mount a no-op middleware.
func TestDynamicToolSurfaceEmptySearchableIsInert(t *testing.T) {
	rt, err := NewRuntime(context.Background(), dynamicTestConfig(dynamicFixtureRegistry(t, dynamicFixtureCore...), true, true))
	if err != nil {
		t.Fatal(err)
	}
	if rt.toolSearch != nil {
		t.Fatal("tool search middleware mounted with an empty searchable set")
	}
	got := namesFromBaseTools(rt.ToolsNodeConfig().Tools)
	if strings.Join(got, ",") != strings.Join(sortedCopy(dynamicFixtureCore), ",") {
		t.Fatalf("tool node = %v, want %v", got, dynamicFixtureCore)
	}
}

// capturingClient records the tool names the provider sees on each turn, then
// drives one tool_search call so the second turn can prove discovery works.
type capturingClient struct {
	mu       sync.Mutex
	turns    int
	toolSets [][]string
}

func (c *capturingClient) ChatWithUsageToolCalls(_ []llm.Message, tools []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(tools))
	for _, d := range tools {
		names = append(names, d.Name)
	}
	c.toolSets = append(c.toolSets, names)
	c.turns++
	if c.turns == 1 {
		// max_results travels as a string because the engine guard re-encodes
		// every tool argument through the registry's map[string]string contract;
		// the wrapper must restore it to a number or toolsearch cannot decode it.
		return "searching", []llm.ToolCall{{
			ID: "call-1", Name: "tool_search", Args: map[string]string{"query": "select:str_replace_editor", "max_results": "5"},
		}}, &llm.TokenUsage{TotalTokens: 3}, nil
	}
	return "done", nil, &llm.TokenUsage{TotalTokens: 4}, nil
}

// TestDynamicToolSurfaceEndToEndHidesThenReveals runs the real ADK loop with a
// fake provider and proves the visibility-filtering degradation works without
// any native model tool-search support: turn 1 sees tool_search + core (never
// the deferred tool), and after the tool_search result turn 2 sees the tool it
// selected.
func TestDynamicToolSurfaceEndToEndHidesThenReveals(t *testing.T) {
	reg := dynamicFixtureRegistry(t, "finish", "report_vulnerability", "str_replace_editor")
	client := &capturingClient{}
	cfg := dynamicTestConfig(reg, true, true)
	cfg.Client = client
	rt, err := NewRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	iter := rt.Run(context.Background(), []*schema.Message{schema.UserMessage("start")}, "cp-dynamic")
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			t.Fatalf("run error: %v", ev.Err)
		}
	}
	client.mu.Lock()
	sets := client.toolSets
	client.mu.Unlock()
	if len(sets) < 2 {
		t.Fatalf("expected at least 2 model turns, got %d (%v)", len(sets), sets)
	}
	turn1 := sets[0]
	if missing := containsAll(turn1, []string{"tool_search", "finish", "report_vulnerability"}); len(missing) > 0 {
		t.Fatalf("turn 1 missing %v (saw %v)", missing, turn1)
	}
	if leaked := containsAll(turn1, []string{"str_replace_editor"}); len(leaked) == 0 {
		t.Fatalf("deferred tool visible before discovery on turn 1: %v", turn1)
	}
	turn2 := sets[1]
	if missing := containsAll(turn2, []string{"str_replace_editor", "finish", "report_vulnerability", "tool_search"}); len(missing) > 0 {
		t.Fatalf("turn 2 missing %v (saw %v) — tool_search did not reveal the deferred tool", missing, turn2)
	}
}

// TestRestoreNumericScalars pins the guard-string round-trip fix: only the
// named field is converted, only when it is an integer string.
func TestRestoreNumericScalars(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"query":"select:x","max_results":"5"}`, `{"max_results":5,"query":"select:x"}`},
		{`{"query":"terminal","max_results":5}`, `{"query":"terminal","max_results":5}`}, // already numeric
		{`{"query":"max_results","max_results":"5x"}`, `{"query":"max_results","max_results":"5x"}`},
		{`{"query":"q"}`, `{"query":"q"}`},
		{`not json`, `not json`},
	}
	for _, tc := range cases {
		got := restoreNumericScalars(tc.in, "max_results")
		if got != tc.want {
			t.Fatalf("restoreNumericScalars(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// A field not named must never be touched.
	if got := restoreNumericScalars(`{"other":"5"}`, "max_results"); got != `{"other":"5"}` {
		t.Fatalf("unrelated field changed: %q", got)
	}
}

// TestDynamicSplitNeverHidesGateTools is the design pin demanded by B7 §4: the
// gate/prompt dependency set is core for every known registry name, and no
// curated searchable name may coincide with it.
func TestDynamicSplitNeverHidesGateTools(t *testing.T) {
	// Gate/prompt dependencies gathered from internal/agent (hooks.go
	// localOnlyTools/isAdministrativeTool, agent_prompt.go prose, agent_guard.go
	// name switches, tool_surface.go restricted allowlists) and internal/tools.
	gateRequired := []string{
		"finish", "report_vulnerability",
		"add_note", "read_notes", "read_ledger", "record_hypothesis",
		"add_hypothesis_evidence", "update_hypothesis", "claim_next_hypothesis",
		"claim_next_task", "build_plan", "update_plan", "submit_verdict",
		"verify_oob", "oob_callback", "verify_timing", "verify_sqli", "verify_ssti",
		"verify_path_traversal", "verify_authbypass", "verify_xxe", "verify_csrf",
		"terminal_execute", "python_action", "http_request", "send_request",
		"list_requests", "browser_action", "page_agent", "port_scan",
		"authz_matrix", "probe_hypothesis", "probe_body_contract", "code_search",
		"scan_source_routes", "scan_source_sinks", "discover_client_routes",
		"ingest_har", "web_search", "cve_search", "exploit_search",
		"read_skill", "list_skills", "search_skills",
		"read_tool_output",
		"spawn_agent", "create_agent", "check_agent", "wait_agent",
		"kill_work", "steer_work",
	}
	for _, name := range gateRequired {
		if isSearchableTool(name) {
			t.Fatalf("gate-required tool %q is classified searchable", name)
		}
		if _, ok := dynamicCoreTools[name]; !ok {
			t.Fatalf("gate-required tool %q is missing from dynamicCoreTools", name)
		}
	}
	for name := range dynamicToolSearchable {
		if _, ok := dynamicCoreTools[name]; ok {
			t.Fatalf("%q is in both the core and searchable sets", name)
		}
		for _, gate := range gateRequired {
			if name == gate {
				t.Fatalf("searchable tool %q is gate-required", name)
			}
		}
	}
}

// TestDynamicSplitDefaultsUnknownToolsToCore: deny-by-curation — an unknown
// tool name is never hidden.
func TestDynamicSplitDefaultsUnknownToolsToCore(t *testing.T) {
	for _, name := range dynamicFixtureUnknown {
		if isSearchableTool(name) {
			t.Fatalf("unknown tool %q must default to core", name)
		}
	}
	prefixedPool := map[string]struct{}{"read_custom": {}, "list_custom": {}, "search_custom": {}, "custom_tool": {}}
	for _, name := range []string{"read_custom", "list_custom", "search_custom"} {
		if isSearchableToolIn(name, prefixedPool) {
			t.Fatalf("read_/list_/search_ prefixed tool %q must stay core", name)
		}
	}
	if !isSearchableToolIn("custom_tool", prefixedPool) {
		t.Fatal("an unprefixed curated tool must be searchable")
	}
	core, searchable := splitToolSurface([]namedTool{{name: "finish"}, {name: "mcp__x__y"}, {name: "record_intelligence"}})
	if got := toolNames(core); strings.Join(got, ",") != "finish,mcp__x__y" {
		t.Fatalf("core = %v", got)
	}
	if got := toolNames(searchable); strings.Join(got, ",") != "record_intelligence" {
		t.Fatalf("searchable = %v", got)
	}
}
