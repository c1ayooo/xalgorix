package agent

// Eino observer bridge. It feeds the legacy hook registry and budget/liveness
// obligations into the ADK runtime without reimplementing those rules in
// internal/engine. The engine stays responsible for scheduling and checkpointing;
// this adapter remains the single place that knows the Agent's per-scan state.

import (
	"context"
	"fmt"
	"strings"

	"github.com/xalgord/xalgorix/v4/internal/engine"
	"github.com/xalgord/xalgorix/v4/internal/llm"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

type einoObserver struct {
	agent     *Agent
	toolCalls int
}

// EngineObserver exposes the legacy obligation surface to an Eino Runtime.
// Callers must use it only after the Agent has been constructed for one scan.
func (a *Agent) EngineObserver() engine.TurnObserver {
	if a == nil {
		return nil
	}
	return &einoObserver{agent: a}
}

func (o *einoObserver) BeforeTurn(_ context.Context, turn int) engine.TurnDecision {
	a := o.agent
	if a.stopped.Load() || (a.ctx != nil && a.ctx.Err() != nil) {
		return engine.TurnDecision{StopReason: "stopped"}
	}
	a.touchActivity()
	a.state.Iteration = turn
	if over, why := a.overBudget(o.toolCalls); over {
		return engine.TurnDecision{StopReason: "resource_budget: " + why}
	}
	if a.scanBudget != nil && !a.scanBudget.reserveIteration(a.maxIter) {
		return engine.TurnDecision{StopReason: "shared iteration budget exhausted"}
	}
	if a.stopForHook(a.semanticLivenessCheck()) {
		return engine.TurnDecision{StopReason: "liveness"}
	}
	if a.autoFinalizeIfPlanComplete() {
		return engine.TurnDecision{StopReason: "plan_complete"}
	}

	result := a.hooks.Fire(OnIterationStart, a.state, nil)
	if autoDelegation := a.maybeAutoDelegate(a.targets); autoDelegation != "" {
		if result.Nudge == "" || strings.Contains(result.Nudge, "MULTI-AGENT DECOMPOSITION") {
			result.Nudge = autoDelegation
		} else {
			result.Nudge = autoDelegation + "\n\n" + result.Nudge
		}
	}
	if result.EmitMessage != "" {
		a.emit(Event{Type: "message", Content: result.EmitMessage, TotalTokens: a.syncBudgetTokens()})
	}
	return engine.TurnDecision{Nudge: result.Nudge, EmitMessage: result.EmitMessage}
}

func (o *einoObserver) BeforeTool(_ context.Context, name string, args map[string]string) engine.GuardVerdict {
	a := o.agent
	merged := make(map[string]string, len(args)+1)
	merged["tool_name"] = name
	for k, v := range args {
		merged[k] = v
	}
	if blocked, reason := a.shouldBlockForOutOfScope(name, merged); blocked {
		msg := "⛔ OUT-OF-SCOPE TARGET BLOCKED — " + reason
		a.emit(Event{Type: "tool_call", ToolName: name, ToolArgs: merged})
		a.emit(Event{Type: "tool_result", ToolName: name, ToolResult: tools.Result{Output: msg}})
		return engine.GuardVerdict{Allowed: false, Output: msg}
	}
	if name == "finish" {
		finish := a.hooks.Fire(OnFinishAttempt, a.state, nil)
		if finish.Block {
			a.state.FinishRecoveryPending = true
			msg := fmt.Sprintf("⚠️ FINISH REJECTED — %s\n\nDO NOT call finish again until you have done more testing.\nContinue with the NEXT PHASE of testing NOW.", finish.BlockReason)
			a.emit(Event{Type: "tool_result", ToolName: "finish", ToolResult: tools.Result{Output: msg}})
			return engine.GuardVerdict{Allowed: false, Output: msg}
		}
	}
	result := a.hooks.Fire(OnToolCall, a.state, merged)
	if result.Nudge != "" {
		a.msgMu.Lock()
		a.messages = append(a.messages, llmMessageForNudge(result.Nudge))
		a.msgMu.Unlock()
	}
	if result.EmitMessage != "" {
		a.emit(Event{Type: "message", Content: result.EmitMessage})
	}
	if result.Block {
		return engine.GuardVerdict{Allowed: false, Output: result.BlockReason}
	}
	if result.ForceSkip {
		return engine.GuardVerdict{Allowed: false, Output: "⏭️ Tool call skipped by the stuck-loop guard: " + result.Nudge}
	}
	a.hooks.Fire(OnToolExecute, a.state, merged)
	a.state.ConsecutiveBlockedCalls = 0
	a.emit(Event{Type: "tool_call", ToolName: name, ToolArgs: merged})
	return engine.GuardVerdict{Allowed: true}
}

func (o *einoObserver) AfterTool(_ context.Context, name string, args map[string]string, output string, callErr error) {
	a := o.agent
	o.toolCalls++
	merged := make(map[string]string, len(args)+3)
	merged["tool_name"] = name
	for k, v := range args {
		merged[k] = v
	}
	merged["output"] = output
	if callErr != nil {
		merged["error"] = callErr.Error()
	}
	result := a.hooks.Fire(OnToolResult, a.state, merged)
	if a.stopForHook(result) {
		return
	}
	a.emit(Event{Type: "tool_result", ToolName: name, ToolResult: tools.Result{
		Output: output, Error: merged["error"], Success: merged["error"] == "",
	}})
	if name == "finish" && callErr == nil {
		a.completeWithFinish(output, func() int { return a.syncBudgetTokens() })
	}
}

// llmMessageForNudge keeps the adapter isolated from the agent's message type
// construction helpers.
func llmMessageForNudge(content string) llm.Message {
	return llm.Message{Role: "user", Content: content}
}
