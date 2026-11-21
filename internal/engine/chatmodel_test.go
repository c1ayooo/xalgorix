package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/xalgord/xalgorix/v4/internal/llm"
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
		t.Fatal("stream must stay disabled in the skeleton")
	}
}
