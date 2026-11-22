package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// The chat page treats a run as a conversation (docs/CHAT_PAGE_REDESIGN.md).
// This file holds the two pieces that page needs from the server: a readable
// title for a freshly mounted run, and a paginated transcript assembled from
// the scan journal plus the human guidance rows.
//
// The transcript is a read-only projection. It never becomes state authority:
// scanctx/scan.json/ledger.json keep that role (CORE_PHILOSOPHY §6), and every
// field here is derived per request from events.jsonl + platform.Guidance.

// conversationTitle is the default title for a run mounted at time t. The task
// name is the only stable human label available before the first message, so
// dispatch uses it with a timestamp suffix; a later human message or a rename
// replaces it.
func conversationTitle(taskName string, t time.Time) string {
	name := strings.TrimSpace(taskName)
	if name == "" {
		name = "未命名任务"
	}
	// Timestamps are stored in UTC; the operator reads them in server-local
	// time, so the suffix follows t.Local().
	return fmt.Sprintf("%s · %s", name, t.Local().Format("01-02 15:04"))
}

// transcriptItem is one rendered line of the conversation. Kind drives the
// frontend component: human/assistant bubbles, collapsible tool cards,
// approval cards and finding cards all share this envelope.
type transcriptItem struct {
	Seq     int             `json:"seq"`
	Kind    string          `json:"kind"` // human|assistant|tool_use|tool_result|approval|finding|system
	Role    string          `json:"role,omitempty"`
	Text    string          `json:"text,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Output  string          `json:"output,omitempty"`
	Status  string          `json:"status,omitempty"`
	AgentID string          `json:"agent_id,omitempty"`
	At      time.Time       `json:"at"`
	// Phase carries the inferred methodology phase of tool events (Track C
	// round segmentation): the transcript view inserts a divider when it
	// changes. Zero = event carried no phase.
	Phase int `json:"phase,omitempty"`
	// Truncated marks an output cut for transport; the full text stays in the
	// scan directory and the event archive.
	Truncated bool `json:"truncated,omitempty"`
	// Approval carries the structured decision so the UI can render the row in
	// the operator's language instead of the server's.
	Approval *approvalView `json:"approval,omitempty"`
}

// approvalView is the transcript projection of an approval event.
type approvalView struct {
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	DecidedBy string `json:"decided_by,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Decided   bool   `json:"decided"`
}

const (
	transcriptMaxOutputChars = 8000
	transcriptMaxEvents      = 50000
	transcriptDefaultLimit   = 80
)

// conversationWire is one sidebar row: the run plus the task name it belongs
// to and the projected status the list endpoint already computes.
type conversationWire struct {
	platform.Run
	TaskName string `json:"task_name,omitempty"`
}

// handleProjectConversations serves GET /api/projects/{id}/conversations — the
// chat sidebar in one round trip: every run of every task in the project, with
// its conversation title, pin flag and projected status.
func (s *Server) handleProjectConversations(w http.ResponseWriter, r *http.Request, store platform.Repository, projectID string) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	runs, err := store.ListRunsByProject(r.Context(), projectID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	tasks, err := store.ListTasks(r.Context(), projectID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	names := make(map[string]string, len(tasks))
	for _, t := range tasks {
		names[t.ID] = t.Name
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	idx.projectRuns(runs, s.pendingDispatchExists)
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	out := make([]conversationWire, 0, len(runs))
	for i := range runs {
		if runs[i].Archived && !includeArchived {
			continue
		}
		if strings.TrimSpace(runs[i].Title) == "" {
			runs[i].Title = conversationTitle(names[runs[i].TaskID], runs[i].CreatedAt)
		}
		out = append(out, conversationWire{Run: runs[i], TaskName: names[runs[i].TaskID]})
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"items": out})
}

// backfillRunTitles fills empty titles for runs mounted before migration 0011.
// Best effort: a failed write leaves the row empty and the sidebar falls back
// to the instance id, which is what it always showed.
func (s *Server) backfillRunTitles(ctx context.Context, store platform.Repository, taskID string, runs []platform.Run) {
	if len(runs) == 0 {
		return
	}
	task, err := store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return
	}
	for i := range runs {
		if strings.TrimSpace(runs[i].Title) != "" {
			continue
		}
		title := conversationTitle(task.Name, runs[i].CreatedAt)
		if err := store.RenameRun(ctx, runs[i].InstanceID, title); err != nil {
			continue
		}
		runs[i].Title = title
	}
}

