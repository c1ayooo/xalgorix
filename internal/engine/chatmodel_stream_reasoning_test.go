package engine

// chatmodel_stream_reasoning_test.go — reasoning text is part of the stream
// contract. It surfaces only for an accepted response, mirroring Generate's
// "accepted responses only" rule, and never for a retried attempt.

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
)

func TestChatModelStream_SurfacesReasoningAfterAcceptance(t *testing.T) {
	client := &scriptedStreamClient{scripts: [][]llm.StreamChunk{
		{
			{Reasoning: "think "},
			{Reasoning: "hard"},
			{Content: "answer"},
			{Done: true, Usage: &llm.TokenUsage{TotalTokens: 3}},
		},
	}}
	var got []string
	model := NewChatModel(client)
	model.WithOnReasoning(func(s string) { got = append(got, s) })

	reader, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	frames := recvAll(t, reader)
	var text strings.Builder
	for _, frame := range frames {
		text.WriteString(frame.Content)
	}
	if text.String() != "answer" {
		t.Fatalf("text = %q, want answer", text.String())
	}
	if len(got) != 1 || got[0] != "think hard" {
		t.Fatalf("onReasoning calls = %v, want [\"think hard\"]", got)
	}
}

func TestChatModelStream_ReasoningFromRejectedAttemptNotSurfaced(t *testing.T) {
	client := &scriptedStreamClient{scripts: [][]llm.StreamChunk{
		// Empty turn with reasoning only: the policy retries it.
		{{Reasoning: "discard me"}, {Done: true}},
		{{Reasoning: "keep me"}, {Content: "answer"}, {Done: true}},
	}}
	rec := &streamRecoveryFake{retryEmpty: true}
	var got []string
	model := NewChatModel(client).WithModelRecovery(rec)
	model.WithOnReasoning(func(s string) { got = append(got, s) })

	reader, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	recvAll(t, reader)
	if len(got) != 1 || got[0] != "keep me" {
		t.Fatalf("onReasoning calls = %v, want only the accepted attempt", got)
	}
}
