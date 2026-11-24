package engine

// tool_compat_test.go — A2/A3 coverage: the built-in alias table resolves only
// to real tools, argument normalization never renames or drops fields, and the
// wired runtime dispatches an alias while the guard sees the canonical name.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

func TestBuildToolAliasesOnlyRegistersRealTools(t *testing.T) {
	configs, reverse := buildToolAliases([]string{"terminal_execute", "http_request", "add_note"}, nil)
	if got, want := configs["terminal_execute"].NameAliases, []string{"terminal", "bash"}; !equalStrings(got, want) {
		t.Fatalf("terminal_execute aliases = %v, want %v", got, want)
	}
	if got := configs["http_request"].NameAliases; !equalStrings(got, []string{"http"}) {
		t.Fatalf("http_request aliases = %v, want [http]", got)
	}
	if _, exists := configs["browser_action"]; exists {
		t.Fatal("alias config for an unregistered tool must be dropped")
	}
	if _, exists := configs["python_action"]; exists {
		t.Fatal("alias config for an unregistered tool must be dropped")
	}
	if canonical := canonicalToolName("bash", reverse); canonical != "terminal_execute" {
		t.Fatalf("bash resolved to %q, want terminal_execute", canonical)
	}
	if canonical := canonicalToolName("nmap", reverse); canonical != "nmap" {
		t.Fatalf("unknown name must pass through, got %q", canonical)
	}
}

func TestBuildToolAliasesDropsConflictingAliases(t *testing.T) {
	// "bash" is itself a registered tool in this lane: Eino rejects a table
	// where an alias collides with a canonical name, so it must be filtered.
	configs, reverse := buildToolAliases([]string{"terminal_execute", "bash"}, nil)
	if got := configs["terminal_execute"].NameAliases; !equalStrings(got, []string{"terminal"}) {
		t.Fatalf("aliases = %v, want [terminal] after dropping the conflicting bash", got)
	}
	if _, exists := reverse["bash"]; exists {
		t.Fatal("conflicting alias leaked into the reverse map")
	}

	// Two canonicals claiming the same alias: first sorted canonical wins,
	// deterministically, and the second loses the alias.
	configs, _ = buildToolAliases(
		[]string{"http_request", "terminal_execute"},
		map[string][]string{"http_request": {"fetch"}, "terminal_execute": {"fetch"}},
	)
	if !equalStrings(configs["http_request"].NameAliases, []string{"fetch"}) {
		t.Fatalf("http_request should keep fetch: %v", configs["http_request"].NameAliases)
	}
	if _, exists := configs["terminal_execute"]; exists {
		t.Fatal("duplicate alias must not be registered twice")
	}
}

func TestBuildToolAliasesOverrideAndDisable(t *testing.T) {
	configs, reverse := buildToolAliases([]string{"http_request"}, map[string][]string{"http_request": {"web_get"}})
	if !equalStrings(configs["http_request"].NameAliases, []string{"web_get"}) {
		t.Fatalf("override not applied: %v", configs["http_request"].NameAliases)
	}
	if _, exists := reverse["http"]; exists {
		t.Fatal("override must replace, not extend, the built-in table")
	}

	configs, reverse = buildToolAliases([]string{"terminal_execute"}, map[string][]string{})
	if configs != nil || reverse != nil {
		t.Fatalf("empty override must disable aliasing, got %+v / %+v", configs, reverse)
	}
}

func TestNormalizeToolArguments(t *testing.T) {
	in := `{"command":"  id -u  ","timeout":30,"follow":false,"obj":{"a":1},"arr":[1,2],"nil":null}`
	out, err := normalizeToolArguments(context.Background(), "http_request", in)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(out), &values); err != nil {
		t.Fatalf("normalized args are not JSON: %v (%s)", err, out)
	}
	if values["command"] != "id -u" {
		t.Fatalf("string not trimmed: %q", values["command"])
	}
	if values["timeout"] != "30" {
		t.Fatalf("number not stringified: %#v", values["timeout"])
	}
	if values["follow"] != "false" {
		t.Fatalf("bool not stringified: %#v", values["follow"])
	}
	if _, ok := values["obj"].(map[string]any); !ok {
		t.Fatalf("structured value was rewritten: %#v", values["obj"])
	}
	if _, ok := values["arr"].([]any); !ok {
		t.Fatalf("array value was rewritten: %#v", values["arr"])
	}
	if values["nil"] != nil {
		t.Fatalf("null value was rewritten: %#v", values["nil"])
	}
	for _, key := range []string{"command", "timeout", "follow", "obj", "arr", "nil"} {
		if _, exists := values[key]; !exists {
			t.Fatalf("field %q was dropped", key)
		}
	}
}

