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

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
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
	// CreatedBy is the operator who produced a human turn (guidance author,
	// side-question asker). Empty for agent output. The human-only projection
	// keeps the per-user filtering the old guidance view had.
	CreatedBy string    `json:"created_by,omitempty"`
	At        time.Time `json:"at"`
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
	// Proposal carries a main-chat intent draft (pending) or its resolution.
	// It is a transcript projection of the intent_proposed/
	// intent_proposal_resolved events, never plan state.
	Proposal *proposalView `json:"proposal,omitempty"`
}

// proposalView is the transcript projection of a main-chat intent draft. The
// card is actionable only while status=pending; the confirm/reject endpoints
// live under /api/conversations/{instanceID}/intent-proposals/.
type proposalView struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id,omitempty"`
	Title      string `json:"title,omitempty"`
	Target     string `json:"target,omitempty"`
	TargetKind string `json:"target_kind,omitempty"`
	Phase      int    `json:"phase,omitempty"`
	VulnClass  string `json:"vuln_class,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Notes      string `json:"notes,omitempty"`
	Status     string `json:"status,omitempty"`
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
	case len(parts) == 4 && parts[1] == "intent-proposals" && parts[3] == "confirm" && r.Method == http.MethodPost:
		s.handleIntentProposalDecision(w, r, store, instanceID, parts[2], true)
	case len(parts) == 4 && parts[1] == "intent-proposals" && parts[3] == "reject" && r.Method == http.MethodPost:
		s.handleIntentProposalDecision(w, r, store, instanceID, parts[2], false)
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
	// ?human=1 narrows the projection to the operator conversation (human
	// turns + side-question answers). The human page is a conversation, not a
	// trace: tool calls, plans, workers and lifecycle stay in the full
	// transcript. Filtering here keeps one persisted authority for both views.
	if r.URL.Query().Get("human") == "1" {
		items = humanTranscriptItems(items)
	}
	// ?unit=<intent> narrows the projection to one work-unit lane. A unit can
	// be dispatched more than once (lease reclaim, retry, re-dispatch), so the
	// lane is the union of all its agent ids: one worker session spans every
	// attempt instead of splitting history across agent lanes.
	if unit := strings.TrimSpace(r.URL.Query().Get("unit")); unit != "" {
		items = unitTranscriptItems(items, s.unitAgentIDs(ctx, instanceID, unit))
	} else if agent := strings.TrimSpace(r.URL.Query().Get("agent")); agent != "" {
		// ?agent=<agent_id> is the legacy single-instance lane (model-initiated
		// spawns and old deep links). The session rail opens a worker's
		// transcript directly, and the full transcript's newest page may contain
		// nothing from that agent (long runs, many lanes), which used to render
		// as "no activity" with no way to page back. Filtering before pagination
		// makes the lane's own history the page authority.
		items = agentTranscriptItems(items, agent)
	}
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
	items = collapseProposalStatuses(items)
	for i := range items {
		items[i].Seq = i + 1
	}
	return items
}

// collapseProposalStatuses keeps one card per proposal id and folds a later
// resolution event into it. Without this, confirming a proposal would append
// a second "confirmed" card next to the still-pending one.
func collapseProposalStatuses(items []transcriptItem) []transcriptItem {
	out := make([]transcriptItem, 0, len(items))
	index := make(map[string]int)
	for _, item := range items {
		if item.Kind != "proposal" || item.Proposal == nil || strings.TrimSpace(item.Proposal.ID) == "" {
			out = append(out, item)
			continue
		}
		id := item.Proposal.ID
		if at, seen := index[id]; seen {
			if status := strings.TrimSpace(item.Proposal.Status); status != "" && status != "pending" {
				out[at].Proposal.Status = status
				out[at].Proposal.TaskID = item.Proposal.TaskID
			}
			continue
		}
		index[id] = len(out)
		out = append(out, item)
	}
	return out
}

// transcriptEvents reads the scan journals behind this run. The conversation
// surface is per-instance: a D11 re-dispatch continues the same conversation
// in a fresh session directory (the old journal is replayed into the prompt
// only), and multi-target dispatches add further sessions — so the transcript
// merges every sibling session's journal, oldest session first. Resolution
// follows the same path as the scan detail page so a wildcard child or an
// imported run lands on the same directories. With a state backend registered
// (R6.1) the merge is one durable instance-union read; the per-session files
// remain the fallback for runs whose rows the import never landed.
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
	if b := currentEventStateBackend(); b != nil && strings.TrimSpace(instanceID) != "" {
		if s.appendRunStateTranscript(b, instanceID, seen, &out) {
			return out
		}
	}
	for _, dir := range dirs {
		s.appendSessionTranscript(dir, seen, &out)
	}
	return out
}

// appendRunStateTranscript folds the run's durable event union (every sibling
// session, oldest session first) into the transcript timeline. Reports false
// when the durable journal has nothing for this run yet — the caller then
// reads the legacy per-session files.
func (s *Server) appendRunStateTranscript(b EventStateBackend, instanceID string, seen map[string]struct{}, out *[]transcriptItem) bool {
	ctx, cancel := eventStateCtx()
	defer cancel()
	recs, total, err := b.LoadEventsByInstance(ctx, instanceID, 0, transcriptMaxEvents)
	if err != nil {
		log.Printf("[chat] durable transcript read for %s failed, trying legacy files: %v", instanceID, err)
		return false
	}
	if total == 0 {
		return false
	}
	for _, r := range recs {
		foldTranscriptLine(r.Payload, seen, out)
	}
	return true
}

// appendSessionTranscript folds one session directory's events.jsonl into the
// run transcript timeline.
func (s *Server) appendSessionTranscript(dir string, seen map[string]struct{}, out *[]transcriptItem) {
	f, err := os.Open(filepath.Join(dir, scanEventJournalFile))
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
		foldTranscriptLine(scanner.Bytes(), seen, out)
	}
}

// foldTranscriptLine turns one journal line (file bytes or a durable row's
// payload) into transcript items. Unknown event types are skipped rather than
// guessed, so a future emitter cannot silently inject a fake bubble.
func foldTranscriptLine(line []byte, seen map[string]struct{}, out *[]transcriptItem) {
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
		ResultMeta  struct {
			CreatedBy  string   `json:"created_by"`
			AnsweredBy string   `json:"answered_by"`
			Surface    string   `json:"surface"`
			ProposalID string   `json:"proposal_id"`
			TaskID     string   `json:"task_id"`
			Title      string   `json:"title"`
			Target     string   `json:"target"`
			TargetKind string   `json:"target_kind"`
			Phase      int      `json:"phase"`
			VulnClass  string   `json:"vuln_class"`
			Endpoint   string   `json:"endpoint"`
			Notes      string   `json:"notes"`
			DependsOn  []string `json:"depends_on"`
			Status     string   `json:"status"`
		} `json:"result_meta"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	at := ev.At
	if at.IsZero() {
		at = time.Now()
	}
	key := fmt.Sprintf("%s|%s|%s|%s|%s", ev.Type, ev.Tool, ev.Content, ev.Output, at.Format(time.RFC3339))
	if _, dup := seen[key]; dup {
		return
	}
	seen[key] = struct{}{}

	switch ev.Type {
	case "message", "assistant_done":
		// assistant_done carries the full text of a streamed turn; the deltas
		// before it have no transcript case on purpose, so the turn lands as
		// exactly one assistant row (live clients fold the deltas themselves
		// via message_id).
		if strings.TrimSpace(ev.Content) == "" || isToolEcho(ev.Content) {
			return
		}
		// surface=main marks the R7 main agent's own replies. The human
		// projection includes them (it is a conversation), while worker
		// narration keeps role=agent and stays in the full trace.
		role := "agent"
		if strings.EqualFold(strings.TrimSpace(ev.ResultMeta.Surface), "main") {
			role = "main"
		}
		*out = append(*out, transcriptItem{Kind: "assistant", Role: role, Text: ev.Content, AgentID: ev.AgentID, At: at})
	case "intent_proposed":
		if strings.TrimSpace(ev.ResultMeta.ProposalID) == "" {
			return
		}
		status := strings.TrimSpace(ev.ResultMeta.Status)
		if status == "" {
			status = "pending"
		}
		*out = append(*out, transcriptItem{
			Kind: "proposal",
			At:   at,
			Proposal: &proposalView{
				ID:         ev.ResultMeta.ProposalID,
				TaskID:     ev.ResultMeta.TaskID,
				Title:      ev.ResultMeta.Title,
				Target:     ev.ResultMeta.Target,
				TargetKind: ev.ResultMeta.TargetKind,
				Phase:      ev.ResultMeta.Phase,
				VulnClass:  ev.ResultMeta.VulnClass,
				Endpoint:   ev.ResultMeta.Endpoint,
				Notes:      ev.ResultMeta.Notes,
				Status:     status,
			},
		})
	case "intent_proposal_resolved":
		if strings.TrimSpace(ev.ResultMeta.ProposalID) == "" {
			return
		}
		status := strings.TrimSpace(ev.ResultMeta.Status)
		if status == "" {
			status = "confirmed"
		}
		*out = append(*out, transcriptItem{
			Kind: "proposal",
			At:   at,
			Proposal: &proposalView{
				ID:     ev.ResultMeta.ProposalID,
				TaskID: ev.ResultMeta.TaskID,
				Title:  ev.ResultMeta.Title,
				Status: status,
			},
		})
	case "chat_question":
		// Operator side-question (chat_questions.go): renders like a
		// human turn; no guidance lifecycle status behind it.
		if strings.TrimSpace(ev.Content) == "" {
			return
		}
		*out = append(*out, transcriptItem{Kind: "human", Role: "human", Text: ev.Content, CreatedBy: ev.ResultMeta.CreatedBy, At: at})
	case "chat_reply":
		// The side-question responder's answer (or its honest failure).
		// Role "chat" lets the UI badge it apart from main-loop agent
		// narration; a failed call keeps its row so the operator sees
		// the error instead of silence.
		item := transcriptItem{Kind: "assistant", Role: "chat", AgentID: ev.AgentID, CreatedBy: ev.ResultMeta.AnsweredBy, At: at}
		if strings.TrimSpace(ev.Error) != "" {
			// Text stays the raw error; the UI renders the localized
			// "answer failed" framing from Status.
			item.Status = "failed"
			item.Text = ev.Error
		} else {
			item.Text = ev.Content
		}
		if strings.TrimSpace(item.Text) == "" {
			return
		}
		*out = append(*out, item)
	case "tool_call":
		*out = append(*out, transcriptItem{Kind: "tool_use", Tool: ev.Tool, Args: ev.Args, Text: ev.Content, AgentID: ev.AgentID, Phase: ev.Phase, At: at})
	case "tool_result":
		text, cut := truncateTranscript(ev.Output)
		*out = append(*out, transcriptItem{Kind: "tool_result", Tool: ev.Tool, Output: text, Truncated: cut, AgentID: ev.AgentID, Phase: ev.Phase, At: at})
	case "approval_requested", "approval_decided":
		if ev.Approval == nil {
			return
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

// transcriptGuidance folds the operator's own turns in. Guidance rows carry
// both run_id and instance_id; matching on either keeps older rows visible.
// humanTranscriptItems keeps the operator conversation: human turns (guidance
// and side questions), side-question answers, the main agent's own replies
// (role=main), and intent proposal cards (the confirmation affordance). Tool
// calls, plans, workers and lifecycle rows stay in the full transcript. Seq is
// renumbered so the pagination contract ("1-based contiguous") still holds
// for the narrowed projection.
func humanTranscriptItems(items []transcriptItem) []transcriptItem {
	out := make([]transcriptItem, 0, len(items))
	for _, item := range items {
		if item.Kind == "human" || item.Kind == "proposal" || item.Role == "chat" || item.Role == "main" {
			out = append(out, item)
		}
	}
	for i := range out {
		out[i].Seq = i + 1
	}
	return out
}

// agentTranscriptItems keeps only rows emitted by one delegated agent, then
// renumbers seq contiguously so before_seq pagination over the filtered view
// stays coherent (same discipline as humanTranscriptItems).
func agentTranscriptItems(items []transcriptItem, agent string) []transcriptItem {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return items
	}
	out := make([]transcriptItem, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.AgentID)
		// Event agent ids are usually the bare sub id; a sibling-session or
		// prefixed id ("<instance>/sub_1") must match the same lane.
		if id == agent || strings.HasSuffix(id, "/"+agent) {
			out = append(out, item)
		}
	}
	for i := range out {
		out[i].Seq = i + 1
	}
	return out
}

