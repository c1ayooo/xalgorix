package agent

// main_chat.go — the R7 main agent as a per-message chat.
//
// The main agent is the human interface only: a user message wakes exactly one
// chat turn. Its tools are propose_intent / confirm_intent / add_hint / finish; it never executes
// tools, never scans, and never runs an autonomous planning loop. Workers own
// execution; the orchestrator planner owns autonomous intent generation.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

var mainChatToolDefs = []llm.ToolDefinition{
	{
		Name:        "propose_intent",
		Description: "Draft a concrete work intent for the worker pool. The proposal is NOT written to the plan: the operator must explicitly confirm it (transcript button or a later confirmation message). Use when the user asks for specific work. One intent = one endpoint (or whole-target class) + vulnerability class + methodology phase.",
		Parameters: []llm.ToolParameter{
			{Name: "title", Description: "Concrete task title.", Required: true},
			{Name: "phase", Description: "Methodology phase 1-22.", Required: false},
			{Name: "vuln_class", Description: "Vulnerability class, e.g. sqli/xss/idor/ssrf.", Required: false},
			{Name: "endpoint", Description: "Specific endpoint/path; omit for a whole-target class.", Required: false},
			{Name: "target", Description: "The real target for this intent (host, URL, or IP:port). Required for dispatch.", Required: true},
			{Name: "target_kind", Description: "control_plane_api | container | web | task. Use control_plane_api only for platform API calls; never for scanning the platform.", Required: false},
			{Name: "notes", Description: "Why this intent exists / what evidence to collect.", Required: false},
			{Name: "depends_on", Description: "Comma-separated task ids that must settle first.", Required: false},
		},
	},
	{
		Name:        "add_hint",
		Description: "Record a human guidance/preference as a hint node. Use for context the workers/planner should respect, without creating a concrete work item.",
		Parameters: []llm.ToolParameter{
			{Name: "message", Description: "The guidance text.", Required: true},
		},
	},
	{
		Name:        "finish",
		Description: "Finish the run at the user's explicit request. Refused while open/running/paused intents remain; wait for the worker pool or steer/kill first.",
		Parameters: []llm.ToolParameter{
			{Name: "reason", Description: "Why the run should finish.", Required: false},
		},
	},
	{
		Name:        "list_intents",
		Description: "READ-ONLY: list the run's work intents (the authoritative plan graph). Use it to answer questions about what is planned, running, done or blocked. It never mutates state.",
		Parameters: []llm.ToolParameter{
			{Name: "status", Description: "Filter: all (default), open, running, paused, done, blocked.", Required: false},
			{Name: "phase", Description: "Only intents of this methodology phase (1-22).", Required: false},
			{Name: "limit", Description: "Max rows (default 20, max 50).", Required: false},
		},
	},
	{
		Name:        "list_units",
		Description: "READ-ONLY: list the exploration-graph work units (intent and belief nodes) with their state, owner and lease. Use it to see what workers are actively holding. It never mutates state.",
		Parameters: []llm.ToolParameter{
			{Name: "kind", Description: "Filter: all (default), intent, belief.", Required: false},
			{Name: "state", Description: "Filter by graph state, e.g. open, running, paused, done, blocked, queued, testing, proven.", Required: false},
			{Name: "limit", Description: "Max rows (default 20, max 50).", Required: false},
		},
	},
	{
		Name:        "list_findings",
		Description: "READ-ONLY: list confirmed findings for this run with severity, endpoint, CVSS and verification tag. Use it to answer questions about results. It never mutates state.",
		Parameters: []llm.ToolParameter{
			{Name: "severity", Description: "Filter: all (default), critical, high, medium, low, info.", Required: false},
			{Name: "limit", Description: "Max rows (default 20, max 50).", Required: false},
		},
	},
	{
		Name:        "list_hypotheses",
		Description: "READ-ONLY: list ledger hypotheses with status, confidence and evidence count. Use it to answer questions about what is being tested or already disproven. It never mutates state.",
		Parameters: []llm.ToolParameter{
			{Name: "status", Description: "Filter: all (default), queued, testing, proven, rejected, blocked, exhausted.", Required: false},
			{Name: "limit", Description: "Max rows (default 20, max 50).", Required: false},
		},
	},
	{
		Name:        "list_hints",
		Description: "READ-ONLY: list operator guidance (hint nodes) recorded for this run. Hints are observed human input and remain active until the run ends; use this to recall standing instructions. It never mutates state.",
		Parameters: []llm.ToolParameter{
			{Name: "state", Description: "Filter: all (default), active, consumed.", Required: false},
			{Name: "limit", Description: "Max rows (default 20, max 50).", Required: false},
		},
	},
	{
		Name:        "steer_work",
		Description: "DECISION: send a course correction to the worker executing one intent. The worker receives it before its next model turn; it does not stop the run. Use the plan task id (unit id).",
		Parameters: []llm.ToolParameter{
			{Name: "unit_id", Description: "Plan task id (intent unit) whose worker should receive the correction.", Required: true},
			{Name: "message", Description: "Concrete correction: what to stop, what to focus on, and why.", Required: true},
		},
	},
	{
		Name:        "kill_work",
		Description: "DECISION: cancel the worker executing one intent. The scheduler settles the intent as blocked. Provide a typed reason_code; the engine refuses a stagnant kill while the worker produced information-layer progress in the last two minutes.",
		Parameters: []llm.ToolParameter{
			{Name: "unit_id", Description: "Plan task id (intent unit) whose worker should be cancelled.", Required: true},
			{Name: "reason_code", Description: "Typed cause: human (default), stagnant, repeated_failure, lane_violation, scope_violation, duplicate, superseded.", Required: false},
			{Name: "reason", Description: "Why the worker is being cancelled (audited).", Required: false},
		},
	},
	{
		Name:        "retire_intent",
		Description: "DECISION: retire an OPEN intent that should no longer be worked (direction exhausted, superseded, duplicate). Only an open intent can be retired; running/paused/done/blocked intents are refused and must be settled by the worker pool. Records actor=human.",
		Parameters: []llm.ToolParameter{
			{Name: "task_id", Description: "Plan task id (unit id) of the open intent to retire.", Required: false},
			{Name: "unit_id", Description: "Alias for task_id when the caller addresses the unit by its graph id.", Required: false},
			{Name: "reason", Description: "Why the direction is being retired (audited).", Required: true},
		},
	},
	{
		Name:        "reprioritize_intent",
		Description: "DECISION: change one intent's claim priority without changing its state (higher runs first). Use it to push an urgent direction ahead of the planner's phase order, or to demote a low-value one. Records actor=human.",
		Parameters: []llm.ToolParameter{
			{Name: "unit_id", Description: "Plan task id (intent unit) to reprioritize.", Required: true},
			{Name: "priority", Description: "New claim priority, integer 0..100000 (higher is claimed first).", Required: true},
			{Name: "reason", Description: "Why the priority changes (audited).", Required: false},
		},
	},
}

