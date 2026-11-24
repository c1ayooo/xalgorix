package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

// newAnthropicStreamClient wires a Client to srv and forces the Anthropic
// code path so the SSE accounting branch is exercised.
func newAnthropicStreamClient(t *testing.T, srvURL string) *Client {
	t.Helper()
	cfg := &config.Config{LLM: "claude-3-5-sonnet", APIBase: srvURL, APIKey: "test"}
	c := NewClient(cfg)
	c.provider = "anthropic"
	return c
}

// TestAnthropicChatStream_CountsOutputTokensOnce pins the fix for the
// streaming double count: message_start advertises an in-flight output_tokens
// estimate (Anthropic sends 1) and message_delta restates the final cumulative
// count. Both events used to be added, so a 45-token reply was billed as 46.
// The merged snapshot must also reach the terminal chunk for the adapter.
func TestAnthropicChatStream_CountsOutputTokensOnce(t *testing.T) {
	sse := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet","usage":{"input_tokens":120,"output_tokens":1,"cache_read_input_tokens":30}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":45}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	c := newAnthropicStreamClient(t, srv.URL)
	var (
		content strings.Builder
		done    *StreamChunk
	)
	for chunk := range c.ChatStream([]Message{{Role: "user", Content: "hi"}}) {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		content.WriteString(chunk.Content)
		if chunk.Done {
			cp := chunk
			done = &cp
		}
	}
	if content.String() != "hello" {
		t.Fatalf("streamed content = %q, want %q", content.String(), "hello")
	}
	if done == nil || done.Usage == nil {
		t.Fatalf("terminal chunk missing usage: %+v", done)
	}
	in, out, total := c.GetTokens()
	if in != 120 || out != 45 || total != 165 {
		t.Fatalf("streaming usage double-counted: in=%d out=%d total=%d (want 120/45/165; "+
			"46 means message_start output was added to message_delta)", in, out, total)
	}
	if cached := c.GetCachedTokens(); cached != 30 {
		t.Fatalf("streaming cached tokens = %d, want 30", cached)
	}
	u := done.Usage
	if u.PromptTokens != 120 || u.CompletionTokens != 45 || u.TotalTokens != 165 || u.CachedTokens != 30 {
		t.Fatalf("terminal chunk usage = %+v, want prompt=120 completion=45 total=165 cached=30", u)
	}
}

// TestAnthropicChatStream_FallsBackToMessageStartOutput covers providers that
// close the stream without a message_delta: the provisional message_start
// count is all we have and must still be committed exactly once.
func TestAnthropicChatStream_FallsBackToMessageStartOutput(t *testing.T) {
	sse := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":2}}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	c := newAnthropicStreamClient(t, srv.URL)
	for chunk := range c.ChatStream([]Message{{Role: "user", Content: "hi"}}) {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}
	in, out, total := c.GetTokens()
	if in != 9 || out != 2 || total != 11 {
		t.Fatalf("fallback usage = in=%d out=%d total=%d, want 9/2/11", in, out, total)
	}
}

// TestChatStreamWithTools_EmitsToolCallFragments proves the streaming channel
// carries the native tools array and surfaces tool-call fragments keyed by
// index, so an adapter can concatenate the arguments JSON.
func TestChatStreamWithTools_EmitsToolCallFragments(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"scanning "}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"content":"now"}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"scan_target","arguments":"{\"tar"}}]}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"get\":\"example.com\"}"}}]}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var sentTools []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, tool := range body.Tools {
			sentTools = append(sentTools, tool.Function.Name)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	c := NewClient(&config.Config{LLM: "gpt-4o-mini", APIBase: srv.URL, APIKey: "test"})
	tools := []ToolDefinition{{
		Name:        "scan_target",
		Description: "Scan one target",
		Parameters:  []ToolParameter{{Name: "target", Description: "hostname", Required: true}},
	}}

	var (
		content   strings.Builder
		fragments []StreamToolCall
		done      *StreamChunk
	)
	for chunk := range c.ChatStreamWithTools([]Message{{Role: "user", Content: "go"}}, tools) {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		content.WriteString(chunk.Content)
		fragments = append(fragments, chunk.ToolCalls...)
		if chunk.Done {
			cp := chunk
			done = &cp
		}
	}

	if len(sentTools) != 1 || sentTools[0] != "scan_target" {
		t.Fatalf("streaming request tools = %v, want [scan_target]", sentTools)
	}
	if content.String() != "scanning now" {
		t.Fatalf("streamed content = %q, want %q", content.String(), "scanning now")
	}
	if len(fragments) != 2 {
		t.Fatalf("tool-call fragments = %d (%+v), want 2", len(fragments), fragments)
	}
	if fragments[0].Index != 0 || fragments[0].ID != "call_1" || fragments[0].Name != "scan_target" ||
		fragments[0].Arguments != `{"tar` {
		t.Fatalf("first fragment = %+v", fragments[0])
	}
	if fragments[1].Index != 0 || fragments[1].ID != "" || fragments[1].Name != "" ||
		fragments[1].Arguments != `get":"example.com"}` {
		t.Fatalf("second fragment = %+v", fragments[1])
	}
	if joined := fragments[0].Arguments + fragments[1].Arguments; joined != `{"target":"example.com"}` {
		t.Fatalf("concatenated arguments = %q", joined)
	}
	if done == nil || done.Usage == nil || done.Usage.PromptTokens != 10 ||
		done.Usage.CompletionTokens != 6 || done.Usage.TotalTokens != 16 {
		t.Fatalf("terminal chunk usage = %+v", done)
	}
	in, out, total := c.GetTokens()
	if in != 10 || out != 6 || total != 16 {
		t.Fatalf("streamed token totals = %d/%d/%d, want 10/6/16", in, out, total)
	}
}