// maybeTitleFromGuidance renames a run whose title is still the dispatch-time
// default ("task · MM-DD HH:MM") to "task · <first human message>". An operator
// rename is never overwritten: the title must still equal the generated one.
func (s *Server) maybeTitleFromGuidance(ctx context.Context, store platform.Repository, instanceID, message string) {
	instanceID = strings.TrimSpace(instanceID)
	snippet := firstRunes(strings.TrimSpace(message), 24)
	if instanceID == "" || snippet == "" {
		return
	}
	run, err := s.runForInstance(ctx, store, instanceID)
	if err != nil || run == nil {
		return
	}
	task, err := store.GetTask(ctx, run.TaskID)
	if err != nil || task == nil {
		return
	}
	if run.Title != conversationTitle(task.Name, run.CreatedAt) {
		return
	}
	name := strings.TrimSpace(task.Name)
	if name == "" {
		name = "未命名任务"
	}
	if err := store.RenameRun(ctx, instanceID, name+" · "+snippet); err != nil {
		log.Printf("[platform] auto-title run %s failed: %v", instanceID, err)
	}
}

// firstRunes truncates by rune so a CJK message is never cut mid-character.
func firstRunes(text string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range text {
		if count == n {
			return strings.TrimSpace(text[:i])
		}
		count++
	}
	return strings.TrimSpace(text)
}