// mainConfirmIntentToolDef is offered only while a proposal is pending: the
// model must not be able to confirm work the operator never proposed to it.
var mainConfirmIntentToolDef = llm.ToolDefinition{
	Name:        "confirm_intent",
	Description: "Write a pending intent proposal to the plan graph. Call ONLY when the operator's CURRENT message explicitly confirms a proposal created in an EARLIER turn (e.g. 确认/执行/开始/派发/confirm/go ahead). Never call it in the same turn that created the proposal, and never confirm a proposal the operator did not explicitly accept.",
	Parameters: []llm.ToolParameter{
		{Name: "proposal_id", Description: "The proposal id returned by propose_intent.", Required: true},
	},
}

func mainChatToolDefsFor(hasPendingProposal bool) []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(mainChatToolDefs)+1)
	defs = append(defs, mainChatToolDefs...)
	if hasPendingProposal {
		defs = append(defs, mainConfirmIntentToolDef)
	}
	return defs
}

func (a *Agent) runMainChatOnly(targets []string, instruction string) {
	if a.scanCtx == nil || a.scanCtx.ID == "" {
		a.emit(Event{Type: "finished", Content: a.op("Scan stopped: scan context is unavailable.", "扫描已停止：扫描上下文不可用。"), Aborted: true, AbortReason: "main_chat_unavailable", TotalTokens: a.syncBudgetTokens()})
		return
	}
	a.targets = append([]string(nil), targets...)
	a.taskInstruction = instruction
	a.scanCtx.SetTargets(a.targets)
	if a.state == nil {
		a.state = NewScanState()
	}
	a.state.ScanContextID = a.scanCtx.ID
	a.state.PlannerWorkerMode = true
	if a.objective != nil {
		a.state.Objective = a.objective
	}
	defer a.syncExplorationGraph()
	a.runMainChatLoop(a.syncBudgetTokens)
}

// mainChatInitialHistory seeds the main chat with the bounded hot-resume
// transcript. The replay was already assembled by the session layer (human
// turns, main-agent replies and durable event projection); without it a
// restarted run lost every earlier operator/agent turn.
func (a *Agent) mainChatInitialHistory(system string) []llm.Message {
	history := []llm.Message{{Role: "system", Content: system}}
	if replay := strings.TrimSpace(a.replayTranscript); replay != "" {
		history = append(history, llm.Message{
			Role:    "user",
			Content: "Previous conversation transcript (context only; the operator's next message follows):\n" + replay,
		})
	}
	return history
}

// chatMainTurnStreamed runs one main-chat model turn with incremental
// assistant_delta events and a final assistant_done carrying the full text.
// The terminal event is tagged surface=main so the human transcript projection
// keeps it as an operator-facing reply instead of worker narration.
func (a *Agent) chatMainTurnStreamed(history []llm.Message, defs []llm.ToolDefinition) (string, []llm.ToolCall, error) {
	messageID := fmt.Sprintf("main-%d-%d", a.mainTurn, time.Now().UnixNano())
	var text strings.Builder
	fragments := make(map[int]*llm.StreamToolCall)
	var streamErr error
	for chunk := range a.client.ChatStreamWithTools(history, defs) {
		if chunk.Err != nil {
			streamErr = chunk.Err
			break
		}
		if chunk.Content != "" {
			text.WriteString(chunk.Content)
			a.emit(Event{
				Type: "assistant_delta", MessageID: messageID,
				Delta: chunk.Content, Content: chunk.Content,
				TotalTokens: a.syncBudgetTokens(),
			})
		}
		for _, fragment := range chunk.ToolCalls {
			entry := fragments[fragment.Index]
			if entry == nil {
				entry = &llm.StreamToolCall{Index: fragment.Index}
				fragments[fragment.Index] = entry
			}
			if fragment.ID != "" {
				entry.ID = fragment.ID
			}
			if fragment.Name != "" {
				entry.Name += fragment.Name
			}
			entry.Arguments += fragment.Arguments
		}
	}
	content := text.String()
	if content != "" {
		a.emit(Event{
			Type: "assistant_done", MessageID: messageID, Content: content,
			Meta:        map[string]any{"surface": "main"},
			TotalTokens: a.syncBudgetTokens(),
		})
	}
	if streamErr != nil {
		// A provider without a streaming tool-call channel can fail before any
		// content: fall back to the non-streaming call so the chat still works.
		if content == "" && len(fragments) == 0 {
			fallback, calls, _, ferr := a.client.ChatWithUsageToolCalls(history, defs)
			if ferr == nil {
				if strings.TrimSpace(fallback) != "" {
					a.emit(Event{Type: "message", Content: fallback, Meta: map[string]any{"surface": "main"}})
				}
				return fallback, calls, nil
			}
		}
		return content, nil, streamErr
	}

	indexes := make([]int, 0, len(fragments))
	for index := range fragments {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	calls := make([]llm.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		fragment := fragments[index]
		name := strings.TrimSpace(fragment.Name)
		if name == "" {
			continue
		}
		args, err := llm.ParseToolArguments(fragment.Arguments)
		call := llm.ToolCall{
			ID:           fragment.ID,
			Name:         name,
			Args:         args,
			RawArguments: fragment.Arguments,
		}
		if err != nil {
			call.ArgumentError = err.Error()
		}
		calls = append(calls, call)
	}
	return content, llm.EnsureToolCallIDs(calls, fmt.Sprintf("main-%d", a.mainTurn)), nil
}

func (a *Agent) runMainChatLoop(tokenCount func() int) {
	if a.client == nil || a.mainInbox == nil {
		a.emit(Event{Type: "finished", Content: a.op("Scan stopped: main chat is unavailable.", "扫描已停止：主会话不可用。"), Aborted: true, AbortReason: "main_chat_unavailable", TotalTokens: tokenCount()})
		return
	}
	system := a.mainChatSystemPrompt()
	history := a.mainChatInitialHistory(system)
	for {
		var userMsg string
		isSystem := false
		select {
		case <-a.ctx.Done():
			return
		case userMsg = <-a.mainInbox:
		case userMsg = <-a.mainSystemInbox:
			isSystem = true
		}
		if a.stopped.Load() {
			return
		}
		if isSystem {
			userMsg = "[SYSTEM] " + userMsg
		}
		a.mainTurn++
		history = append(history, llm.Message{Role: "user", Content: a.mainChatUserMessage(userMsg)})
		history = a.trimMainChatHistory(history, 24)
		finished := false
		// Resolve the tool face once per user turn: a proposal created in THIS
		// turn must not make confirm_intent available to the same turn. The
		// model can still suggest it, but only a later user message can carry
		// the confirmation.
		defs := mainChatToolDefsFor(a.pendingMainProposalCount() > 0)
		// 6 turns covers a couple of read-only lookups plus the answer; the
		// tools never loop on their own, so this stays a hard bound.
		for turn := 0; turn < 6 && !finished; turn++ {
			content, calls, err := a.chatMainTurnStreamed(history, defs)
			if err != nil {
				a.emit(Event{Type: "message", Content: a.op("⚠️ Main agent model call failed: ", "⚠️ 主 Agent 模型调用失败：") + err.Error()})
				break
			}
			if strings.TrimSpace(content) != "" {
				// surface=main lets the human projection include the main
				// agent's replies without also including worker narration.
				a.emit(Event{Type: "message", Content: content, Meta: map[string]any{"surface": "main"}})
			}
			if len(calls) == 0 {
				break
			}
			history = append(history, llm.Message{
				Role:      "assistant",
				Content:   content,
				ToolCalls: mainChatMessageToolCalls(calls),
			})
			for _, call := range calls {
				result, done := a.executeMainChatTool(call, userMsg)
				history = append(history, llm.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    result,
				})
				if done {
					finished = true
					break
				}
			}
		}
		if finished {
			return
		}
	}
}

