package engine

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
)

// fakeStreamClient is a ChatClient that also exposes the streaming seam.
type fakeStreamClient struct {
	fakeChatClient
	chunks   []llm.StreamChunk
	gotInput []llm.Message
	gotTools []llm.ToolDefinition
}

func (f *fakeStreamClient) ChatStreamWithTools(messages []llm.Message, tools []llm.ToolDefinition) <-chan llm.StreamChunk {
	f.gotInput = append([]llm.Message(nil), messages...)
	f.gotTools = append([]llm.ToolDefinition(nil), tools...)
	ch := make(chan llm.StreamChunk, len(f.chunks))
	for _, chunk := range f.chunks {
		ch <- chunk
	}
	close(ch)
	return ch
}

// blockingStreamClient sends one delta then parks until release is closed,
// which models a provider that is still streaming when the caller cancels.
type blockingStreamClient struct {
	fakeChatClient
	release chan struct{}
}

func (b *blockingStreamClient) ChatStreamWithTools([]llm.Message, []llm.ToolDefinition) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Content: "partial"}
	go func() {
		defer close(ch)
		<-b.release
	}()
	return ch
}

func recvAll(t *testing.T, reader *schema.StreamReader[*schema.Message]) []*schema.Message {
	t.Helper()
	defer reader.Close()
	var out []*schema.Message
	for {
		msg, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("stream recv: %v", err)
		}
		out = append(out, msg)
	}
}

// TestChatModelStream_TextDeltasAndUsageOnce covers the incremental text path:
// every delta is surfaced as its own frame, the final frame carries the single
// usage snapshot, and the concatenation equals the full reply.
func TestChatModelStream_TextDeltasAndUsageOnce(t *testing.T) {
	client := &fakeStreamClient{chunks: []llm.StreamChunk{
		{Content: "hel"},
		{Content: "lo"},
		{Done: true, Usage: &llm.TokenUsage{PromptTokens: 10, CompletionTokens: 6, TotalTokens: 16, CachedTokens: 4}},
	}}
	model := NewChatModel(client)

	reader, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	frames := recvAll(t, reader)
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3 (%+v)", len(frames), frames)
	}
	var text strings.Builder
	usageFrames := 0
	for _, frame := range frames {
		if frame.Role != schema.Assistant {
			t.Fatalf("frame role = %q, want assistant", frame.Role)
		}
		text.WriteString(frame.Content)
		if frame.ResponseMeta != nil && frame.ResponseMeta.Usage != nil {
			usageFrames++
		}
	}
	if text.String() != "hello" {
		t.Fatalf("concatenated text = %q, want %q", text.String(), "hello")
	}
	if usageFrames != 1 {
		t.Fatalf("frames carrying usage = %d, want exactly 1", usageFrames)
	}
	last := frames[len(frames)-1]
	if last.ResponseMeta == nil || last.ResponseMeta.Usage == nil {
		t.Fatalf("final frame missing usage: %+v", last)
	}
	u := last.ResponseMeta.Usage
	if u.PromptTokens != 10 || u.CompletionTokens != 6 || u.TotalTokens != 16 || u.PromptTokenDetails.CachedTokens != 4 {
		t.Fatalf("final usage = %+v", u)
	}
	if last.ResponseMeta.FinishReason != "stop" {
		t.Fatalf("finish reason = %q, want stop", last.ResponseMeta.FinishReason)
	}
	if len(client.gotInput) != 1 || client.gotInput[0].Role != "user" || client.gotInput[0].Content != "hi" {
		t.Fatalf("provider input = %+v", client.gotInput)
	}
}

