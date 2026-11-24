package web

// chat_questions.go — operator side-questions on a run (REDESIGN §5.2 chat
// layer; 4zreco chat-page semantics). A question sent to a running scan is
// answered by an independent, tool-less LLM call over a point-in-time scan
// context snapshot: it never enters the scan loop, never spends a scan
// iteration, and is persisted as chat_question / chat_reply journal events so
// the conversation transcript renders it like any other turn. The snapshot is
// a read-only projection — the answer is model self-description (charter §3.5
// lowest authority) and feeds no judgment path.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// questionAnswerTimeout bounds each side-question LLM call, mirroring the
// goal-analysis budget: an answer the operator waits on must fail loudly, not
// hang the composer forever.
const questionAnswerTimeout = 120 * time.Second

// handleTaskQuestions serves POST /api/tasks/{id}/questions, dispatched from
// handleTaskDetail. Accepted questions return 202 immediately; the answer
// arrives as a chat_reply journal event + SSE broadcast (no response body
// payload — the transcript is the transport, zero optimistic echo).
func (s *Server) handleTaskQuestions(w http.ResponseWriter, r *http.Request, store platform.Repository, taskID string) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	limitJSONBody(w, r)
	var req struct {
		Message    string `json:"message"`
		RequestID  string `json:"request_id"`
		InstanceID string `json:"instance_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" || len([]rune(req.Message)) > maxGuidanceCodePoints {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "message must be 1..4000 code points"})
		return
	}

	// Same target-resolution and ownership rules as guidance: an explicit
	// instance must be mounted on THIS task, else the task's running (or
	// latest) run wins. Questions to a finished run are answered from the
	// persisted snapshot; nothing here depends on a live agent.
	runID, instanceID, terr := s.resolveGuidanceRun(r.Context(), store, taskID, req.InstanceID)
	if terr != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": terr.Error()})
		return
	}
	if instanceID == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "no run mounted on this task"})
		return
	}

	createdBy := ""
	if claims, ok := sessionClaimsFrom(r); ok {
		createdBy = claims.Username
	}
	// Snapshot before claiming the slot: a run with no readable context (no
	// live instance and no durable scan records) must fail honestly instead of
	// queueing a question that can never be answered. This also closes the old
	// nil dereference that made side questions on finished runs 500.
	snap := s.snapshotChatInstance(instanceID)
	if snap == nil {
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": "no readable scan context is available for this run; the side assistant cannot answer",
		})
		return
	}

	// One in-flight question per run: the composer disables itself while
	// pending, so 409 only guards double tabs / raced retries (4zreco
	// chatBusy semantics — no queueing, the operator retries).
	if !s.beginQuestion(instanceID) {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "a question for this run is already being answered"})
		return
	}
	question := WSEvent{
		Type:       "chat_question",
		Content:    req.Message,
		InstanceID: instanceID,
		ResultMeta: map[string]any{"request_id": req.RequestID, "task_id": taskID, "run_id": runID, "created_by": createdBy},
	}
	s.appendConversationEvent(instanceID, question)
	s.broadcastToInstance(instanceID, question)

	language := ""
	if cfg := s.configSnapshot(); cfg != nil {
		language = cfg.Language
	}
	system := buildQuestionAnswerPrompt(snap, language)

	go s.answerRunQuestion(instanceID, createdBy, req.RequestID, system, req.Message)

	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"status":      "pending",
		"accepted":    true,
		"run_id":      runID,
		"instance_id": instanceID,
		"request_id":  req.RequestID,
	})
}

// beginQuestion claims the per-run in-flight slot. False means a question is
// already being answered for this run.
func (s *Server) beginQuestion(instanceID string) bool {
	s.questionsMu.Lock()
	defer s.questionsMu.Unlock()
	if s.questionsInFlight == nil {
		s.questionsInFlight = make(map[string]struct{})
	}
	if _, busy := s.questionsInFlight[instanceID]; busy {
		return false
	}
	s.questionsInFlight[instanceID] = struct{}{}
	return true
}

func (s *Server) endQuestion(instanceID string) {
	s.questionsMu.Lock()
	defer s.questionsMu.Unlock()
	delete(s.questionsInFlight, instanceID)
}

// snapshotChatInstance resolves the chat-visible state at question time: the
// live instance when this process still holds it, else the durable scan-record
// projection. The answer must reason over a point-in-time copy, not a live
// structure that keeps moving under the LLM call — and a run spanning several
// D11 session records (or a post-restart run) has no live instance at all.
func (s *Server) snapshotChatInstance(instanceID string) *chatScanSnapshot {
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst != nil {
		if snap := chatSnapshotFromInstance(inst); snap != nil {
			return snap
		}
	}
	return s.persistedChatSnapshot(instanceID)
}

// persistedChatSnapshot rebuilds the same snapshot from the durable scan
// summaries: the newest sibling session drives status/timing/instruction, the
// findings corpus merges across every session of the run (one record per
// instance is a projection blind spot, not the truth), and the newest tail
// supplies recent events.
func (s *Server) persistedChatSnapshot(instanceID string) *chatScanSnapshot {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	recs := idx.lookupAll(instanceID, "")
	if len(recs) == 0 {
		return nil
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].StartedAt > recs[j].StartedAt })
	newest := recs[0]

	snap := &chatScanSnapshot{
		ID:          instanceID,
		Status:      newest.Status,
		ScanMode:    newest.ScanMode,
		FinishedAt:  newest.FinishedAt,
		Instruction: newest.Instruction,
		Iterations:  newest.Iterations,
		ToolCalls:   newest.ToolCalls,
		TotalTokens: newest.TotalTokens,
		Vulns:       make([]VulnSummary, 0, len(newest.Vulns)),
		Events:      append([]WSEvent(nil), newest.Events...),
	}
	// Timestamps span the whole run: earliest start, latest finish.
	for _, rec := range recs {
		if rec.StartedAt != "" && (snap.StartedAt == "" || rec.StartedAt < snap.StartedAt) {
			snap.StartedAt = rec.StartedAt
		}
		if rec.FinishedAt > snap.FinishedAt {
			snap.FinishedAt = rec.FinishedAt
		}
	}
	snap.Targets = mergeChatTargets(recs)
	for _, rec := range recs {
		for _, vuln := range rec.Vulns {
			appendVulnSummaryUnique(&snap.Vulns, vuln)
		}
	}
	snap.VulnCount = len(snap.Vulns)
	return snap
}

// mergeChatTargets lists the run's distinct targets (a multi-target dispatch
// or a re-dispatch can cover more than the newest record's target).
func mergeChatTargets(recs []*ScanRecord) string {
	seen := make(map[string]bool, len(recs))
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		target := strings.TrimSpace(rec.Target)
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		out = append(out, target)
	}
	return strings.Join(out, ", ")
}

// answerRunQuestion runs the tool-less responder and lands the answer in the
// journal + SSE. Failure is honest: a failed chat_reply row the transcript
// renders, never silence.
func (s *Server) answerRunQuestion(instanceID, createdBy, requestID, system, question string) {
	defer s.endQuestion(instanceID)

	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerTimeout)
	defer cancel()

	reply := WSEvent{
		Type:       "chat_reply",
		InstanceID: instanceID,
		AgentID:    "operator_chat",
		ResultMeta: map[string]any{"request_id": requestID, "answered_by": createdBy},
	}
	if s.scanQuestionFn == nil {
		// Never panic in this goroutine: a nil fact would kill the process, and
		// the operator still deserves an honest failed row.
		reply.Error = "side assistant is not configured"
		reply.ResultMeta["status"] = "failed"
		s.appendConversationEvent(instanceID, reply)
		s.broadcastToInstance(instanceID, reply)
		return
	}
	answer, err := s.scanQuestionFn(ctx, system, question)
	if err != nil {
		log.Printf("[chat] side-question for %s failed: %v", instanceID, err)
		reply.Error = err.Error()
		reply.ResultMeta["status"] = "failed"
	} else {
		answer = strings.TrimSpace(llm.CleanContent(answer))
		if answer == "" {
			answer = "I do not have enough scan context to answer that."
		}
		reply.Content = answer
		reply.ResultMeta["status"] = "answered"
	}
	s.appendConversationEvent(instanceID, reply)
	s.broadcastToInstance(instanceID, reply)
}

// buildQuestionAnswerPrompt assembles the responder's system prompt: a live
// framing preamble over the same scan snapshot body post-scan chat uses, so
// both channels see identical context discipline (bounded vulns/events tail).
func buildQuestionAnswerPrompt(snap *chatScanSnapshot, language string) string {
	var b strings.Builder
	if directive := config.OutputLanguageDirective(language); directive != "" {
		b.WriteString(directive)
		b.WriteString("\n\n")
	}
	b.WriteString(`You are Xalgorix's operator-side assistant. The assessment below is in progress on this machine; the operator is asking you a question about it from the conversation page.

Rules:
- Answer ONLY from the snapshot in this prompt. It is the latest streamed scan state and may lag by a few seconds; say so when recency matters.
- You cannot run tools, restart work, or affect the scan from here. If the operator asks for new testing, summarize what the snapshot already shows for that topic and point them at the composer's directive mode (or the approvals queue) instead.
- Never invent findings, endpoints, evidence or scores that are not in the snapshot.
- Be direct and concise; answer in the operator's language.

`)
	writeScanChatSnapshot(&b, snap)
	return b.String()
}
