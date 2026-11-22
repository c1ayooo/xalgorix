package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
)

type fakeChatClient struct {
	messages []llm.Message
	tools    []llm.ToolDefinition
	content  string
	calls    []llm.ToolCall
	usage    *llm.TokenUsage
	err      error
}

func (f *fakeChatClient) ChatWithUsageToolCalls(messages []llm.Message, tools []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	f.messages = append([]llm.Message(nil), messages...)
	f.tools = append([]llm.ToolDefinition(nil), tools...)
	return f.content, f.calls, f.usage, f.err
}

func TestChatModelGenerateMapsMessagesToolsAndUsage(t *testing.T) {
	client := &fakeChatClient{
		content: "calling the scanner",
		calls: []llm.ToolCall{{
			ID: "call-1", Name: "scan_target",
			Args: map[string]string{"target": "example.com"},
		}},
		usage: &llm.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18, CachedTokens: 3},
	}
	base := NewChatModel(client)
	bound, err := base.WithTools([]*schema.ToolInfo{{
		Name: "scan_target",
		Desc: "Scan one target",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"target": {Type: schema.String, Desc: "hostname", Required: true},
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(base.tools) != 0 {
		t.Fatal("WithTools mutated the base model")
	}
	if got := bound.(*ChatModel).tools; len(got) != 1 || got[0].Name != "scan_target" || len(got[0].Parameters) != 1 {
		t.Fatalf("bound tools = %+v", got)
	}

	msg, err := bound.Generate(context.Background(), []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("go"),
		schema.ToolMessage("tool output", "call-0", schema.WithToolName("scan_target")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.messages) != 3 || client.messages[1].Role != string(schema.User) ||
		client.messages[2].ToolCallID != "call-0" {
		t.Fatalf("converted input = %+v", client.messages)
	}
	if msg.Role != schema.Assistant || msg.Content != "calling the scanner" || len(msg.ToolCalls) != 1 {
		t.Fatalf("converted output = %+v", msg)
	}
	call := msg.ToolCalls[0]
	if call.ID != "call-1" || call.Function.Name != "scan_target" ||
		!strings.Contains(call.Function.Arguments, `"target":"example.com"`) {
		t.Fatalf("tool call mapping = %+v", call)
	}
	if msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil ||
		msg.ResponseMeta.Usage.TotalTokens != 18 || msg.ResponseMeta.Usage.PromptTokenDetails.CachedTokens != 3 {
		t.Fatalf("usage mapping = %+v", msg.ResponseMeta)
	}
}

func TestChatModelUsesRawArgumentsAndPropagatesErrors(t *testing.T) {
	client := &fakeChatClient{calls: []llm.ToolCall{{
		ID: "call-2", Name: "raw", RawArguments: `{"a":1,"b":"x"}`,
	}}}
	msg, err := NewChatModel(client).Generate(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.ToolCalls[0].Function.Arguments; got != `{"a":1,"b":"x"}` {
		t.Fatalf("raw arguments = %q", got)
	}

	wantErr := errors.New("provider down")
	client.err = wantErr
	if _, err := NewChatModel(client).Generate(context.Background(), []*schema.Message{schema.UserMessage("go")}); !errors.Is(err, wantErr) {
		t.Fatalf("generate error = %v, want %v", err, wantErr)
	}
	if _, err := NewChatModel(nil).Generate(context.Background(), nil); err == nil {
		t.Fatal("nil client must fail")
	}
	if _, err := NewChatModel(client).Stream(context.Background(), nil); err == nil {
		t.Fatal("stream must stay disabled")
	}
}

// TestChatModelXMLFallbackKeepsEinoToolProtocol pins the non-OpenAI provider
// path: the adapter sends the text dialect (no tools array), parses the model's
// XML back into schema.ToolCalls for the ADK, and renders prior tool history
// back into text instead of leaking native tool_calls to a protocol that does
// not understand them.
func TestChatModelXMLFallbackKeepsEinoToolProtocol(t *testing.T) {
	client := &fakeChatClient{
		content: `<function=finish><parameter=summary>fallback works</parameter></function>`,
	}
	model := NewChatModel(client).WithNativeTools(false)
	msg, err := model.Generate(context.Background(), []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("go"),
		{Role: schema.Assistant, Content: "prior", ToolCalls: []schema.ToolCall{{
			ID: "call-1", Type: "function",
			Function: schema.FunctionCall{Name: "read_notes", Arguments: `{"id":"n1"}`},
		}}},
		schema.ToolMessage("note body", "call-1", schema.WithToolName("read_notes")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.tools) != 0 {
		t.Fatalf("XML fallback sent a native tools array: %+v", client.tools)
	}
	if len(client.messages) != 4 {
		t.Fatalf("provider messages = %+v", client.messages)
	}
	if !strings.Contains(client.messages[2].Content, "<function=read_notes>") ||
		!strings.Contains(client.messages[2].Content, "<parameter=id>n1</parameter>") {
		t.Fatalf("assistant tool call was not rendered to XML: %q", client.messages[2].Content)
	}
	if client.messages[3].Role != "user" || !strings.Contains(client.messages[3].Content, "[TOOL RESULT call-1]") {
		t.Fatalf("tool result was not rendered to text: %+v", client.messages[3])
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "finish" {
		t.Fatalf("XML response was not converted to an Eino tool call: %+v", msg)
	}
	if !strings.Contains(msg.ToolCalls[0].Function.Arguments, "fallback works") {
		t.Fatalf("tool arguments = %q", msg.ToolCalls[0].Function.Arguments)
	}
}

// fakeMetaChatClient 是带 reasoning 捕获能力的 fake（*llm.Client 的形状）。
type fakeMetaChatClient struct {
	fakeChatClient
	reasoning string
}

func (f *fakeMetaChatClient) ChatWithUsageToolCallsMeta(messages []llm.Message, tools []llm.ToolDefinition) (string, string, []llm.ToolCall, *llm.TokenUsage, error) {
	f.messages = append([]llm.Message(nil), messages...)
	return f.content, f.reasoning, f.calls, f.usage, f.err
}

// TestChatModelOnReasoning: 接受的响应触发回调一次；无 meta 能力的 client
// 走旧路径——不触发回调、也不报错（能力探针语义）。
func TestChatModelOnReasoning(t *testing.T) {
	var got []string
	client := &fakeMetaChatClient{
		fakeChatClient: fakeChatClient{content: "done"},
		reasoning:      "先侦察再注入",
	}
	model := NewChatModel(client).WithOnReasoning(func(s string) { got = append(got, s) })
	if _, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("go")}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "先侦察再注入" {
		t.Fatalf("onReasoning = %v, want exactly one capture", got)
	}

	// 只有 ChatClient 能力的 fake：能力探针回落旧路径。
	var none []string
	legacy := NewChatModel(&fakeChatClient{content: "done"}).
		WithOnReasoning(func(s string) { none = append(none, s) })
	if _, err := legacy.Generate(context.Background(), []*schema.Message{schema.UserMessage("go")}); err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("legacy client must not fire reasoning, got %v", none)
	}
}
