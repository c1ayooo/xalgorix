package web

// scan_security.go — GET /api/scans/{id}/security: the operator-facing view
// of the security chain for one scan (REDESIGN D11 follow-up, 2026-11-22).
//
// Two halves:
//   - guards: the STATIC catalog of enforcement layers (charter §2 安全链六环
//     + the agent-level gates). Static by design — the frontend localizes
//     titles/details from the rule id, so the catalog never drifts per scan.
//   - blocks: every guard refusal observed in this scan's events journal,
//     categorized by the guard that fired, newest first, capped.
//
// Blocks are extracted from events.jsonl (the audit projection of record).
// The marker set mirrors the guard block prefixes emitted by agent_guard /
// scopeguard / target_scope / goal_gate / hooks. Journal-only by intention:
// a refusal that never reached the journal also never reached the model,
// so it is not a guard event the operator needs to see.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// securityBlock is one guard refusal surfaced to the operator.
type securityBlock struct {
	TS       string `json:"ts"`
	Category string `json:"category"`
	Tool     string `json:"tool,omitempty"`
	Reason   string `json:"reason"`
}

// scanSecurityView is the GET /api/scans/{id}/security response.
type scanSecurityView struct {
	ScanID    string          `json:"scan_id"`
	Blocks    []securityBlock `json:"blocks"`
	Counters  map[string]int  `json:"counters"`
	Truncated bool            `json:"truncated"`
}

// securityBlockCategories maps journal marker prefixes to a stable category
// key the frontend localizes. Order matters: first match wins.
var securityBlockCategories = []struct {
	prefix   string
	category string
}{
	{"OUT-OF-SCOPE TARGET BLOCKED", "scopeguard"},
	{"OUTSIDE SCAN BOUNDARY", "target_scope"},
	{"PHASE RESTRICTION BLOCKED", "phase"},
	{"AUTH PREREQUISITE GUARD BLOCKED", "auth"},
	{"ACTIVITY POLICY BLOCKED", "activity"},
	{"GOAL OBLIGATION", "goal"},
	{"RUN RE-OPENED BY THE OPERATOR", "goal"},
	{"UNRECORDED EVIDENCE", "ledger"},
	{"proven-but-unreported", "ledger"},
	{"REPORT NOT SAVED", "report"},
	{"DELEGATION DUPLICATE", "delegation"},
	{"benchmark isolation", "benchmark"},
}

// securityCategories is the fixed counter key set (zero-filled) so the UI
// renders every bucket without inferring absent keys.
var securityCategories = []string{
	"scopeguard", "target_scope", "phase", "auth", "activity",
	"goal", "ledger", "report", "benchmark", "delegation", "other",
}

const securityBlockCap = 200             // newest blocks returned
const securityJournalMaxBytes = 32 << 20 // read cap for oversized journals

// handleScanSecurity serves GET /api/scans/{id}/security.
//
// Instance-form requests (the task-detail run view) merge every sibling
// session's journal, newest session first — a D11 re-dispatch continues the
// same guarded investigation, so its previous sessions' refusals stay visible
// instead of vanishing into the fresh session's empty journal.
func (s *Server) handleScanSecurity(w http.ResponseWriter, r *http.Request) {
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/security")
	if scanID == "" {
		http.Error(w, `{"error":"scan id is required"}`, http.StatusBadRequest)
		return
	}
	entries := s.findAllScanSummaries()
	res, ok := s.resolveScanRecordByIDFrom(scanID, func() []scanEntry { return entries })
	if !ok {
		http.Error(w, `{"error":"scan not found"}`, http.StatusNotFound)
		return
	}
	dirs := s.sessionDirsForProjection(res, entries)

	view := scanSecurityView{
		ScanID:   filepath.Base(scanID),
		Blocks:   []securityBlock{},
		Counters: map[string]int{},
	}
	for _, c := range securityCategories {
		view.Counters[c] = 0
	}

	// Newest first; the cap keeps the newest refusals across the whole run
	// when it racks them up. With a state backend registered (R6.1) the tail
	// comes from the durable journal — the instance union for run-scoped
	// requests, the session's own rows otherwise; the legacy files remain the
	// fallback for directories whose rows the import never landed.
	var blocks []securityBlock
	truncated := false
	if events, ok := securityJournalEvents(res, dirs); ok {
		for i := len(events) - 1; i >= 0 && !truncated; i-- {
			ev := events[i]
			category, reason := classifySecurityBlock(&ev)
			if category == "" {
				continue
			}
			if len(blocks) >= securityBlockCap {
				truncated = true
				break
			}
			view.Counters[category]++
			blocks = append(blocks, securityBlock{
				TS:       ev.Timestamp,
				Category: category,
				Tool:     ev.ToolName,
				Reason:   firstLine(reason),
			})
		}
	} else {
		for i := len(dirs) - 1; i >= 0 && !truncated; i-- {
			data, err := readJournalTail(filepath.Join(dirs[i], scanEventJournalFile), securityJournalMaxBytes)
			if err != nil || len(data) == 0 {
				continue
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			for j := len(lines) - 1; j >= 0; j-- {
				line := strings.TrimSpace(lines[j])
				if line == "" {
					continue
				}
				var ev WSEvent
				if json.Unmarshal([]byte(line), &ev) != nil {
					continue
				}
				category, reason := classifySecurityBlock(&ev)
				if category == "" {
					continue
				}
				if len(blocks) >= securityBlockCap {
					truncated = true
					break
				}
				view.Counters[category]++
				blocks = append(blocks, securityBlock{
					TS:       ev.Timestamp,
					Category: category,
					Tool:     ev.ToolName,
					Reason:   firstLine(reason),
				})
			}
		}
	}
	// Chronological order for display (oldest → newest within the cap).
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].TS < blocks[j].TS })
	view.Blocks = blocks
	view.Truncated = truncated
	writeJSONStatus(w, http.StatusOK, view)
}