// handleConversationDetail serves the chat page's conversation surface. The
// mount id is the run's instance_id; the path is deliberately separate from
// /api/instances/ so lifecycle actions (pause/resume/stop) keep their handler.
//
//	GET   /api/conversations/{instanceID}/transcript?before_seq=&limit=
//	PATCH /api/conversations/{instanceID}   {"title","pinned","archived"}
func (s *Server) handleConversationDetail(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/conversations/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	instanceID := parts[0]
	switch {
	case len(parts) == 2 && parts[1] == "transcript" && r.Method == http.MethodGet:
		s.handleTranscript(w, r, store, instanceID)
	case len(parts) == 2 && parts[1] == "attachments" && r.Method == http.MethodPost:
		s.handleConversationAttachment(w, r)
	case len(parts) == 1 && r.Method == http.MethodPatch:
		s.handleInstancePatch(w, r, store, instanceID)
	default:
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (s *Server) handleInstancePatch(w http.ResponseWriter, r *http.Request, store platform.Repository, instanceID string) {
	limitJSONBody(w, r)
	var body struct {
		Title    *string `json:"title"`
		Pinned   *bool   `json:"pinned"`
		Archived *bool   `json:"archived"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Title == nil && body.Pinned == nil && body.Archived == nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "nothing to update"})
		return
	}
	if body.Title != nil && strings.TrimSpace(*body.Title) == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "title cannot be empty"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if body.Title != nil {
		if err := store.RenameRun(ctx, instanceID, strings.TrimSpace(*body.Title)); err != nil {
			writePlatformError(w, err)
			return
		}
	}
	if body.Pinned != nil {
		if err := store.SetRunPinned(ctx, instanceID, *body.Pinned); err != nil {
			writePlatformError(w, err)
			return
		}
	}
	if body.Archived != nil {
		if err := store.SetRunArchived(ctx, instanceID, *body.Archived); err != nil {
			writePlatformError(w, err)
			return
		}
	}
	run, err := s.runForInstance(ctx, store, instanceID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, run)
}

func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request, store platform.Repository, instanceID string) {
	limit := transcriptDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 500 {
			limit = v
		}
	}
	beforeSeq := 0 // 0 = latest page
	if raw := r.URL.Query().Get("before_seq"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			beforeSeq = v
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	run, _ := s.runForInstance(ctx, store, instanceID)

	items := s.buildTranscript(ctx, store, instanceID, run)
	total := len(items)

	end := total
	if beforeSeq > 0 {
		// seq is 1-based and contiguous, so seq < beforeSeq == index < beforeSeq-1.
		end = beforeSeq - 1
		if end > total {
			end = total
		}
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	page := items[start:end]
	earliest := 0
	if len(page) > 0 {
		earliest = page[0].Seq
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"items":        page,
		"total":        total,
		"has_more":     start > 0,
		"earliest_seq": earliest,
		"latest_seq":   total,
		"running":      s.instanceIsRunning(instanceID),
	})
}

func (s *Server) runForInstance(ctx context.Context, store platform.Repository, instanceID string) (*platform.Run, error) {
	runs, err := store.RunsByInstanceID(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, platform.ErrNotFound
	}
	return &runs[0], nil
}

// buildTranscript merges agent events and human guidance into one timeline.
// Unknown event types are skipped rather than guessed, so a future emitter
// cannot silently inject a fake bubble.
func (s *Server) buildTranscript(ctx context.Context, store platform.Repository, instanceID string, run *platform.Run) []transcriptItem {
	// Guidance goes in first so that on a timestamp tie (same second) the
	// human turn stays above the agent's reaction to it. Event rows then keep
	// their journal order, which is the only ordering the emitters guarantee.
	items := make([]transcriptItem, 0, 128)
	items = append(items, s.transcriptGuidance(ctx, store, instanceID, run)...)
	items = append(items, s.transcriptEvents(instanceID, run)...)

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].At.Before(items[j].At)
	})
	for i := range items {
		items[i].Seq = i + 1
	}
	return items
}

// transcriptEvents reads the scan journals behind this run. The conversation
// surface is per-instance: a D11 re-dispatch continues the same conversation
// in a fresh session directory (the old journal is replayed into the prompt
// only), and multi-target dispatches add further sessions — so the transcript
// merges every sibling session's journal, oldest session first. Resolution
// follows the same path as the scan detail page so a wildcard child or an
// imported run lands on the same directories.
func (s *Server) transcriptEvents(instanceID string, run *platform.Run) []transcriptItem {
	entries := s.findAllScanSummaries()
	// Resolve the INSTANCE first so the D11 sibling expansion applies; the
	// legacy scan id is only a fallback for runs whose records lost the
	// instance stamp (imported history). Preferring the legacy id would pin
	// the transcript to the first session and hide the resumed one.
	res, ok := s.resolveScanRecordByIDFrom(instanceID, func() []scanEntry { return entries })
	if !ok && run != nil && strings.TrimSpace(run.LegacyScanID) != "" {
		res, ok = s.resolveScanRecordByIDFrom(run.LegacyScanID, func() []scanEntry { return entries })
	}
	if !ok || res.dir == "" {
		return nil
	}
	dirs := s.sessionDirsForProjection(res, entries)

	out := make([]transcriptItem, 0, 128)
	// The engine emits some terminal events more than once (finished per
	// phase, and a message mirror of every tool result). Rendering them all
	// would show duplicated bubbles, so identical (type,tool,body,timestamp)
	// rows collapse to the first occurrence. The dedup spans sessions so a
	// turn mirrored across a continuity boundary still renders once.
	seen := make(map[string]struct{}, 256)
	for _, dir := range dirs {
		s.appendSessionTranscript(dir, seen, &out)
	}
	return out
}

// appendSessionTranscript folds one session directory's events.jsonl into the
// run transcript timeline.
func (s *Server) appendSessionTranscript(dir string, seen map[string]struct{}, out *[]transcriptItem) {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lines := 0
	for scanner.Scan() {
		lines++
		if lines > transcriptMaxEvents {
			break
		}
		var ev struct {
			Type    string          `json:"type"`
			Content string          `json:"content"`
			Tool    string          `json:"tool_name"`
			Args    json.RawMessage `json:"tool_args"`
			Output  string          `json:"output"`
			Error   string          `json:"error"`
			AgentID string          `json:"agent_id"`
			At      time.Time       `json:"timestamp"`
			Phase   int             `json:"current_phase"`
			Vulns   []struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Severity string `json:"severity"`
				Endpoint string `json:"endpoint"`
				CVSS     any    `json:"cvss"`
			} `json:"vulns"`
			Aborted     bool               `json:"aborted"`
			AbortReason string             `json:"abort_reason"`
			Approval    *platform.Approval `json:"approval"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		at := ev.At
		if at.IsZero() {
			at = time.Now()
		}
		key := fmt.Sprintf("%s|%s|%s|%s|%s", ev.Type, ev.Tool, ev.Content, ev.Output, at.Format(time.RFC3339))
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		switch ev.Type {
		case "message":
			if strings.TrimSpace(ev.Content) == "" || isToolEcho(ev.Content) {
				continue
			}
			*out = append(*out, transcriptItem{Kind: "assistant", Role: "agent", Text: ev.Content, AgentID: ev.AgentID, At: at})
		case "chat_question":
			// Operator side-question (chat_questions.go): renders like a
			// human turn; no guidance lifecycle status behind it.
			if strings.TrimSpace(ev.Content) == "" {
				continue
			}
			*out = append(*out, transcriptItem{Kind: "human", Role: "human", Text: ev.Content, At: at})
		case "chat_reply":
			// The side-question responder's answer (or its honest failure).
			// Role "chat" lets the UI badge it apart from main-loop agent
			// narration; a failed call keeps its row so the operator sees
			// the error instead of silence.
			item := transcriptItem{Kind: "assistant", Role: "chat", AgentID: ev.AgentID, At: at}
			if strings.TrimSpace(ev.Error) != "" {
				// Text stays the raw error; the UI renders the localized
				// "answer failed" framing from Status.
				item.Status = "failed"
				item.Text = ev.Error
			} else {
				item.Text = ev.Content
			}
			if strings.TrimSpace(item.Text) == "" {
				continue
			}
			*out = append(*out, item)
		case "tool_call":
			*out = append(*out, transcriptItem{Kind: "tool_use", Tool: ev.Tool, Args: ev.Args, Text: ev.Content, AgentID: ev.AgentID, Phase: ev.Phase, At: at})
		case "tool_result":
			text, cut := truncateTranscript(ev.Output)
			*out = append(*out, transcriptItem{Kind: "tool_result", Tool: ev.Tool, Output: text, Truncated: cut, AgentID: ev.AgentID, Phase: ev.Phase, At: at})
		case "approval_requested", "approval_decided":
			if ev.Approval == nil {
				continue
			}
			*out = append(*out, transcriptItem{
				Kind:   "approval",
				Text:   approvalTranscriptLine(ev.Type, ev.Approval),
				Status: ev.Approval.Status,
				At:     at,
				Approval: &approvalView{
					Kind:      ev.Approval.Kind,
					Status:    ev.Approval.Status,
					DecidedBy: ev.Approval.DecidedBy,
					Reason:    ev.Approval.Reason,
					Decided:   ev.Type == "approval_decided",
				},
			})
		case "finished":
			status := "finished"
			if ev.Aborted {
				status = "aborted"
				if reason := strings.TrimSpace(ev.AbortReason); reason != "" {
					status += ": " + reason
				}
			}
			*out = append(*out, transcriptItem{Kind: "system", Text: status, At: at})
			for _, v := range ev.Vulns {
				line := strings.TrimSpace(v.Title)
				if v.Severity != "" {
					line = fmt.Sprintf("[%s] %s", strings.ToUpper(v.Severity), line)
				}
				if v.Endpoint != "" {
					line += " @ " + v.Endpoint
				}
				*out = append(*out, transcriptItem{Kind: "finding", Text: line, Status: v.ID, At: at})
			}
		}
	}
}

// transcriptGuidance folds the operator's own turns in. Guidance rows carry
// both run_id and instance_id; matching on either keeps older rows visible.
func (s *Server) transcriptGuidance(ctx context.Context, store platform.Repository, instanceID string, run *platform.Run) []transcriptItem {
	if store == nil || run == nil {
		return nil
	}
	rows, err := store.ListGuidance(ctx, run.TaskID, 500)
	if err != nil {
		return nil
	}
	out := make([]transcriptItem, 0, len(rows))
	for _, g := range rows {
		if g.InstanceID != instanceID && (run.ID == "" || g.RunID != run.ID) {
			continue
		}
		if strings.TrimSpace(g.Message) == "" {
			continue
		}
		out = append(out, transcriptItem{
			Kind: "human", Role: "human", Text: g.Message,
			Status: g.Status, At: g.CreatedAt,
		})
	}
	return out
}

// approvalTranscriptLine renders one approval event for the transcript. The
// line carries what an operator needs at a glance: kind, decision and who made
// it; the payload stays in the approval row.
func approvalTranscriptLine(eventType string, a *platform.Approval) string {
	kind := strings.TrimSpace(a.Kind)
	if kind == "" {
		kind = "approval"
	}
	if eventType == "approval_requested" || a.Status == platform.ApprovalPending {
		return fmt.Sprintf("审批请求：%s（等待决定）", kind)
	}
	line := fmt.Sprintf("审批 %s：%s", kind, a.Status)
	if strings.TrimSpace(a.DecidedBy) != "" {
		line += " · " + a.DecidedBy
	}
	if strings.TrimSpace(a.Reason) != "" {
		line += " · " + a.Reason
	}
	return line
}

// isToolEcho reports whether an assistant message is only a mirror of a tool
// result ("Tool '<name>' result: ..."). The engine emits both the tool_result
// event and this message; rendering both doubles every tool card.
func isToolEcho(content string) bool {
	first := content
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	return strings.HasPrefix(first, "Tool '") && strings.Contains(first, "' result:")
}

// appendConversationEvent writes an event into the run's journal so the chat
// transcript can render it. Approval events are broadcast-only today: SSE
// clients see them, but a reader of events.jsonl (transcript, scan detail
// replay, post-hoc audit) never does. Best effort — a journal write failure
// must not fail the decision that triggered it.
func (s *Server) appendConversationEvent(instanceID string, ev WSEvent) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return
	}
	dir := s.scanDirForRun(instanceID, nil)
	if dir == "" {
		return
	}
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().Format(time.RFC3339)
	}
	if ev.InstanceID == "" {
		ev.InstanceID = instanceID
	}
	if err := appendScanEventJournal(dir, ev); err != nil {
		log.Printf("[chat] append %s event for %s failed: %v", ev.Type, instanceID, err)
	}
}

func truncateTranscript(text string) (string, bool) {
	if len(text) <= transcriptMaxOutputChars {
		return text, false
	}
	return text[:transcriptMaxOutputChars], true
}

// instanceIsRunning reports whether the dispatch identified by instanceID is
// currently executing. The instance registry is the authority; a run without a
// live instance is idle (paused counts as not running).
func (s *Server) instanceIsRunning(instanceID string) bool {
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		return false
	}
	return inst.Status == "running"
}
