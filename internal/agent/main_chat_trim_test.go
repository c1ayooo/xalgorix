package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func newTrimTestAgent(t *testing.T) (*Agent, *graph.MemoryStore) {
	t.Helper()
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx := scanctx.New("scan-trim", "")
	sctx.SetInstanceID("run-trim")
	state := NewScanState()
	state.ScanContextID = sctx.ID
	state.PlannerWorkerMode = true
	return &Agent{state: state, scanCtx: sctx, events: make(chan Event, 16)}, store
}

func trimTestHistory(pairs int) []llm.Message {
	h := []llm.Message{{Role: "system", Content: "system"}}
	for i := 0; i < pairs; i++ {
		h = append(h, llm.Message{Role: "user", Content: fmt.Sprintf("[USER] keep focus on endpoint /api/%d", i)})
		h = append(h, llm.Message{Role: "assistant", Content: "ack"})
	}
	return h
}

// IG6a / CORE §2: trimming the operator chat must not discard human input.
// Every dropped operator message becomes an idempotent hint node.
func TestMainChatTrimPersistsOperatorHints(t *testing.T) {
	ag, store := newTrimTestAgent(t)
	history := trimTestHistory(30) // 61 messages → 19 operator messages dropped

	out := ag.trimMainChatHistory(history, 24)
	if len(out) != 24 {
		t.Fatalf("trimmed history length = %d, want 24", len(out))
	}

	hints, err := store.ListNodes(context.Background(), "run-trim", graph.KindHint)
	if err != nil {
		t.Fatalf("list hints: %v", err)
	}
	if len(hints) != 19 {
		t.Fatalf("persisted hints = %d, want 19 (one per dropped operator message)", len(hints))
	}
	for _, h := range hints {
		if h.Origin != "human" {
			t.Fatalf("hint origin = %q, want human", h.Origin)
		}
		if !strings.Contains(h.Payload, "main_chat_trim") {
			t.Fatalf("hint payload missing capture provenance: %s", h.Payload)
		}
	}

	// Re-trimming the same history must not duplicate hints: unit keys are
	// content-addressed and UpsertNode is idempotent.
	if again := ag.trimMainChatHistory(history, 24); len(again) != 24 {
		t.Fatalf("repeat trim length = %d", len(again))
	}
	hintsAgain, err := store.ListNodes(context.Background(), "run-trim", graph.KindHint)
	if err != nil {
		t.Fatalf("list hints after retry: %v", err)
	}
	if len(hintsAgain) != len(hints) {
		t.Fatalf("hint count changed on retry: %d -> %d", len(hints), len(hintsAgain))
	}
}

// A confirmation is a decision already applied by the tool, not guidance, so
// it must not be replayed as a hint.
func TestMainChatTrimSkipsConfirmations(t *testing.T) {
	ag, store := newTrimTestAgent(t)
	h := []llm.Message{{Role: "system", Content: "system"}}
	for i := 0; i < 30; i++ {
		h = append(h, llm.Message{Role: "user", Content: "[USER] 确认执行"})
		h = append(h, llm.Message{Role: "assistant", Content: "ok"})
	}
	ag.trimMainChatHistory(h, 24)
	hints, err := store.ListNodes(context.Background(), "run-trim", graph.KindHint)
	if err != nil {
		t.Fatalf("list hints: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("confirmations were persisted as hints: %d", len(hints))
	}
}

// If the graph authority is unavailable, a bounded history is NOT trimmed:
// dropping human input silently is worse than deferring the trim one turn.
func TestMainChatTrimDefersWhenHintWriteFails(t *testing.T) {
	graph.SetBackend(nil)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx := scanctx.New("scan-trim-nil", "")
	sctx.SetInstanceID("run-trim-nil")
	state := NewScanState()
	state.ScanContextID = sctx.ID
	ag := &Agent{state: state, scanCtx: sctx, events: make(chan Event, 16)}

	history := trimTestHistory(12) // 25 messages: over 24, under max+8
	out := ag.trimMainChatHistory(history, 24)
	if len(out) != len(history) {
		t.Fatalf("history was trimmed without persisting operator hints: %d -> %d", len(history), len(out))
	}
}

func TestMainChatOperatorTextMarkers(t *testing.T) {
	cases := []struct {
		content string
		want    string
		ok      bool
	}{
		{"[GRAPH] open=1\n[USER] focus on auth bypass", "focus on auth bypass", true},
		{"[USER] [SYSTEM] frontier drained", "", false},
		{"[CONTEXT] earlier operator messages were recorded", "", false},
		{"Previous conversation transcript (context only): ...", "", false},
		{"[USER] ", "", false},
	}
	for _, tc := range cases {
		got, ok := mainChatOperatorText(tc.content)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("mainChatOperatorText(%q) = (%q,%v), want (%q,%v)", tc.content, got, ok, tc.want, tc.ok)
		}
	}
}
