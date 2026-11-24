package engine

// patch_tool_calls_test.go — A dangling tool_call must be repaired with an
// explicit "not executed" result, and a clean history must pass through
// untouched (same message pointers, no injected messages).

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func rewriteState(t *testing.T, mw adk.ChatModelAgentMiddleware, msgs []*schema.Message) []*schema.Message {
	t.Helper()
	_, state, err := mw.BeforeModelRewriteState(context.Background(), &adk.ChatModelAgentState{Messages: msgs}, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("BeforeModelRewriteState: %v", err)
	}
	return state.Messages
}

func TestPatchToolCallsLeavesCleanHistoryUntouched(t *testing.T) {
	mw, err := newPatchToolCallsMiddleware(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	call := schema.ToolCall{
		ID:       "call-1",
		Type:     "function",
		Function: schema.FunctionCall{Name: "terminal_execute", Arguments: `{"command":"id"}`},
	}
	msgs := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("go"),
		schema.AssistantMessage("running", []schema.ToolCall{call}),
		schema.ToolMessage("uid=0", "call-1", schema.WithToolName("terminal_execute")),
	}
	got := rewriteState(t, mw, msgs)
	if len(got) != len(msgs) {
		t.Fatalf("clean history rewrote message count: %d -> %d", len(msgs), len(got))
	}
	for i := range msgs {
		if got[i] != msgs[i] {
			t.Fatalf("clean message %d was rewritten (%+v -> %+v)", i, msgs[i], got[i])
		}
	}
}

func TestPatchToolCallsRepairsDanglingCall(t *testing.T) {
	mw, err := newPatchToolCallsMiddleware(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	answered := schema.ToolCall{
		ID:       "call-1",
		Type:     "function",
		Function: schema.FunctionCall{Name: "http_request", Arguments: `{"url":"https://t"}`},
	}
	dangling := schema.ToolCall{
		ID:       "call-2",
		Type:     "function",
		Function: schema.FunctionCall{Name: "terminal_execute", Arguments: `{"command":"id"}`},
	}
	msgs := []*schema.Message{
		schema.UserMessage("go"),
		schema.AssistantMessage("running both", []schema.ToolCall{answered, dangling}),
		schema.ToolMessage("200 OK", "call-1", schema.WithToolName("http_request")),
	}
	got := rewriteState(t, mw, msgs)
	if len(got) != len(msgs)+1 {
		t.Fatalf("dangling call not repaired: got %d messages, want %d", len(got), len(msgs)+1)
	}
	// The placeholder is inserted immediately after the assistant turn, before
	// the tool messages that follow it.
	patched := got[2]
	if patched.Role != schema.Tool || patched.ToolCallID != "call-2" {
		t.Fatalf("patched message = %+v, want a tool message for call-2", patched)
	}
	if patched.ToolName != "terminal_execute" {
		t.Fatalf("patched tool name = %q, want terminal_execute", patched.ToolName)
	}
	for _, want := range []string{"未执行", "NOT executed", "审批中断", "context compaction", "terminal_execute"} {
		if !strings.Contains(patched.Content, want) {
			t.Fatalf("patched content missing %q: %q", want, patched.Content)
		}
	}
	// The answered call keeps its original message; only the dangling one is padded.
	if got[0] != msgs[0] || got[1] != msgs[1] || got[3] != msgs[2] {
		t.Fatal("repair touched messages that were already consistent")
	}
}

func TestPatchToolCallsRepairsCallBeforeNonToolMessage(t *testing.T) {
	mw, err := newPatchToolCallsMiddleware(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A tool result separated from its call by a user message does not count
	// (providers reject it the same way): the call is padded where it appears.
	call := schema.ToolCall{
		ID:       "call-9",
		Type:     "function",
		Function: schema.FunctionCall{Name: "browser_action", Arguments: `{"command":"navigate"}`},
	}
	msgs := []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{call}),
		schema.UserMessage("nudge"),
		schema.ToolMessage("late", "call-9", schema.WithToolName("browser_action")),
	}
	got := rewriteState(t, mw, msgs)
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4: %+v", len(got), got)
	}
	if got[1].Role != schema.Tool || got[1].ToolCallID != "call-9" {
		t.Fatalf("padding not inserted directly after the call: %+v", got)
	}
}