// NotifyMainSystem wakes the main chat for a completion check (frontier
// drained). It is not a user message and carries no work.
func (a *Agent) NotifyMainSystem(msg string) {
	if a == nil || a.mainSystemInbox == nil || strings.TrimSpace(msg) == "" {
		return
	}
	select {
	case a.mainSystemInbox <- msg:
	default:
	}
}

func (a *Agent) mainChatSystemPrompt() string {
	var b strings.Builder
	b.WriteString("You are the main agent / human interface of an authorized penetration test. ")
	b.WriteString("You do NOT execute tools, scans, or tests. Workers execute intents; the planner also inserts autonomous intents. ")
	b.WriteString("NEVER write the plan directly. When the user asks for work, call propose_intent with a concrete target/endpoint/class/phase; the proposal is a DRAFT outside the plan until the operator explicitly confirms it. Tell the operator it needs confirmation. ")
	b.WriteString("Call confirm_intent ONLY when the operator's CURRENT message explicitly confirms a pending proposal that was created in an EARLIER turn (确认/执行/开始/派发/confirm/go ahead). Never confirm in the same turn you proposed, and never confirm without an explicit operator instruction. The transcript button is an equivalent confirmation and does not go through you. ")
	b.WriteString("When the user gives context or a preference without a concrete task, call add_hint. Never put a concrete work item in a hint. ")
	b.WriteString("To correct running work you may steer_work (send a correction to one worker), kill_work (cancel one worker with a typed reason_code), retire_intent (delete an OPEN intent that should no longer be worked), or reprioritize_intent (change one intent's claim priority without changing its state). These are decisions, not execution: you still never run a tool, scan, or report yourself. ")
	b.WriteString("When the user asks about the current run, call the READ-ONLY query tools first: list_intents (plan), list_units (in-progress intent/belief units and owners), list_findings (confirmed findings), list_hypotheses (ledger beliefs), list_hints (operator guidance). Answer only from their output; never invent progress, findings or evidence. ")
	b.WriteString("Only call finish when the user explicitly asks to stop/finish, or when a [SYSTEM] frontier-drained message arrives and the user's goal is complete. The tool refuses while open/running intents or unconfirmed proposals remain. ")
	b.WriteString("Reply concisely in the operator's language. Do not claim work you did not delegate.\n")
	if len(a.targets) > 0 {
		fmt.Fprintf(&b, "Target: %s\n", strings.Join(a.targets, ", "))
	}
	if a.state != nil && a.state.Objective != nil {
		fmt.Fprintf(&b, "Objective: %s\n", a.state.Objective.Statement)
	}
	if a.taskInstruction != "" {
		fmt.Fprintf(&b, "Task instruction (authoritative user intent):\n%s\n", truncateMainChatText(a.taskInstruction, 6000))
	}
	if len(a.controlPlaneHosts) > 0 {
		fmt.Fprintf(&b, "Control-plane hosts (never scan/exploit): %s\n", strings.Join(a.controlPlaneHosts, ", "))
		b.WriteString("For a platform API call, call propose_intent with target_kind=control_plane_api and target set to the platform. For a challenge container, use target_kind=container and target set to the container address. Never use a control-plane host as a web/container target.\n")
	}
	b.WriteString("Each propose_intent requires a target; there is no implicit scan target.\n")
	return b.String()
}

func (a *Agent) mainChatUserMessage(msg string) string {
	var b strings.Builder
	if counts, err := a.mainChatCounts(); err == nil {
		fmt.Fprintf(&b, "[GRAPH] open=%d running=%d paused=%d done=%d blocked=%d\n",
			counts.OpenIntents, counts.RunningIntents, counts.PausedIntents, counts.DoneIntents, counts.BlockedIntents)
	}
	if pending := a.pendingMainProposalList(); len(pending) > 0 {
		fmt.Fprintf(&b, "[PROPOSALS] %d pending: ", len(pending))
		for i, p := range pending {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "%s=%s", p.ID, truncateMainChatText(p.Title, 80))
		}
		b.WriteString(" — written only on explicit operator confirmation\n")
	}
	b.WriteString("[USER] ")
	b.WriteString(strings.TrimSpace(msg))
	return b.String()
}

func (a *Agent) mainChatCounts() (graph.Counts, error) {
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" {
		return graph.Counts{}, fmt.Errorf("graph unavailable")
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	return repo.Counts(ctx, scanID)
}

func (a *Agent) executeMainChatTool(call llm.ToolCall, userMsg string) (string, bool) {
	a.emit(Event{Type: "tool_call", ToolName: call.Name, ToolArgs: call.Args})
	result, finished := a.mainChatToolResult(call, userMsg)
	meta := map[string]any{"success": !strings.HasPrefix(result, "error:")}
	a.emit(Event{Type: "tool_result", ToolName: call.Name, ToolResult: tools.Result{Output: result, Metadata: meta}, TotalTokens: a.syncBudgetTokens()})
	return result, finished
}

