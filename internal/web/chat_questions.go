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

	// One in-flight question per run: the composer disables itself while
	// pending, so 409 only guards double tabs / raced retries (4zreco
	// chatBusy semantics — no queueing, the operator retries).
	if !s.beginQuestion(instanceID) {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "a question for this run is already being answered"})
		return
	}

	createdBy := ""
	if claims, ok := sessionClaimsFrom(r); ok {
		createdBy = claims.Username
	}
	snap := s.snapshotChatInstance(instanceID)
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

// snapshotChatInstance copies the instance's chat-visible fields under the
// instance lock: the answer must reason over the state at question time, not
// a live structure that keeps moving under the LLM call.
func (s *Server) snapshotChatInstance(instanceID string) *ScanInstance {
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		return nil
	}
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	cp := *inst //nolint:govet // intentional snapshot under RLock; the copy's mutex is never taken
	cp.Vulns = append([]VulnSummary(nil), inst.Vulns...)
	cp.events = append([]WSEvent(nil), inst.events...)
	cp.chatCfg = nil
	cp.chatMessages = nil
	cp.agent = nil
	return &cp
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
func buildQuestionAnswerPrompt(inst *ScanInstance, language string) string {
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
	writeScanChatSnapshot(&b, inst)
	return b.String()
}
