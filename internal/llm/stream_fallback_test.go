package llm

// stream_fallback_test.go — a gateway that ignores stream=true and answers with
// one plain JSON body must still produce a complete turn. Without this, the
// stream consumer sees an empty response and the agent burns recovery turns.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

func TestChatStreamWithTools_NonSSEBodyYieldsOneCompleteTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "thinking about it",
					"tool_calls": []map[string]any{{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "read_ledger", "arguments": `{"filter":"schedulable"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10},
		})
	}))
	defer srv.Close()

	client := NewClient(&config.Config{LLM: "test", APIBase: srv.URL, APIKey: "x"})
	var (
		content strings.Builder
		calls   []StreamToolCall
		done    *StreamChunk
	)
	for chunk := range client.ChatStreamWithTools([]Message{{Role: "user", Content: "hi"}}, nil) {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		content.WriteString(chunk.Content)
		calls = append(calls, chunk.ToolCalls...)
		if chunk.Done {
			cp := chunk
			done = &cp
		}
	}
	if content.String() != "thinking about it" {
		t.Fatalf("content = %q", content.String())
	}
	if len(calls) != 1 || calls[0].Name != "read_ledger" || calls[0].Arguments != `{"filter":"schedulable"}` {
		t.Fatalf("tool calls = %+v", calls)
	}
	if done == nil || done.Usage == nil {
		t.Fatalf("terminal chunk missing usage: %+v", done)
	}
	if done.Usage.PromptTokens != 7 || done.Usage.CompletionTokens != 3 || done.Usage.TotalTokens != 10 {
		t.Fatalf("usage = %+v", done.Usage)
	}
}
