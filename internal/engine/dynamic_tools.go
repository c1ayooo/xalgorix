package engine

// dynamic_tools.go — B7 dynamic tool surface (always enabled; the curated pool
// is deliberately tiny, so ordinary lanes keep their full tool list).
//
// Eino's adk/middlewares/dynamictool/toolsearch middleware does NOT require a
// model with native tool search. With UseModelToolSearch=false (the default we
// use) it degrades to visibility filtering: it adds a `tool_search` meta-tool,
// hides the deferred tools from the model's tool list, and re-adds each one
// once the model has discovered it through tool_search. The middleware also
// injects an <available-deferred-tools> reminder naming every deferred tool, so
// the model knows they exist (same "no phantom gaps" contract as
// Registry.SetSchemaHidden).
//
// That mechanism is only observable when the provider request carries a native
// tool array. In XML fallback mode (RuntimeConfig.NativeTools=false) the engine
// sets wireTools=nil (chatmodel.go) and the full schema travels inside the
// system prompt (agent.prompt → registry.SchemaXML()), so filtering ToolInfos
// changes nothing the model sees. Runtime therefore only mounts the middleware
// when NativeTools is true; otherwise the flag is inert and logged.
//
// Safety contract: a tool can become hidden ONLY by being listed in
// dynamicToolSearchable AND not being in dynamicCoreTools AND not being
// read_/list_/search_ prefixed. Every other tool — including every MCP, plugin
// or operator-authored tool the engine has never heard of — stays visible. That
// is deny-by-curation: an unknown tool is core by default, never hidden.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/dynamictool/toolsearch"
	einoTool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// dynamicCoreTools is the explicit never-hidden set. It is the audit surface
// for "which tools must survive the split because a prompt instruction, hook,
// guard or finish/report/verification gate can name them". It is intentionally
// broader than strictly necessary (宁可少收不可漏收): a name that turns out not
// to be registered in a lane is simply ignored by the split.
var dynamicCoreTools = map[string]struct{}{
	// Finish / report gate. finish is the scan-completion gate and
	// report_vulnerability is the only way a finding reaches the report pipeline;
	// neither may ever hide behind a discovery step.
	"finish":               {},
	"report_vulnerability": {},
	// Evidence / notes / plan: prompt-mandated bookkeeping the gates read.
	"add_note":                {},
	"read_notes":              {},
	"build_plan":              {},
	"update_plan":             {},
	"record_hypothesis":       {},
	"add_hypothesis_evidence": {},
	"update_hypothesis":       {},
	"read_ledger":             {},
	"claim_next_hypothesis":   {},
	"claim_next_task":         {},
	"submit_verdict":          {},
	// Notes / skills surfaces named by the prompt.
	"read_skill":    {},
	"list_skills":   {},
	"search_skills": {},
	// Execution + recon tools the prompt tells the model to call by name.
	"terminal_execute":       {},
	"python_action":          {},
	"http_request":           {},
	"send_request":           {},
	"list_requests":          {},
	"browser_action":         {},
	"page_agent":             {},
	"port_scan":              {},
	"authz_matrix":           {},
	"probe_hypothesis":       {},
	"probe_body_contract":    {},
	"code_search":            {},
	"scan_source_routes":     {},
	"scan_source_sinks":      {},
	"discover_client_routes": {},
	"ingest_har":             {},
	"web_search":             {},
	"cve_search":             {},
	"exploit_search":         {},
	// Out-of-band verification (security chain / proof gates).
	"oob_callback": {},
	"verify_oob":   {},
	// Deterministic confirmation tools.
	"verify_timing":         {},
	"verify_sqli":           {},
	"verify_ssti":           {},
	"verify_path_traversal": {},
	"verify_authbypass":     {},
	"verify_xxe":            {},
	"verify_csrf":           {},
	// Delegation / control tools named by the prompt.
	"create_agent": {},
	"spawn_agent":  {},
	"check_agent":  {},
	"wait_agent":   {},
	"kill_work":    {},
	"steer_work":   {},
	// Graph / session surfaces enumerated by restricted-surface prompts.
	"graph_overview": {},
	"read_plan":      {},
	"add_intent":     {},
	"retire_intent":  {},
	// Engine-instructed retrieval: archived tool output stubs tell the model to
	// call read_tool_output by name, so it must stay directly callable.
	"read_tool_output": {},
}

// dynamicToolSearchable is the curated discovery-gated pool. A tool is only
// hidden when it appears here AND is not in dynamicCoreTools AND is not
// read_/list_/search_ prefixed.
//
// The pool is deliberately small. Almost every tool in the base registry is
// named somewhere in agent_prompt.go / hooks.go / agent_guard.go (grep counts:
// add_note 22, browser_action 30, terminal_execute 36, report_vulnerability 33,
// finish 109, ...), so hiding one would contradict an instruction the model is
// already following. The entry below is the tool with no "must call" reference
// in the scan prompt and hook/gate code:
//
//	str_replace_editor: the prompt explicitly tells the model NOT to use it
//	("Use terminal_execute with cat, grep, or head ... intent"), it exists for
//	whitebox source edits.
//
// record_intelligence used to live here on the theory that the engine projects
// finding InfoItems itself. In practice that projection masks secrets
// (info_items carries "token=***"), so the operator's intelligence view went
// empty: round 7 produced zero record_intelligence calls and the task intel tab
// had nothing to show. The prompt now names it as a must-call for credentials
// and sensitive data, so it is core again — a tool the operator reads from is
// not auxiliary.
//
// Expanding the pool is a per-tool review: add the name only after grepping
// prompt/hook/gate code for a "must call" reference and adding it to the gate
// test's core expectations if the greps show a dependency.
var dynamicToolSearchable = map[string]struct{}{
	"str_replace_editor": {},
}

