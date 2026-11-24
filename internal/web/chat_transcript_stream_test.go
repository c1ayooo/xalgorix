package web

// chat_transcript_stream_test.go — a streamed assistant turn must survive the
// read projections. Deltas are the live channel (no transcript case); the
// closing assistant_done carries the full text and folds into exactly one
// assistant row.

import (
	"testing"
)

func TestFoldTranscript_StreamingTurnLandsAsOneAssistantRow(t *testing.T) {
	lines := []string{
		`{"type":"assistant_delta","message_id":"m1","delta":"Hel","content":"Hel","agent_id":"agent_1"}`,
		`{"type":"assistant_delta","message_id":"m1","delta":"lo","content":"lo","agent_id":"agent_1"}`,
		`{"type":"assistant_done","message_id":"m1","content":"Hello","agent_id":"agent_1"}`,
	}
	seen := make(map[string]struct{})
	var items []transcriptItem
	for _, line := range lines {
		foldTranscriptLine([]byte(line), seen, &items)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want exactly one assistant row: %#v", len(items), items)
	}
	if items[0].Kind != "assistant" || items[0].Text != "Hello" || items[0].AgentID != "agent_1" {
		t.Fatalf("projected row = %#v", items[0])
	}
}

// Deltas alone never invent a row: a reader that catches the journal mid-turn
// shows the previous turns, not a half-written one.
func TestFoldTranscript_DeltasAloneProduceNoRow(t *testing.T) {
	lines := []string{
		`{"type":"assistant_delta","message_id":"m2","delta":"Hel","content":"Hel","agent_id":"agent_1"}`,
		`{"type":"assistant_delta","message_id":"m2","delta":"lo","content":"lo","agent_id":"agent_1"}`,
	}
	seen := make(map[string]struct{})
	var items []transcriptItem
	for _, line := range lines {
		foldTranscriptLine([]byte(line), seen, &items)
	}
	if len(items) != 0 {
		t.Fatalf("deltas produced transcript rows: %#v", items)
	}
}
