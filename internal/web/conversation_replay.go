package web

// conversation_replay.go — hot-resume ladder L2 (REDESIGN 2026-11-22): rebuild
// a bounded projection of the previous session's conversation from the scan
// dir's events.jsonl so a 1:1 task:run re-dispatch starts warm instead of
// cold. L1 (eino ADK checkpoint Resume) is in-process-only in the current
// eino version — Runtime.Resume needs the interrupt targetID that exists only
// inside the live interrupt event, so a cross-process resume cannot use it;
// that is documented in REDESIGN, not silently dropped.
//
// This is deliberately a TRANSCRIPT, not a structural message replay: journal
// events carry no tool-call ids, and fabricating OpenAI-style
// tool_call/tool-response pairs would couple the resumed session to a
// synthetic protocol state. A formatted transcript inside one user message is
// provider-agnostic and honest about what it is: a projection, not state
// authority (charter §6 — events cannot replay the same decision; scan.json +
// notes + ledger remain authoritative).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

// replayBudgetChars bounds the transcript (~24K tokens at ~4 chars/token).
// The tail is kept: recent context matters most for continuation.
const replayBudgetChars = 96_000

// replayMaxEvents caps how many journal lines are scanned from the tail.
const replayMaxEvents = 400

// conversationReplayTranscript renders the previous session's last events as
// a chronological transcript. Empty when the journal is absent or unusable
// (the caller then falls back to the L3 resume briefing alone).
func conversationReplayTranscript(sctx *scanctx.ScanContext) string {
	if sctx == nil || strings.TrimSpace(sctx.ScanDir) == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(sctx.ScanDir, scanEventJournalFile))
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > replayMaxEvents {
		lines = lines[len(lines)-replayMaxEvents:]
	}
	total := 0
	var kept []string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var ev WSEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		text := renderReplayEvent(&ev)
		if text == "" {
			continue
		}
		if total+len(text) > replayBudgetChars {
			break
		}
		total += len(text)
		// Scanning newest-first and prepending keeps chronological order.
		kept = append([]string{text}, kept...)
	}
	if len(kept) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("⏪ PREVIOUS SESSION TRANSCRIPT — hot-resume projection of this scan's earlier conversation (oldest → newest). Continue seamlessly; do not repeat work the transcript already completed:\n")
	for _, l := range kept {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderReplayEvent formats one journal event as a transcript line; "" for
// event types that carry no conversational value (queue/admission noise).
func renderReplayEvent(ev *WSEvent) string {
	ts := ""
	if len(ev.Timestamp) >= 19 {
		ts = ev.Timestamp[11:19]
	}
	switch ev.Type {
	case "message":
		content := ev.Content
		// Human turns carry the injection marker from agent.go's SendMessage
		// path — strip it; the OPERATOR speaker label replaces it.
		if idx := strings.Index(content, "[USER MESSAGE DURING SCAN]: "); idx >= 0 {
			content = strings.TrimSpace(content[idx+len("[USER MESSAGE DURING SCAN]: "):])
		}
		content = replayTrim(content, 400)
		if content == "" {
			return ""
		}
		if strings.Contains(ev.Content, "[USER MESSAGE DURING SCAN]") {
			return fmt.Sprintf("[%s] OPERATOR: %s", ts, content)
		}
		return fmt.Sprintf("[%s] YOU (assistant): %s", ts, content)
	case "tool_call":
		args, _ := json.Marshal(ev.ToolArgs)
		return fmt.Sprintf("[%s] YOU called %s(%s)", ts, ev.ToolName, replayTrim(string(args), replayLineMax))
	case "tool_result":
		body := ev.Output
		if ev.Error != "" {
			body = ev.Error + " | " + body
		}
		return fmt.Sprintf("[%s] RESULT %s: %s", ts, ev.ToolName, replayTrim(body, replayLineMax))
	default:
		return ""
	}
}

// replayLineMax and replayTrim bound one transcript line, rune-safe.
const replayLineMax = 600

func replayTrim(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