// unitTranscriptItems keeps only rows emitted by any agent that ran one work
// unit, then renumbers seq contiguously. Retries append to the same lane, so
// the unit's transcript is the union of its attempts, not the newest one.
func unitTranscriptItems(items []transcriptItem, agentIDs []string) []transcriptItem {
	if len(agentIDs) == 0 {
		return []transcriptItem{}
	}
	want := make(map[string]bool, len(agentIDs))
	for _, id := range agentIDs {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	out := make([]transcriptItem, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.AgentID)
		if id == "" {
			continue
		}
		matched := want[id]
		if !matched {
			for agentID := range want {
				if strings.HasSuffix(id, "/"+agentID) {
					matched = true
					break
				}
			}
		}
		if matched {
			out = append(out, item)
		}
	}
	for i := range out {
		out[i].Seq = i + 1
	}
	return out
}

// unitAgentIDs resolves a work unit to every agent that ran it: the live
// agentsgraph first (a dispatch may not have reached its lifecycle boundary
// yet), then the durable run_workers rows.
func (s *Server) unitAgentIDs(ctx context.Context, instanceID, unitID string) []string {
	unitID = strings.TrimSpace(unitID)
	if unitID == "" {
		return nil
	}
	seen := make(map[string]bool)
	out := make([]string, 0, 2)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst != nil {
		inst.mu.RLock()
		live := inst.agent
		inst.mu.RUnlock()
		if live != nil {
			for _, a := range live.DelegationSnapshot().Agents {
				if a.UnitID == unitID {
					add(a.AgentID)
				}
			}
		}
	}
	for _, rec := range s.workerHistory(instanceID) {
		if rec.UnitID == unitID {
			add(rec.AgentID)
		}
	}
	return out
}

// transcriptGuidance folds the task's guidance rows (the human turns the
// platform store owns) into the timeline. Status rides along so the human
// view can show injected/queued/consumed without a second query.
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
			Status: g.Status, CreatedBy: g.CreatedBy, At: g.CreatedAt,
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