// TestChatModelStream_AssemblesToolCalls proves fragments are joined by index
// and delivered complete on the last frame, in provider order.
func TestChatModelStream_AssemblesToolCalls(t *testing.T) {
	client := &fakeStreamClient{chunks: []llm.StreamChunk{
		{ToolCalls: []llm.StreamToolCall{{Index: 0, ID: "call_1", Name: "scan_target", Arguments: `{"tar`}}},
		{ToolCalls: []llm.StreamToolCall{{Index: 0, Arguments: `get":"example.com"}`}}},
		{ToolCalls: []llm.StreamToolCall{{Index: 1, ID: "call_2", Name: "note", Arguments: `{"body":"ok"}`}}},
		{Done: true, Usage: &llm.TokenUsage{PromptTokens: 3, CompletionTokens: 9, TotalTokens: 12}},
	}}
	bound, err := NewChatModel(client).WithTools([]*schema.ToolInfo{{
		Name: "scan_target",
		Desc: "Scan one target",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"target": {Type: schema.String, Desc: "hostname", Required: true},
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}

	reader, err := bound.Stream(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	frames := recvAll(t, reader)
	if len(client.gotTools) != 1 || client.gotTools[0].Name != "scan_target" {
		t.Fatalf("streamed tool definitions = %+v, want scan_target", client.gotTools)
	}
	last := frames[len(frames)-1]
	if len(last.ToolCalls) != 2 {
		t.Fatalf("final tool calls = %+v, want 2", last.ToolCalls)
	}
	if last.ToolCalls[0].ID != "call_1" || last.ToolCalls[0].Function.Name != "scan_target" ||
		last.ToolCalls[0].Function.Arguments != `{"target":"example.com"}` {
		t.Fatalf("first call = %+v", last.ToolCalls[0])
	}
	if last.ToolCalls[1].ID != "call_2" || last.ToolCalls[1].Function.Name != "note" ||
		last.ToolCalls[1].Function.Arguments != `{"body":"ok"}` {
		t.Fatalf("second call = %+v", last.ToolCalls[1])
	}
	if last.ResponseMeta == nil || last.ResponseMeta.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %+v, want tool_calls", last.ResponseMeta)
	}
}

// TestChatModelStream_CancelClosesStream asserts a cancelled ctx terminates
// the reader with the ctx error instead of hanging or returning io.EOF.
func TestChatModelStream_CancelClosesStream(t *testing.T) {
	client := &blockingStreamClient{release: make(chan struct{})}
	model := NewChatModel(client)

	ctx, cancel := context.WithCancel(context.Background())
	reader, err := model.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer reader.Close()

	first, err := reader.Recv()
	if err != nil {
		t.Fatalf("first recv: %v", err)
	}
	if first.Content != "partial" {
		t.Fatalf("first frame = %q, want partial", first.Content)
	}

	cancel()
	if _, err := reader.Recv(); !errors.Is(err, context.Canceled) {
		t.Fatalf("recv after cancel = %v, want context.Canceled", err)
	}
	close(client.release)
}

// TestChatModelStream_XMLFallbackBuffersAndParsesCalls covers the
// nativeTools=false path: text is buffered so the XML tool dialect can be
// stripped, and the final frame carries both cleaned content and the call.
func TestChatModelStream_XMLFallbackBuffersAndParsesCalls(t *testing.T) {
	client := &fakeStreamClient{chunks: []llm.StreamChunk{
		{Content: "working "},
		{Content: `<function=scan_target><parameter=target>example.com</parameter></function>`},
		{Done: true, Usage: &llm.TokenUsage{PromptTokens: 5, CompletionTokens: 4, TotalTokens: 9}},
	}}
	model := NewChatModel(client).WithNativeTools(false)

	reader, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	frames := recvAll(t, reader)
	if len(client.gotTools) != 0 {
		t.Fatalf("XML fallback sent native tools: %+v", client.gotTools)
	}
	// The buffered path emits exactly one frame: the completed message.
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1 (%+v)", len(frames), frames)
	}
	final := frames[0]
	if final.Content != "working" {
		t.Fatalf("final content = %q, want %q (XML stripped)", final.Content, "working")
	}
	if len(final.ToolCalls) != 1 || final.ToolCalls[0].Function.Name != "scan_target" ||
		final.ToolCalls[0].Function.Arguments != `{"target":"example.com"}` {
		t.Fatalf("parsed tool calls = %+v", final.ToolCalls)
	}
	if final.ToolCalls[0].ID == "" {
		t.Fatal("XML fallback call kept an empty id")
	}
}

// TestChatModelStream_DegradesForNonStreamingClient pins the fallback: a
// ChatClient without ChatStreamWithTools yields one complete frame instead of
// failing the turn (streaming is an optimization, not a requirement).
func TestChatModelStream_DegradesForNonStreamingClient(t *testing.T) {
	model := NewChatModel(&fakeChatClient{content: "x"})
	reader, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream on a non-streaming client: %v", err)
	}
	frames := recvAll(t, reader)
	if len(frames) != 1 || frames[0].Content != "x" {
		t.Fatalf("fallback frames = %+v, want one message with content x", frames)
	}
}
