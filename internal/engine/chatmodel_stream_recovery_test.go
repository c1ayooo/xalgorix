package engine

// chatmodel_stream_recovery_test.go — the streaming path must obey the same
// recovery policy as Generate. A retry is only safe before anything reached
// the reader; after a delta, replaying the provider request would duplicate
// text, so the error/response is surfaced instead.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
)

// scriptedStreamClient serves one scripted chunk slice per provider call.
type scriptedStreamClient struct {
	mu      sync.Mutex
	scripts [][]llm.StreamChunk
	calls   int
}

func (c *scriptedStreamClient) ChatWithUsageToolCalls([]llm.Message, []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	return "", nil, nil, errors.New("stream recovery tests must not use the non-streaming path")
}

func (c *scriptedStreamClient) ChatStreamWithTools([]llm.Message, []llm.ToolDefinition) <-chan llm.StreamChunk {
	c.mu.Lock()
	idx := c.calls
	c.calls++
	c.mu.Unlock()

	ch := make(chan llm.StreamChunk, 4)
	go func() {
		defer close(ch)
		if idx >= len(c.scripts) {
			return
		}
		for _, chunk := range c.scripts[idx] {
			ch <- chunk
		}
	}()
	return ch
}

func (c *scriptedStreamClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// streamRecoveryFake records the policy callbacks and answers with a fixed
// decision set.
type streamRecoveryFake struct {
	mu             sync.Mutex
	attempts       []int
	responses      []string
	errorsSeen     int
	retryEmpty     bool
	retryErrors    bool
	fatalOnSuccess error
}

func (f *streamRecoveryFake) BeforeModel(_ context.Context, attempt int) ModelRequestAction {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, attempt)
	return ModelRequestAction{}
}

func (f *streamRecoveryFake) OnModelError(_ context.Context, _ error) ModelErrorAction {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errorsSeen++
	if f.retryErrors {
		return ModelErrorAction{Retry: true, Nudge: "retry the request"}
	}
	return ModelErrorAction{}
}

func (f *streamRecoveryFake) OnModelResponse(_ context.Context, _ []llm.Message, content string, _ []llm.ToolCall, _ *llm.TokenUsage) ModelResponseAction {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, content)
	if f.fatalOnSuccess != nil {
		return ModelResponseAction{Fatal: f.fatalOnSuccess}
	}
	if f.retryEmpty && strings.TrimSpace(content) == "" {
		return ModelResponseAction{Retry: true, Nudge: "the response was empty"}
	}
	return ModelResponseAction{}
}

func (f *streamRecoveryFake) snapshot() (attempts []int, responses []string, errorsSeen int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.attempts...), append([]string(nil), f.responses...), f.errorsSeen
}

// TestChatModelStream_RecoveryRetriesEmptyResponse: an empty first turn is an
// ordinary policy retry because nothing was surfaced to the reader.
func TestChatModelStream_RecoveryRetriesEmptyResponse(t *testing.T) {
	client := &scriptedStreamClient{scripts: [][]llm.StreamChunk{
		{{Done: true, Usage: &llm.TokenUsage{CompletionTokens: 1, TotalTokens: 1}}},
		{{Content: "hello"}, {Done: true}},
	}}
	rec := &streamRecoveryFake{retryEmpty: true}
	model := NewChatModel(client).WithModelRecovery(rec)

	reader, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	frames := recvAll(t, reader)
	var text strings.Builder
	for _, frame := range frames {
		text.WriteString(frame.Content)
	}
	if text.String() != "hello" {
		t.Fatalf("text = %q, want hello", text.String())
	}
	if got := client.callCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	attempts, responses, _ := rec.snapshot()
	if len(attempts) != 2 || attempts[0] != 0 || attempts[1] != 1 {
		t.Fatalf("BeforeModel attempts = %v, want [0 1]", attempts)
	}
	if len(responses) != 2 || responses[0] != "" || responses[1] != "hello" {
		t.Fatalf("OnModelResponse contents = %v", responses)
	}
}

// TestChatModelStream_RecoveryRetriesProviderErrorBeforeOutput: a failure
// before the first delta is retried with the recovery decision applied.
func TestChatModelStream_RecoveryRetriesProviderErrorBeforeOutput(t *testing.T) {
	client := &scriptedStreamClient{scripts: [][]llm.StreamChunk{
		{{Err: errors.New("boom")}},
		{{Content: "ok"}, {Done: true}},
	}}
	rec := &streamRecoveryFake{retryErrors: true}
	model := NewChatModel(client).WithModelRecovery(rec)

	reader, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	frames := recvAll(t, reader)
	var text strings.Builder
	for _, frame := range frames {
		text.WriteString(frame.Content)
	}
	if text.String() != "ok" {
		t.Fatalf("text = %q, want ok", text.String())
	}
	if got := client.callCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	if _, _, errs := rec.snapshot(); errs != 1 {
		t.Fatalf("OnModelError calls = %d, want 1", errs)
	}
}

// TestChatModelStream_RecoveryRefusesRetryAfterOutput pins the streaming
// safety rule: once a delta reached the reader, the provider error is surfaced
// instead of replaying the request and duplicating text.
func TestChatModelStream_RecoveryRefusesRetryAfterOutput(t *testing.T) {
	client := &scriptedStreamClient{scripts: [][]llm.StreamChunk{
		{{Content: "partial"}, {Err: errors.New("boom")}},
		{{Content: "recovered"}, {Done: true}},
	}}
	rec := &streamRecoveryFake{retryErrors: true}
	model := NewChatModel(client).WithModelRecovery(rec)

	reader, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer reader.Close()
	first, err := reader.Recv()
	if err != nil || first.Content != "partial" {
		t.Fatalf("first frame = %+v err=%v", first, err)
	}
	_, err = reader.Recv()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("second recv err = %v, want the provider error", err)
	}
	if got := client.callCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (no replay after output)", got)
	}
	if _, _, errs := rec.snapshot(); errs != 1 {
		t.Fatalf("OnModelError calls = %d, want 1", errs)
	}
}

// TestChatModelStream_RecoveryFatalSurfaces: a policy fatal on a successful
// response becomes the reader's error, never a silent success.
func TestChatModelStream_RecoveryFatalSurfaces(t *testing.T) {
	client := &scriptedStreamClient{scripts: [][]llm.StreamChunk{
		{{Content: "x"}, {Done: true}},
	}}
	fatal := errors.New("provider paused")
	rec := &streamRecoveryFake{fatalOnSuccess: fatal}
	model := NewChatModel(client).WithModelRecovery(rec)

	reader, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer reader.Close()
	if _, err := reader.Recv(); err != nil {
		t.Fatalf("first frame err = %v", err)
	}
	if _, err := reader.Recv(); !errors.Is(err, fatal) {
		t.Fatalf("second recv err = %v, want %v", err, fatal)
	}
}