func TestNormalizeToolArgumentsLeavesCleanInputByteIdentical(t *testing.T) {
	for _, in := range []string{
		`{"command":"id -u"}`,
		`{"url":"https://t/x","method":"GET"}`,
		``,
		`null`,
		`not json`,
		`[1,2,3]`,
	} {
		out, err := normalizeToolArguments(context.Background(), "x", in)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", in, err)
		}
		if out != in {
			t.Fatalf("clean/invalid input rewritten: %q -> %q", in, out)
		}
	}
}

func TestNormalizeToolArgumentsLeavesWhitespaceSensitivePayloads(t *testing.T) {
	in := `{"command":"  str_replace  ","old_str":"  fn main() {\n","new_str":"\tfn main() {\n","code":"  if x:\n    y\n","body":" a=b \n","text":" admin ","value":"  note  ","path":"  /tmp/x  "}`
	out, err := normalizeToolArguments(context.Background(), "str_replace_editor", in)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(out), &values); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"old_str", "new_str", "code", "body", "text", "value"} {
		var original map[string]string
		if err := json.Unmarshal([]byte(in), &original); err != nil {
			t.Fatal(err)
		}
		if values[key] != original[key] {
			t.Fatalf("payload arg %q was rewritten: %q -> %q", key, original[key], values[key])
		}
	}
	if values["command"] != "str_replace" || values["path"] != "/tmp/x" {
		t.Fatalf("control args not trimmed: %+v", values)
	}
}

func TestNormalizeToolArgumentsKeepsEmptyRequiredValue(t *testing.T) {
	out, err := normalizeToolArguments(context.Background(), "terminal_execute", `{"command":"   "}`)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(out), &values); err != nil {
		t.Fatal(err)
	}
	if value, exists := values["command"]; !exists || value != "" {
		t.Fatalf("required field must stay present (empty), got %+v", values)
	}
}

type aliasToolClient struct {
	turns  int
	second []llm.Message
}

func (c *aliasToolClient) ChatWithUsageToolCalls(messages []llm.Message, _ []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	c.turns++
	if c.turns == 1 {
		return "", []llm.ToolCall{{
			ID: "call-1", Name: "bash", Args: map[string]string{"command": "  id -u  "},
		}}, nil, nil
	}
	c.second = append([]llm.Message(nil), messages...)
	return "done", nil, nil, nil
}

// TestRuntimeDispatchesAliasWithCanonicalGuardName proves the ToolsNodeConfig
// alias wiring end-to-end: the model calls "bash", the registered
// terminal_execute tool runs with normalized args, and the guard (which gates
// scope/phase policy by name) sees "terminal_execute".
func TestRuntimeDispatchesAliasWithCanonicalGuardName(t *testing.T) {
	reg := tools.NewRegistry()
	var executed map[string]string
	reg.Register(&tools.Tool{
		Name: "terminal_execute", Description: "run a command",
		Parameters: []tools.Parameter{{Name: "command", Required: true}},
		Execute: func(args map[string]string) (tools.Result, error) {
			executed = args
			return tools.Result{Success: true, Output: "uid=0(root)"}, nil
		},
	})
	var guardName string
	var guardArgs map[string]string
	guard := ToolGuardFunc(func(_ context.Context, name string, args map[string]string) GuardVerdict {
		guardName = name
		guardArgs = args
		return GuardVerdict{Allowed: true}
	})
	client := &aliasToolClient{}
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: client, Registry: reg, CheckpointStore: NewMemoryCheckpointStore(),
		Instruction: "test", MaxIterations: 5, Guard: guard,
	})
	if err != nil {
		t.Fatalf("alias table rejected by the tool node: %v", err)
	}
	cfg := rt.ToolsNodeConfig()
	if got := cfg.ToolAliases["terminal_execute"].NameAliases; !equalStrings(got, []string{"terminal", "bash"}) {
		t.Fatalf("runtime ToolAliases = %v", cfg.ToolAliases)
	}
	if !cfg.ExecuteSequentially {
		t.Fatal("ExecuteSequentially must stay true")
	}

	drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "alias-cp"))
	if executed["command"] != "id -u" {
		t.Fatalf("tool did not execute with normalized args: %+v", executed)
	}
	if guardName != "terminal_execute" {
		t.Fatalf("guard saw %q, want canonical terminal_execute", guardName)
	}
	if guardArgs["command"] != "id -u" {
		t.Fatalf("guard args not normalized: %+v", guardArgs)
	}
	if client.turns < 2 {
		t.Fatalf("model never received the tool result (turns=%d)", client.turns)
	}
	found := false
	for _, msg := range client.second {
		// The default adapter renders tool results in the provider-neutral XML
		// dialect (cfg.NativeTools is false here), so match on content.
		if strings.Contains(msg.Content, "uid=0") && msg.Role != "assistant" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool result missing from the next model turn: %#v", client.second)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