func (a *Agent) mainChatToolResult(call llm.ToolCall, userMsg string) (string, bool) {
	switch strings.TrimSpace(call.Name) {
	case "propose_intent":
		out, err := a.mainChatProposeIntent(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "add_intent":
		// Defense in depth: the tool is no longer advertised, but a model
		// that still calls it must not bypass operator confirmation.
		return "error: call propose_intent instead; intents are written only after the operator confirms a proposal", false
	case "confirm_intent":
		out, err := a.mainChatConfirmIntent(call.Args, userMsg)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "add_hint":
		out, err := a.mainChatAddHint(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "finish":
		if pending := a.pendingMainProposalCount(); pending > 0 {
			return fmt.Sprintf("error: %d intent proposal(s) await operator confirmation; confirm or discard them first", pending), false
		}
		counts, err := a.mainChatCounts()
		if err != nil {
			return "error: " + err.Error(), false
		}
		if active := counts.OpenIntents + counts.RunningIntents + counts.PausedIntents; active > 0 {
			return fmt.Sprintf("error: %d intent(s) are still open/running/paused; wait for the worker pool or steer/kill first", active), false
		}
		reason := strings.TrimSpace(call.Args["reason"])
		if reason == "" {
			reason = "user requested finish"
		}
		a.completeWithFinish("Run finished: "+reason+". Reported findings are saved.", a.syncBudgetTokens)
		return "finished", true
	case "list_intents":
		out, err := a.mainChatListIntents(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "list_units":
		out, err := a.mainChatListUnits(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "list_findings":
		out, err := a.mainChatListFindings(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "list_hypotheses":
		out, err := a.mainChatListHypotheses(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "list_hints":
		out, err := a.mainChatListHints(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "steer_work":
		out, err := a.mainChatSteerWork(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "kill_work":
		out, err := a.mainChatKillWork(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "retire_intent":
		out, err := a.mainChatRetireIntent(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	case "reprioritize_intent":
		out, err := a.mainChatReprioritizeIntent(call.Args)
		if err != nil {
			return "error: " + err.Error(), false
		}
		return out, false
	default:
		return "error: unknown tool " + call.Name, false
	}
}

// mainIntentProposal is an operator-confirmable intent draft. It is process
// memory only: it never becomes plan state and never reaches the exploration
// graph until the operator confirms it. CreatedTurn makes same-turn
// self-confirmation impossible.
type mainIntentProposal struct {
	ID          string
	TaskID      string
	Title       string
	Target      string
	TargetKind  string
	VulnClass   string
	Endpoint    string
	Notes       string
	Phase       int
	DependsOn   []string
	CreatedTurn int64
	CreatedAt   time.Time
}

func mainProposalMeta(p *mainIntentProposal, status string) map[string]any {
	meta := map[string]any{
		"proposal_id": p.ID,
		"task_id":     p.TaskID,
		"title":       p.Title,
		"target":      p.Target,
		"target_kind": p.TargetKind,
		"phase":       p.Phase,
		"vuln_class":  p.VulnClass,
		"endpoint":    p.Endpoint,
		"notes":       p.Notes,
		"depends_on":  p.DependsOn,
		"status":      status,
	}
	return meta
}

func (a *Agent) pendingMainProposalCount() int {
	a.mainProposalsMu.Lock()
	defer a.mainProposalsMu.Unlock()
	return len(a.mainProposals)
}

// pendingMainProposalList returns a deterministic (creation order) snapshot so
// the prompt can remind the model which drafts still await confirmation even
// after the chat history has been trimmed.
func (a *Agent) pendingMainProposalList() []*mainIntentProposal {
	a.mainProposalsMu.Lock()
	out := make([]*mainIntentProposal, 0, len(a.mainProposals))
	for _, p := range a.mainProposals {
		out = append(out, p)
	}
	a.mainProposalsMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// parseMainIntentProposal validates one proposal exactly like the old direct
// insert did, so a draft can never become an intent with a missing target or a
// control-plane host used as a web target.
func (a *Agent) parseMainIntentProposal(args map[string]string) (*mainIntentProposal, error) {
	title := strings.TrimSpace(args["title"])
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	target := strings.TrimSpace(args["target"])
	if target == "" {
		return nil, fmt.Errorf("target is required (no implicit scan target)")
	}
	targetKind := strings.ToLower(strings.TrimSpace(args["target_kind"]))
	if targetKind == "" {
		if a.isControlPlaneTarget(target) {
			targetKind = "control_plane_api"
		} else {
			targetKind = "web"
		}
	}
	if targetKind != "control_plane_api" && a.isControlPlaneTarget(target) {
		return nil, fmt.Errorf("control-plane hosts are only allowed with target_kind=control_plane_api")
	}
	phase := intValueStr(args["phase"])
	if phase < 1 {
		phase = 1
	}
	if phase > 22 {
		phase = 22
	}
	return &mainIntentProposal{
		Title:      title,
		Target:     target,
		TargetKind: targetKind,
		VulnClass:  strings.ToLower(strings.TrimSpace(args["vuln_class"])),
		Endpoint:   strings.TrimSpace(args["endpoint"]),
		Notes:      strings.TrimSpace(args["notes"]),
		Phase:      phase,
		DependsOn:  splitCSV(args["depends_on"]),
	}, nil
}

func (a *Agent) mainChatProposeIntent(args map[string]string) (string, error) {
	p, err := a.parseMainIntentProposal(args)
	if err != nil {
		return "", err
	}
	a.mainProposalsMu.Lock()
	if a.mainProposals == nil {
		a.mainProposals = make(map[string]*mainIntentProposal)
	}
	a.mainProposalSeq++
	p.ID = fmt.Sprintf("proposal-%d-%d", time.Now().UnixNano(), a.mainProposalSeq)
	p.TaskID = fmt.Sprintf("human-%d", time.Now().UnixNano())
	p.CreatedAt = time.Now()
	p.CreatedTurn = a.mainTurn
	a.mainProposals[p.ID] = p
	a.mainProposalsMu.Unlock()

	a.emit(Event{Type: "intent_proposed", Content: p.Title, Meta: mainProposalMeta(p, "pending")})
	return fmt.Sprintf(
		"Proposed intent %s (NOT in the plan yet): %s (phase %d, target %s). Awaiting operator confirmation.",
		p.ID, p.Title, p.Phase, p.Target,
	), nil
}

// mainChatConfirmIntent is the text path. The proposal must come from an
// earlier turn and the operator's current message must be an explicit
// confirmation; the transcript button uses the exported method instead.
func (a *Agent) mainChatConfirmIntent(args map[string]string, userMsg string) (string, error) {
	id := strings.TrimSpace(args["proposal_id"])
	if id == "" {
		return "", fmt.Errorf("proposal_id is required")
	}
	a.mainProposalsMu.Lock()
	p := a.mainProposals[id]
	a.mainProposalsMu.Unlock()
	if p == nil {
		return "", fmt.Errorf("no pending proposal %s", id)
	}
	if p.CreatedTurn >= a.mainTurn {
		return "", fmt.Errorf("the proposal was created in this turn; wait for the operator to confirm it in a later message")
	}
	if !mainChatExplicitConfirm(userMsg) {
		return "", fmt.Errorf("the operator did not explicitly confirm; ask them to confirm first")
	}
	return a.confirmMainIntent(p)
}

// ConfirmMainIntentProposal is the deterministic transcript-button path: the
// click itself is the confirmation, so no model turn is involved.
func (a *Agent) ConfirmMainIntentProposal(proposalID string) (taskID, title string, err error) {
	a.mainProposalsMu.Lock()
	p := a.mainProposals[strings.TrimSpace(proposalID)]
	a.mainProposalsMu.Unlock()
	if p == nil {
		return "", "", fmt.Errorf("intent proposal not found or already resolved")
	}
	if _, err := a.confirmMainIntent(p); err != nil {
		return "", "", err
	}
	return p.TaskID, p.Title, nil
}

// RejectMainIntentProposal discards a pending draft without touching the plan.
func (a *Agent) RejectMainIntentProposal(proposalID string) (title string, err error) {
	a.mainProposalsMu.Lock()
	p := a.mainProposals[strings.TrimSpace(proposalID)]
	if p == nil {
		a.mainProposalsMu.Unlock()
		return "", fmt.Errorf("intent proposal not found or already resolved")
	}
	delete(a.mainProposals, p.ID)
	a.mainProposalsMu.Unlock()
	a.emit(Event{Type: "intent_proposal_resolved", Content: p.Title, Meta: mainProposalMeta(p, "rejected")})
	return p.Title, nil
}

// confirmMainIntent removes the draft exactly once and writes the intent.
// A write failure restores the draft so the operator can retry.
func (a *Agent) confirmMainIntent(p *mainIntentProposal) (string, error) {
	a.mainProposalsMu.Lock()
	if _, pending := a.mainProposals[p.ID]; !pending {
		a.mainProposalsMu.Unlock()
		return "", fmt.Errorf("intent proposal already resolved")
	}
	delete(a.mainProposals, p.ID)
	a.mainProposalsMu.Unlock()

	if err := a.insertMainIntent(p); err != nil {
		a.mainProposalsMu.Lock()
		a.mainProposals[p.ID] = p
		a.mainProposalsMu.Unlock()
		return "", err
	}
	a.emit(Event{Type: "intent_proposal_resolved", Content: p.Title, Meta: mainProposalMeta(p, "confirmed")})
	a.emit(Event{Type: "plan_updated", Content: "human intent: " + p.Title, Meta: map[string]any{
		"source": "human", "task_id": p.TaskID, "phase": p.Phase, "proposal_id": p.ID,
	}})
	return fmt.Sprintf("Inserted intent %s (phase %d): %s", p.TaskID, p.Phase, p.Title), nil
}

func (a *Agent) insertMainIntent(p *mainIntentProposal) error {
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" {
		return fmt.Errorf("graph authority unavailable")
	}
	// Re-check the control-plane rule at write time: the draft may outlive a
	// policy change, and a control-plane host must never become a scan target.
	if p.TargetKind != "control_plane_api" && a.isControlPlaneTarget(p.Target) {
		return fmt.Errorf("control-plane hosts are only allowed with target_kind=control_plane_api")
	}
	payload, _ := json.Marshal(map[string]any{
		"task_id":     p.TaskID,
		"title":       p.Title,
		"phase":       p.Phase,
		"vuln_class":  p.VulnClass,
		"endpoint":    p.Endpoint,
		"target":      p.Target,
		"target_kind": p.TargetKind,
		"origin":      "human",
		"depends_on":  p.DependsOn,
		"notes":       p.Notes,
	})
	ctx, cancel := planGraphContext()
	defer cancel()
	node, err := repo.UpsertNode(ctx, graph.NodeInput{
		ScanID: scanID, Kind: graph.KindIntent, UnitKey: p.TaskID,
		State: graph.StateOpen, Priority: 1500 - p.Phase*10, Origin: "human", Payload: string(payload),
	})
	if err != nil {
		return err
	}
	// Resolve dependencies against current plan tasks.
	if len(p.DependsOn) > 0 {
		tasks, _ := repo.PlanTasks(ctx, scanID)
		byID := map[string]int64{}
		for _, t := range tasks {
			byID[t.TaskID] = t.NodeID
		}
		srcs := make([]int64, 0, len(p.DependsOn))
		for _, dep := range p.DependsOn {
			if id, ok := byID[dep]; ok {
				srcs = append(srcs, id)
			}
		}
		_ = repo.ReplaceDependencyEdges(ctx, scanID, node.ID, srcs)
	}
	return nil
}

// mainChatExplicitConfirm accepts only a short, explicit operator affirmation.
// The guidance marker prefix is stripped first; it is transport metadata, not
// the operator's text.
func mainChatExplicitConfirm(msg string) bool {
	text := strings.TrimSpace(msg)
	if idx := strings.Index(text, "]: "); idx >= 0 && strings.HasPrefix(text, "[OPERATOR GUIDANCE ") {
		text = strings.TrimSpace(text[idx+3:])
	}
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" || len([]rune(text)) > 120 {
		return false
	}
	for _, prefix := range []string{
		"确认", "确定", "执行", "开始", "派发",
		"confirm", "confirmed", "go ahead", "do it",
	} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return text == "yes" || text == "ok"
}

func (a *Agent) mainChatAddHint(args map[string]string) (string, error) {
	msg := strings.TrimSpace(args["message"])
	if msg == "" {
		return "", fmt.Errorf("message is required")
	}
	hintID := fmt.Sprintf("hint-%d", time.Now().UnixNano())
	if err := a.recordHumanHint(hintID, msg, nil); err != nil {
		return "", err
	}
	a.emit(Event{Type: "plan_updated", Content: "human hint recorded", Meta: map[string]any{"source": "human", "hint_id": hintID}})
	return "Hint recorded: " + msg, nil
}

// recordHumanHint persists one operator utterance as a hint node. Per CORE §2
// human input is observed information owned by the operator, so a hint is
// written with origin=human and a deterministic unit key: re-capturing the
// same text (for example a trim retry) is idempotent and never duplicates it.
func (a *Agent) recordHumanHint(hintID, msg string, extra map[string]any) error {
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" {
		return fmt.Errorf("graph authority unavailable")
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return fmt.Errorf("message is required")
	}
	payload := map[string]any{
		"guidance_id": hintID,
		"message":     msg,
		"source":      "human",
	}
	for k, v := range extra {
		payload[k] = v
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := planGraphContext()
	defer cancel()
	if _, err := repo.UpsertNode(ctx, graph.NodeInput{
		ScanID: scanID, Kind: graph.KindHint, UnitKey: hintID,
		State: graph.StateActive, Priority: 50, Origin: "human", Payload: string(body),
	}); err != nil {
		return err
	}
	return nil
}

// mainChatSteerWork routes a human correction to the worker executing one
// unit. It is a decision action: it changes what the worker does next, never
// what it is allowed to do (lane/scope guards still apply).
func (a *Agent) mainChatSteerWork(args map[string]string) (string, error) {
	unitID := strings.TrimSpace(args["unit_id"])
	if unitID == "" {
		return "", fmt.Errorf("unit_id is required")
	}
	msg := strings.TrimSpace(args["message"])
	if msg == "" {
		return "", fmt.Errorf("message is required")
	}
	if err := a.SteerWorker(unitID, msg); err != nil {
		return "", err
	}
	a.emit(Event{Type: "plan_updated", Content: "human steer: " + unitID, Meta: map[string]any{
		"actor": "human", "action": "steer_work", "unit_id": unitID,
	}})
	return fmt.Sprintf("Steered worker for %s (actor=human).", unitID), nil
}

// mainChatKillWork cancels one worker after engine validation of the typed
// reason code (a stagnant kill is refused while the progress ledger shows a
// recent information-layer increment).
func (a *Agent) mainChatKillWork(args map[string]string) (string, error) {
	unitID := strings.TrimSpace(args["unit_id"])
	if unitID == "" {
		return "", fmt.Errorf("unit_id is required")
	}
	code, err := normalizeKillReasonCode(args["reason_code"])
	if err != nil {
		return "", err
	}
	if err := a.KillWorkerWithReason(unitID, code, args["reason"]); err != nil {
		return "", err
	}
	a.emit(Event{Type: "plan_updated", Content: "human kill: " + unitID, Meta: map[string]any{
		"actor": "human", "action": "kill_work", "unit_id": unitID, "reason_code": code,
	}})
	return fmt.Sprintf("Cancelled the worker for %s (reason_code=%s, actor=human).", unitID, code), nil
}

// mainChatRetireIntent deletes an OPEN intent from the authoritative plan.
// Only open → deleted is allowed: running/paused work must be killed or
// settled by the pool, and done/blocked intents are already terminal. The
// transition is a CAS, so a concurrent planner/worker change returns a
// conflict instead of silently overwriting newer state.
func (a *Agent) mainChatRetireIntent(args map[string]string) (string, error) {
	unitKey := strings.TrimSpace(args["task_id"])
	if unitKey == "" {
		unitKey = strings.TrimSpace(args["unit_id"])
	}
	if unitKey == "" {
		return "", fmt.Errorf("task_id (or unit_id) is required")
	}
	reason := strings.TrimSpace(args["reason"])
	if reason == "" {
		return "", fmt.Errorf("reason is required")
	}
	repo, scanID, err := a.mainChatGraph()
	if err != nil {
		return "", err
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	tasks, err := repo.PlanTasks(ctx, scanID)
	if err != nil {
		return "", err
	}
	var target *graph.PlanTask
	for i := range tasks {
		if strings.TrimSpace(tasks[i].TaskID) == unitKey {
			target = &tasks[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("no intent %q in the plan", unitKey)
	}
	state := strings.ToLower(strings.TrimSpace(target.GraphState))
	if state == "" {
		state = strings.ToLower(strings.TrimSpace(target.Status))
	}
	if state != string(graph.StateOpen) && state != "pending" {
		return "", fmt.Errorf("intent %s is %s; only open intents can be retired (kill or settle running work first)", unitKey, state)
	}
	ok, err := repo.Transition(ctx, scanID, target.NodeID, graph.KindIntent, graph.StateOpen, graph.StateDeleted)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("intent %s changed state before the retire could be applied; re-read the plan and retry", unitKey)
	}
	a.emit(Event{Type: "plan_updated", Content: "human retire: " + unitKey, Meta: map[string]any{
		"actor": "human", "action": "retire_intent", "unit_id": unitKey, "reason": reason,
	}})
	return fmt.Sprintf("Retired intent %s (state=deleted). actor=human reason=%s", unitKey, reason), nil
}

// mainChatReprioritizeIntent changes one intent's claim priority. It is a
// decision, not a state change: the lifecycle (open/running/...) is untouched.
func (a *Agent) mainChatReprioritizeIntent(args map[string]string) (string, error) {
	unitID := strings.TrimSpace(args["unit_id"])
	if unitID == "" {
		return "", fmt.Errorf("unit_id is required")
	}
	raw := strings.TrimSpace(args["priority"])
	if raw == "" {
		return "", fmt.Errorf("priority is required")
	}
	priority, err := strconv.Atoi(raw)
	if err != nil {
		return "", fmt.Errorf("priority must be an integer: %w", err)
	}
	if err := a.ReprioritizeIntent(unitID, priority, args["reason"]); err != nil {
		return "", err
	}
	return fmt.Sprintf("Reprioritized intent %s to %d (actor=human).", unitID, priority), nil
}

const (
	mainChatReadDefault = 20
	mainChatReadMax     = 50
	// mainChatReadChars bounds one read-only answer: a query must never flood
	// the chat history or the model context shared with the operator turns.
	mainChatReadChars = 6000
)

// mainChatReadLimit clamps the model-supplied limit. Zero/negative means the
// default; anything above the ceiling is clamped instead of rejected so a
// slightly greedy call still returns useful data.
func mainChatReadLimit(raw string) int {
	n := intValueStr(raw)
	if n <= 0 {
		return mainChatReadDefault
	}
	if n > mainChatReadMax {
		return mainChatReadMax
	}
	return n
}

// mainChatReadResult joins bounded lines and appends an honest overflow note
// instead of silently dropping rows.
func mainChatReadResult(header string, lines []string, matched int) string {
	var b strings.Builder
	b.WriteString(header)
	for _, line := range lines {
		if b.Len()+len(line)+1 > mainChatReadChars {
			fmt.Fprintf(&b, "\n… output truncated; %d more row(s) match. Narrow the filter or raise limit (max %d).", matched-len(lines), mainChatReadMax)
			return b.String()
		}
		b.WriteString("\n")
		b.WriteString(line)
	}
	if matched > len(lines) {
		fmt.Fprintf(&b, "\n… %d more row(s) match; narrow the filter or raise limit (max %d).", matched-len(lines), mainChatReadMax)
	}
	return b.String()
}

func (a *Agent) mainChatGraph() (graph.Repository, string, error) {
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" {
		return nil, "", fmt.Errorf("graph authority unavailable")
	}
	return repo, scanID, nil
}

// mainChatListIntents is a read-only projection of the authoritative plan
// graph. It never mutates or emits.
func (a *Agent) mainChatListIntents(args map[string]string) (string, error) {
	repo, scanID, err := a.mainChatGraph()
	if err != nil {
		return "", err
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	tasks, err := repo.PlanTasks(ctx, scanID)
	if err != nil {
		return "", err
	}
	// The plan view does not carry the dispatch target (it lives in the intent
	// payload). Read it so the main agent can answer "what are we testing"
	// instead of only seeing endpoints.
	targetByNode := map[int64]string{}
	if nodes, nerr := repo.ListNodes(ctx, scanID, graph.KindIntent); nerr == nil {
		for _, node := range nodes {
			payload := node.PayloadJSON()
			raw, _ := payload["target"].(string)
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if kind, _ := payload["target_kind"].(string); strings.TrimSpace(kind) != "" {
				raw += " (" + strings.TrimSpace(kind) + ")"
			}
			targetByNode[node.ID] = raw
		}
	}
	status := strings.ToLower(strings.TrimSpace(args["status"]))
	phase := intValueStr(args["phase"])
	limit := mainChatReadLimit(args["limit"])
	lines := make([]string, 0, limit)
	matched := 0
	for _, t := range tasks {
		state := strings.ToLower(strings.TrimSpace(t.GraphState))
		if state == "" {
			state = strings.ToLower(strings.TrimSpace(t.Status))
		}
		if status != "" && status != "all" && state != status && strings.ToLower(t.Status) != status {
			continue
		}
		if phase > 0 && t.Phase != phase {
			continue
		}
		matched++
		if len(lines) < limit {
			lines = append(lines, mainChatIntentLine(t, state, targetByNode[t.NodeID]))
		}
	}
	if matched == 0 {
		return "No intents match. The plan graph may still be empty.", nil
	}
	return mainChatReadResult(fmt.Sprintf("Intents for %s (match=%d):", scanID, matched), lines, matched), nil
}

func mainChatIntentLine(t graph.PlanTask, state, target string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s | P%d | %s | %s", t.TaskID, t.Phase, state, t.Title)
	if target != "" {
		fmt.Fprintf(&b, " | target=%s", target)
	}
	if t.VulnClass != "" {
		fmt.Fprintf(&b, " | %s", t.VulnClass)
	}
	if t.Endpoint != "" {
		fmt.Fprintf(&b, " | %s", t.Endpoint)
	}
	if t.Owner != "" {
		fmt.Fprintf(&b, " | owner=%s", t.Owner)
	}
	if len(t.DependsOn) > 0 {
		fmt.Fprintf(&b, " | deps=%s", strings.Join(t.DependsOn, ","))
	}
	if t.Disposition != "" {
		fmt.Fprintf(&b, " | disposition=%s", t.Disposition)
	}
	return truncStr(b.String(), 240)
}

// mainChatListUnits exposes the unified intent+belief unit view: what the pool
// is holding right now and which hypothesis nodes are being tested. Read-only.
func (a *Agent) mainChatListUnits(args map[string]string) (string, error) {
	repo, scanID, err := a.mainChatGraph()
	if err != nil {
		return "", err
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	units, err := repo.Units(ctx, scanID)
	if err != nil {
		return "", err
	}
	kind := strings.ToLower(strings.TrimSpace(args["kind"]))
	state := strings.ToLower(strings.TrimSpace(args["state"]))
	limit := mainChatReadLimit(args["limit"])
	lines := make([]string, 0, limit)
	matched := 0
	for _, u := range units {
		if kind != "" && kind != "all" && strings.ToLower(u.Kind) != kind {
			continue
		}
		if state != "" && state != "all" && strings.ToLower(u.State) != state {
			continue
		}
		matched++
		if len(lines) < limit {
			lines = append(lines, mainChatUnitLine(u))
		}
	}
	if matched == 0 {
		return "No units match. The exploration graph has no intent/belief nodes for this filter.", nil
	}
	return mainChatReadResult(fmt.Sprintf("Units for %s (match=%d):", scanID, matched), lines, matched), nil
}

func mainChatUnitLine(u graph.Unit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s %s | state=%s", u.Kind, u.UnitID, u.State)
	if u.Owner != "" {
		fmt.Fprintf(&b, " | owner=%s", u.Owner)
	}
	if u.Phase > 0 {
		fmt.Fprintf(&b, " | P%d", u.Phase)
	}
	if u.VulnClass != "" {
		fmt.Fprintf(&b, " | %s", u.VulnClass)
	}
	if u.Subject != "" {
		fmt.Fprintf(&b, " | %s", u.Subject)
	}
	if u.Expired {
		b.WriteString(" | lease-expired")
	}
	return truncStr(b.String(), 240)
}

// mainChatListFindings reads the run's confirmed findings projection. It only
// reads; it never touches the report gate.
func (a *Agent) mainChatListFindings(args map[string]string) (string, error) {
	if a.scanCtx == nil || strings.TrimSpace(a.scanCtx.ID) == "" {
		return "", fmt.Errorf("scan context unavailable")
	}
	severity := strings.ToLower(strings.TrimSpace(args["severity"]))
	limit := mainChatReadLimit(args["limit"])
	vulns := reporting.GetVulnerabilitiesForContext(a.scanCtx.ID)
	lines := make([]string, 0, limit)
	matched := 0
	for _, v := range vulns {
		if severity != "" && severity != "all" && !strings.EqualFold(v.Severity, severity) {
			continue
		}
		matched++
		if len(lines) < limit {
			lines = append(lines, mainChatFindingLine(v))
		}
	}
	if matched == 0 {
		return "No findings recorded for this run yet.", nil
	}
	return mainChatReadResult(fmt.Sprintf("Findings (match=%d):", matched), lines, matched), nil
}

func mainChatFindingLine(v reporting.Vulnerability) string {
	var b strings.Builder
	severity := strings.ToUpper(strings.TrimSpace(v.Severity))
	if severity == "" {
		severity = "?"
	}
	fmt.Fprintf(&b, "- [%s] %s", severity, v.Title)
	if v.Endpoint != "" {
		fmt.Fprintf(&b, " @ %s", v.Endpoint)
	}
	if v.CVSS > 0 {
		fmt.Fprintf(&b, " (CVSS %.1f)", v.CVSS)
	}
	if v.ID != "" {
		fmt.Fprintf(&b, " | id=%s", v.ID)
	}
	if tag := mainChatVerificationTag(v); tag != "" {
		fmt.Fprintf(&b, " | %s", tag)
	}
	return truncStr(b.String(), 240)
}

func mainChatVerificationTag(v reporting.Vulnerability) string {
	if v.Verified {
		return reporting.TagVerified
	}
	if len(v.Tags) > 0 {
		return v.Tags[0]
	}
	return ""
}

// mainChatListHypotheses reads the shared ledger: what is queued, being
// tested, proven or rejected, plus evidence counts. Read-only.
func (a *Agent) mainChatListHypotheses(args map[string]string) (string, error) {
	ledger := a.ledger()
	if ledger == nil {
		return "", fmt.Errorf("ledger unavailable")
	}
	status := strings.ToLower(strings.TrimSpace(args["status"]))
	limit := mainChatReadLimit(args["limit"])
	lines := make([]string, 0, limit)
	matched := 0
	for _, h := range ledger.All() {
		if status != "" && status != "all" && strings.ToLower(string(h.Status)) != status {
			continue
		}
		matched++
		if len(lines) < limit {
			lines = append(lines, mainChatHypothesisLine(h))
		}
	}
	if matched == 0 {
		return "No hypotheses match. The ledger is empty for this run.", nil
	}
	return mainChatReadResult(
		fmt.Sprintf("Hypotheses (match=%d, evidence_items=%d):", matched, ledger.EvidenceCount()),
		lines, matched,
	), nil
}

// mainChatListHints lists operator guidance recorded as hint nodes. Hints are
// observed human input (CORE §2) and stay active for the whole run; the
// consumed state is reserved for a future lifecycle pass, so this is read-only.
func (a *Agent) mainChatListHints(args map[string]string) (string, error) {
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" {
		return "", fmt.Errorf("graph authority unavailable")
	}
	state := strings.ToLower(strings.TrimSpace(args["state"]))
	limit := mainChatReadLimit(args["limit"])
	var states []graph.State
	switch state {
	case "", "all":
	case "active":
		states = append(states, graph.StateActive)
	case "consumed":
		states = append(states, graph.StateConsumed)
	default:
		return "", fmt.Errorf("state must be all, active or consumed")
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	nodes, err := repo.ListNodes(ctx, scanID, graph.KindHint, states...)
	if err != nil {
		return "", err
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if !nodes[i].CreatedAt.Equal(nodes[j].CreatedAt) {
			return nodes[i].CreatedAt.Before(nodes[j].CreatedAt)
		}
		return nodes[i].ID < nodes[j].ID
	})
	lines := make([]string, 0, limit)
	for _, n := range nodes {
		if len(lines) >= limit {
			break
		}
		msg := strings.TrimSpace(stringField(n.PayloadJSON(), "message"))
		if msg == "" {
			continue
		}
		key := strings.TrimSpace(n.UnitKey)
		if key == "" {
			key = fmt.Sprintf("node-%d", n.ID)
		}
		lines = append(lines, fmt.Sprintf("- %s | %s | %s", key, n.State, clipSnapshotText(msg, 240)))
	}
	if len(lines) == 0 {
		return "No operator hints recorded for this run.", nil
	}
	return mainChatReadResult(fmt.Sprintf("Operator hints (match=%d):", len(nodes)), lines, len(nodes)), nil
}

func mainChatHypothesisLine(h scanctx.Hypothesis) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s | %s | %s", h.ID, h.Status, h.VulnClass)
	if h.Endpoint != "" {
		fmt.Fprintf(&b, " | %s", h.Endpoint)
		if h.Parameter != "" {
			fmt.Fprintf(&b, " param=%s", h.Parameter)
		}
	}
	fmt.Fprintf(&b, " | conf=%.2f | evidence=%d", h.Confidence, len(h.Evidence))
	if h.AssignedTo != "" {
		fmt.Fprintf(&b, " | owner=%s", h.AssignedTo)
	}
	if h.NextAction != "" {
		fmt.Fprintf(&b, " | next=%s", h.NextAction)
	}
	return truncStr(b.String(), 240)
}

func mainChatMessageToolCalls(calls []llm.ToolCall) []llm.MessageToolCall {
	out := make([]llm.MessageToolCall, 0, len(calls))
	for _, c := range calls {
		args := c.RawArguments
		if strings.TrimSpace(args) == "" {
			b, _ := json.Marshal(c.Args)
			args = string(b)
		}
		out = append(out, llm.MessageToolCall{
			ID:   c.ID,
			Type: "function",
			Function: llm.MessageToolFunction{
				Name:      c.Name,
				Arguments: args,
			},
		})
	}
	return out
}

// mainChatUserMarker is the transport wrapper mainChatUserMessage puts in front
// of the operator's actual text. Only text after the first marker is the
// operator's input; [GRAPH]/[PROPOSALS] lines are runtime annotations.
const mainChatUserMarker = "[USER] "

// mainChatOperatorText extracts the operator's raw utterance from a main-chat
// user message. It returns false for runtime-only turns ([SYSTEM]),
// compaction notes ([CONTEXT]), and the replay transcript, which are history
// or automation rather than human input.
func mainChatOperatorText(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "[SYSTEM] ") || strings.HasPrefix(trimmed, "[CONTEXT] ") {
		return "", false
	}
	i := strings.Index(content, mainChatUserMarker)
	if i < 0 {
		return "", false
	}
	text := strings.TrimSpace(content[i+len(mainChatUserMarker):])
	if text == "" {
		return "", false
	}
	if strings.HasPrefix(text, "[SYSTEM] ") {
		return "", false
	}
	return text, true
}

// trimMainChatHistory bounds the operator chat window WITHOUT losing human
// input. Before an operator message leaves the window it is projected to a
// hint node (origin=human, idempotent unit key); per CORE §2 that is where
// human input lives, so the chat transcript may age out while the information
// does not.
//
// A failed graph write does not silently drop the message: the trim is deferred
// to the next turn (writes are idempotent, so a retry is safe). Only when the
// buffer would otherwise exceed max+8 is the trim forced, and then the loss is
// reported as an explicit error event rather than hidden.
func (a *Agent) trimMainChatHistory(history []llm.Message, max int) []llm.Message {
	if len(history) <= max {
		return history
	}
	keep := max - 1
	if keep < 1 {
		keep = 1
	}
	dropEnd := len(history) - keep
	if dropEnd <= 1 {
		return history
	}
	dropped := history[1:dropEnd]
	persisted, operatorMsgs := a.persistDroppedOperatorMessages(dropped)
	if !persisted {
		if len(history) <= max+8 {
			log.Printf("[agent] main chat trim deferred: %d operator message(s) in the dropped window could not be recorded as hints; retrying next turn", operatorMsgs)
			return history
		}
		a.emit(Event{Type: "error", Content: fmt.Sprintf(
			"Main chat context bound exceeded: %d earlier operator message(s) could not be recorded as hints and were dropped from the chat window.", operatorMsgs)})
	}
	out := make([]llm.Message, 0, keep+1)
	out = append(out, history[0])
	out = append(out, history[dropEnd:]...)
	return out
}

// persistDroppedOperatorMessages writes every operator utterance in dropped as
// an idempotent hint node. It returns (true, n) when every write succeeded and
// (false, n) when at least one failed; n is the number of operator messages, so
// the caller can report how much human input is at stake.
func (a *Agent) persistDroppedOperatorMessages(dropped []llm.Message) (bool, int) {
	ok := true
	seen := make(map[string]bool)
	operatorMsgs := 0
	for _, msg := range dropped {
		if msg.Role != "user" {
			continue
		}
		text, found := mainChatOperatorText(msg.Content)
		if !found {
			continue
		}
		operatorMsgs++
		// An explicit confirmation is a decision already applied by the tool;
		// replaying it as guidance would be wrong.
		if mainChatExplicitConfirm(text) {
			continue
		}
		sum := sha256.Sum256([]byte(text))
		hintID := fmt.Sprintf("hint-chat-%x", sum[:8])
		if seen[hintID] {
			continue
		}
		seen[hintID] = true
		if err := a.recordHumanHint(hintID, text, map[string]any{"captured_from": "main_chat_trim"}); err != nil {
			log.Printf("[agent] main chat trim: failed to record operator hint %s: %v", hintID, err)
			ok = false
		}
	}
	return ok, operatorMsgs
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intValueStr(raw string) int {
	n := 0
	for _, r := range strings.TrimSpace(raw) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func (a *Agent) isControlPlaneTarget(raw string) bool {
	host := normalizeMainChatHost(raw)
	if host == "" {
		return false
	}
	for _, cpRaw := range a.controlPlaneHosts {
		cp := normalizeMainChatHost(cpRaw)
		if cp != "" && (host == cp || strings.HasSuffix(host, "."+cp)) {
			return true
		}
	}
	return false
}

func normalizeMainChatHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "//" + raw
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return strings.TrimSuffix(u.Hostname(), ".")
	}
	return strings.TrimSuffix(raw, ".")
}

func truncateMainChatText(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 {
		return ""
	}
	// Rune-safe: the task instruction may be Chinese, and a byte slice would
	// split a multi-byte character into invalid UTF-8 in the prompt.
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