// classifySecurityBlock inspects one journal event for a guard refusal,
// returning (category, reason-first-line). Category "" = not a guard event.
func classifySecurityBlock(ev *WSEvent) (string, string) {
	text := ""
	switch ev.Type {
	case "tool_result":
		// Journal events carry the tool result flat (Output/Error), already
		// projected from the agent Event's ToolResult by processEvent.
		text = ev.Output
	case "message":
		text = ev.Content
	default:
		return "", ""
	}
	if ev.Error != "" {
		text = ev.Error + "\n" + text
	}
	if !strings.Contains(text, "⛔") && !strings.Contains(text, "⚠️") {
		return "", ""
	}
	for _, m := range securityBlockCategories {
		if idx := strings.Index(text, m.prefix); idx >= 0 {
			reason := text[idx:]
			if nl := strings.IndexAny(reason, "\n"); nl >= 0 {
				reason = reason[:nl]
			}
			return m.category, strings.TrimSpace(reason)
		}
	}
	return "other", firstLine(text)
}

// securityJournalTailRows bounds the durable-mode tail: the newest rows the
// security view considers across the whole run. The file mode caps by bytes
// (32MB per session); durable payloads are the same trimmed event shapes, so
// a row cap of this size covers at least what the byte cap yielded while
// never materializing the full journal.
const securityJournalTailRows = 20000

// securityJournalEvents reads the newest-first event tail behind this
// request from the durable journal: the run's instance union when the
// request addresses the run (D11 — PG aggregates every sibling session's
// rows by instance id, so no sibling directory enumeration feeds this read),
// the session's own rows otherwise. ok=false sends the caller to the legacy
// file loop, which with a backend registered covers only the resolved
// session's own file (pre-import stragglers).
func securityJournalEvents(res scanResolution, dirs []string) ([]WSEvent, bool) {
	b := currentEventStateBackend()
	if b == nil || len(dirs) == 0 {
		return nil, false
	}
	ctx, cancel := eventStateCtx()
	defer cancel()
	if res.instanceForm && res.rec != nil && res.rec.InstanceID != "" {
		_, total, err := b.LoadEventsByInstance(ctx, res.rec.InstanceID, 0, 1)
		if err != nil || total == 0 {
			return nil, false
		}
		recs, _, err := b.LoadEventsByInstance(ctx, res.rec.InstanceID, int(max(total-int64(securityJournalTailRows), 0)), securityJournalTailRows)
		if err != nil {
			return nil, false
		}
		return decodeEventRecords(recs), true
	}
	scanID := filepath.Base(filepath.Clean(dirs[0]))
	_, total, err := b.LoadEventsByScan(ctx, scanID, 0, 1)
	if err != nil || total == 0 {
		return nil, false
	}
	recs, _, err := b.LoadEventsByScan(ctx, scanID, int(max(total-int64(securityJournalTailRows), 0)), securityJournalTailRows)
	if err != nil {
		return nil, false
	}
	return decodeEventRecords(recs), true
}

// readJournalTail reads at most maxBytes from the end of the journal so an
// oversized scan never balloons the response build.
func readJournalTail(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() <= maxBytes {
		return os.ReadFile(path)
	}
	off := info.Size() - maxBytes
	buf := make([]byte, maxBytes)
	n, err := f.ReadAt(buf, off)
	if err != nil && n == 0 {
		return nil, err
	}
	// Drop the first (likely partial) line.
	if idx := bytes.IndexByte(buf[:n], '\n'); idx >= 0 {
		buf = buf[idx+1:]
		n -= idx + 1
	}
	return buf[:n], nil
}

// firstLine returns the first line of s, trimmed.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