// namedTool pairs a registry tool adapter with its canonical name so the
// dynamic split preserves the registry's deterministic (sorted) order.
type namedTool struct {
	name string
	tool einoTool.InvokableTool
}

// isSearchableTool reports whether one tool name may be hidden behind
// tool_search. Deny-by-curation: only an explicitly curated name is hidden, and
// a restricted-surface prefix (read_/list_/search_) always wins over the pool.
func isSearchableTool(name string) bool {
	return isSearchableToolIn(name, dynamicToolSearchable)
}

// isSearchableToolIn is the pure rule behind isSearchableTool, taking the pool
// as a parameter so tests can exercise the read_/list_/search_ guard without
// mutating the package-level curated set.
func isSearchableToolIn(name string, searchable map[string]struct{}) bool {
	if _, ok := dynamicCoreTools[name]; ok {
		return false
	}
	if _, ok := searchable[name]; !ok {
		return false
	}
	// Restricted surfaces (main/planner/orchestrator) list exactly the names
	// registry.List() kept, and those allowlists are read_/list_/search_ prefix
	// based. Hiding a prefixed tool would contradict such a prompt, so prefixed
	// names are always core.
	if strings.HasPrefix(name, "read_") || strings.HasPrefix(name, "list_") || strings.HasPrefix(name, "search_") {
		return false
	}
	return true
}

// splitToolSurface partitions one lane's tool adapters. Everything that is not
// explicitly searchable stays in core, which is what makes the split safe for
// tools the engine does not know about.
func splitToolSurface(tools []namedTool) (core, searchable []namedTool) {
	core = make([]namedTool, 0, len(tools))
	searchable = make([]namedTool, 0, len(tools))
	for _, t := range tools {
		if isSearchableTool(t.name) {
			searchable = append(searchable, t)
			continue
		}
		core = append(core, t)
	}
	return core, searchable
}

// newToolSearchMiddleware builds the Eino tool-search middleware over the
// searchable adapters. UseModelToolSearch stays false: this must work on models
// without a native tool-search capability (visibility filtering only).
func newToolSearchMiddleware(ctx context.Context, searchable []namedTool) (adk.ChatModelAgentMiddleware, error) {
	if len(searchable) == 0 {
		return nil, fmt.Errorf("engine: tool search requires at least one searchable tool")
	}
	dynamic := make([]einoTool.BaseTool, 0, len(searchable))
	for _, t := range searchable {
		dynamic = append(dynamic, t.tool)
	}
	mw, err := toolsearch.New(ctx, &toolsearch.Config{DynamicTools: dynamic})
	if err != nil {
		return nil, err
	}
	// tool_search is an Eino-native tool with a typed argument schema
	// (max_results is an integer). The engine's guard layer re-encodes every
	// tool argument through the registry's string contract, which would hand it
	// max_results:"5" and fail to decode. Restore the scalar at the tool edge
	// instead of weakening the contract for every other tool.
	return &toolSearchNativeArgMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		inner:                        mw,
	}, nil
}

// toolSearchNativeArgMiddleware delegates to the Eino tool-search middleware and
// wraps the injected tool_search tool so numeric arguments survive the
// registry-string round trip. toolsearch's own behaviour (hiding deferred tools,
// forward selection) is untouched.
type toolSearchNativeArgMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	inner adk.ChatModelAgentMiddleware
}

func (m *toolSearchNativeArgMiddleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	ctx, out, err := m.inner.BeforeAgent(ctx, runCtx)
	if err != nil || out == nil {
		return ctx, out, err
	}
	toolsOut := make([]einoTool.BaseTool, len(out.Tools))
	copy(toolsOut, out.Tools)
	for i, tool := range toolsOut {
		info, infoErr := tool.Info(ctx)
		if infoErr != nil || info == nil || info.Name != "tool_search" {
			continue
		}
		if invokable, ok := tool.(einoTool.InvokableTool); ok {
			toolsOut[i] = &toolSearchArgsFixer{inner: invokable}
		}
	}
	cloned := *out
	cloned.Tools = toolsOut
	return ctx, &cloned, nil
}

func (m *toolSearchNativeArgMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	return m.inner.BeforeModelRewriteState(ctx, state, mc)
}

// toolSearchArgsFixer restores integer-valued arguments that the guard layer
// re-encoded as JSON strings before calling the real tool_search tool.
type toolSearchArgsFixer struct {
	inner einoTool.InvokableTool
}

func (t *toolSearchArgsFixer) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *toolSearchArgsFixer) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...einoTool.Option) (string, error) {
	return t.inner.InvokableRun(ctx, restoreNumericScalars(argumentsInJSON, "max_results"), opts...)
}

// restoreNumericScalars turns a JSON-string value back into a JSON number for
// the named top-level fields when (and only when) it parses as an integer. Any
// other shape is returned untouched, so this can never corrupt a query string.
func restoreNumericScalars(arguments string, fields ...string) string {
	raw := strings.TrimSpace(arguments)
	if raw == "" || raw == "null" {
		return arguments
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return arguments
	}
	changed := false
	for _, field := range fields {
		value, ok := values[field]
		if !ok {
			continue
		}
		var str string
		if err := json.Unmarshal(value, &str); err != nil {
			continue
		}
		n, err := strconv.Atoi(str)
		if err != nil {
			continue
		}
		values[field] = json.RawMessage(strconv.Itoa(n))
		changed = true
	}
	if !changed {
		return arguments
	}
	out, err := json.Marshal(values)
	if err != nil {
		return arguments
	}
	return string(out)
}

// toolNames extracts the canonical names of a named-tool slice, preserving
// order. Used for tests and diagnostics.
func toolNames(tools []namedTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.name)
	}
	return out
}
