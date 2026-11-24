package web

// chat_transcript_agent_test.go — the per-agent transcript slice must keep only
// one lane's rows and renumber seq so before_seq pagination over the filtered
// view stays coherent.

import "testing"

func TestAgentTranscriptItemsFiltersAndRenumbers(t *testing.T) {
	items := []transcriptItem{
		{Seq: 1, Kind: "assistant", AgentID: "sub_1", Text: "one"},
		{Seq: 2, Kind: "assistant", AgentID: "sub_2", Text: "two"},
		{Seq: 3, Kind: "tool_result", AgentID: "inst-1/sub_1"},
		{Seq: 4, Kind: "assistant", AgentID: "", Text: "main"},
	}
	got := agentTranscriptItems(items, "sub_1")
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2: %#v", len(got), got)
	}
	if got[0].Seq != 1 || got[0].AgentID != "sub_1" {
		t.Fatalf("first row = %#v", got[0])
	}
	if got[1].Seq != 2 || got[1].AgentID != "inst-1/sub_1" {
		t.Fatalf("prefixed sibling id must match and renumber: %#v", got[1])
	}
}

func TestAgentTranscriptItemsEmptyAgentIsNoop(t *testing.T) {
	items := []transcriptItem{{Seq: 7, AgentID: "sub_1"}, {Seq: 9, AgentID: "sub_2"}}
	got := agentTranscriptItems(items, "  ")
	if len(got) != len(items) || got[1].Seq != 9 {
		t.Fatalf("empty agent must not filter or renumber: %#v", got)
	}
}
