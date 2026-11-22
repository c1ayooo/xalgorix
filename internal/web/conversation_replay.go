package web

// conversation_replay.go — hot-resume ladder L2 (REDESIGN D11): rebuild a
// bounded projection of the previous session's conversation from the scan
// dir's events.jsonl so a 1:1 task:run re-dispatch starts warm instead of
// cold, plus the stale-checkpoint drop that makes the re-open actually run.
//
// L1 (eino ADK checkpoint Resume) is in-process-only in the current eino
// version: Runtime.Resume needs the interrupt targetID that exists only
// inside the live interrupt event, and checkpoints are never written at
// user stop. Worse, a checkpoint SAVED BY CANCEL makes the next rt.Run
// resume the finished graph and exit instantly (observed on the first 1:1
// re-dispatches, 2026-11-22) — so a re-open DROPS the stale checkpoint via
// dropStaleEngineCheckpoints instead of resuming it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// replayBudgetChars bounds the transcript (~24K tokens at ~4 chars/token).
// The tail is kept: recent context matters most for continuation.
const replayBudgetChars = 96_000

// replayMaxEvents caps how many journal lines are scanned from the tail.
const replayMaxEvents = 400

// conversationReplayTranscript renders the previous session's last events as
// a chronological transcript. R6.2 (REDESIGN D12 ⑤): the run's durable
// event history is the source — the instance union when the replaying
// context knows its run identity, that scan's own rows otherwise — and the
// legacy events.jsonl file of the dir remains the pre-import fallback.
// Empty when neither source has usable rows (the caller then falls back to
// the L3 resume briefing alone).
func conversationReplayTranscript(sctx *scanctx.ScanContext) string {
	if sctx == nil {
		return ""
	}
	// The ScanContext carries no run identity; this variant replays the
	// session's own durable rows (the scan_session caller passes the run id).
	return conversationReplayTranscriptFromDir(sctx.ScanDir, "")
}

// conversationReplayTranscriptFromDir is the dir-parameterized variant: a
// 1:1 re-dispatch replays the PREVIOUS scan dir's history even though the
// new session writes a fresh one. instanceID (when non-empty) widens the
// durable read to the run's whole sibling history — the D12 full-history
// replay — while dir anchors the legacy file fallback.
func conversationReplayTranscriptFromDir(dir string, instanceID string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	total := 0
	var kept []string
	fold := func(ev WSEvent) bool {
		text := renderReplayEvent(&ev)
		if text == "" {
			return true
		}
		if total+len(text) > replayBudgetChars {
			return false
		}
		total += len(text)
		// Walking newest-first and prepending keeps chronological order.
		kept = append([]string{text}, kept...)
		return true
	}
	if events := replayEventsFromState(dir, instanceID); len(events) > 0 {
		for i := len(events) - 1; i >= 0; i-- {
			if !fold(events[i]) {
				break
			}
		}
	} else {
		data, err := os.ReadFile(filepath.Join(dir, scanEventJournalFile))
		if err != nil || len(data) == 0 {
			return ""
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) > replayMaxEvents {
			lines = lines[len(lines)-replayMaxEvents:]
		}
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			var ev WSEvent
			if json.Unmarshal([]byte(line), &ev) != nil {
				continue
			}
			if !fold(ev) {
				break
			}
		}
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

// replayEventsFromState resolves the replay's durable source rows, oldest
// first: the run's whole sibling history when the run identity is known
// (REDESIGN D12 ⑤ — the re-dispatch replays the run's full history, budget
// unchanged), that scan's own rows otherwise. nil = no durable rows (the
// caller keeps the legacy file walk; pre-import directories only).
func replayEventsFromState(dir, instanceID string) []WSEvent {
	b := currentEventStateBackend()
	if b == nil {
		return nil
	}
	ctx, cancel := eventStateCtx()
	defer cancel()
	tail := func(recs []runstate.EventRecord, totalCount int64, err error) []WSEvent {
		if err != nil || totalCount == 0 {
			return nil
		}
		return decodeEventRecords(recs)
	}
	scanID := scanStateIDForDir(dir)
	if strings.TrimSpace(instanceID) != "" {
		_, total, err := b.LoadEventsByInstance(ctx, instanceID, 0, 1)
		if err != nil || total > 0 {
			off := 0
			if total > int64(replayMaxEvents) {
				off = int(total) - replayMaxEvents
			}
			recs, _, rerr := b.LoadEventsByInstance(ctx, instanceID, off, replayMaxEvents)
			if evs := tail(recs, total, rerr); evs != nil {
				return evs
			}
			if err == nil {
				return nil // durable union exists but is empty — no rows
			}
		}
	}
	_, total, err := b.LoadEventsByScan(ctx, scanID, 0, 1)
	if err != nil || total == 0 {
		return nil
	}
	off := 0
	if total > int64(replayMaxEvents) {
		off = int(total) - replayMaxEvents
	}
	recs, _, rerr := b.LoadEventsByScan(ctx, scanID, off, replayMaxEvents)
	return tail(recs, total, rerr)
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

// dropStaleEngineCheckpoints removes the engine-level checkpoint for this
// instance and any per-scan fallback copies, so a 1:1 re-open starts a fresh
// ADK run instead of resuming a finished graph. Missing files are fine.
func dropStaleEngineCheckpoints(dataDir string, sctx *scanctx.ScanContext) {
	if sctx == nil || sctx.ID == "" {
		return
	}
	sum := sha256.Sum256([]byte(sctx.ID))
	paths := []string{
		filepath.Join(dataDir, "_engine", "checkpoints", hex.EncodeToString(sum[:])+".ckpt"),
	}
	if sctx.ScanDir != "" {
		matches, _ := filepath.Glob(filepath.Join(sctx.ScanDir, "*.ckpt"))
		paths = append(paths, matches...)
	}
	for _, p := range paths {
		if err := os.Remove(p); err == nil {
			log.Printf("[platform] dropped stale engine checkpoint %s for re-open", p)
		}
	}
}
