package agent

// Eino observer bridge. It feeds the legacy hook registry and budget/liveness
// obligations into the ADK runtime without reimplementing those rules in
// internal/engine. The engine stays responsible for scheduling and checkpointing;
// this adapter remains the single place that knows the Agent's per-scan state.
// BeforeTool replays the legacy dispatch loop's block-gate sequence
// (activity policy → phase restriction → auth prerequisites → out-of-scope →
// target scope) so both engines reject identical calls with identical
// tool-result text; the ADK middleware turns a denial into the tool result
// message, replacing legacy's appendToolOutcome plumbing.

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
	// Block gates run in the exact legacy dispatch-loop order (agent.go):
	// activity policy → phase restriction → auth prerequisites → out-of-scope
	// → target scope. Every blocked path mirrors the legacy tool-result shape
	// (same prefix, same escalating noteBlockedToolCall nudge) so a denial
	// reads identically on both engines.
	block := func(prefix, reason string) engine.GuardVerdict {
		msg := prefix + reason + noteBlockedToolCall(a.state, name, merged)
		a.emit(Event{Type: "tool_call", ToolName: name, ToolArgs: merged})
		a.emit(Event{Type: "tool_result", ToolName: name, ToolResult: tools.Result{Output: msg}, TotalTokens: a.syncBudgetTokens()})
		return engine.GuardVerdict{Allowed: false, Output: msg}
	}
	if blocked, reason := a.shouldBlockForActivityPolicy(name, merged); blocked {
		return block("⛔ ACTIVITY POLICY BLOCKED TOOL — ", reason)
	}
	if blocked, reason := a.shouldBlockForPhaseRestriction(name, merged); blocked {
		return block("⛔ PHASE RESTRICTION BLOCKED TOOL — ", reason)
	}
	if blocked, reason := a.shouldBlockForMissingAuthPrerequisites(name, merged); blocked {
		return block("⛔ AUTH PREREQUISITE GUARD BLOCKED TOOL — ", reason)
	}
	if blocked, reason := a.shouldBlockForOutOfScope(name, merged); blocked {
		return block("⛔ OUT-OF-SCOPE TARGET BLOCKED — ", reason)
	}
	if blocked, reason := a.shouldBlockForTargetScope(name, merged); blocked {
		return block("⛔ OUTSIDE SCAN BOUNDARY — ", reason)
	}
	if name == "finish" {
		finish := a.hooks.Fire(OnFinishAttempt, a.state, nil)
		if finish.Block {
			a.state.FinishRecoveryPending = true
			msg := fmt.Sprintf("⚠️ FINISH REJECTED — %s\n\nDO NOT call finish again until you have done more testing.\nContinue with the NEXT PHASE of testing NOW.", finish.BlockReason)
			a.emit(Event{Type: "tool_result", ToolName: "finish", ToolResult: tools.Result{Output: msg}})
			// Legacy's rejected-finish path switches to the deterministic
			// validator temperature for the coverage re-verification turn.
			a.client.SetTemperature(TempValidator)
			return engine.GuardVerdict{Allowed: false, Output: msg}
		}
		// Mark the accepted finish BEFORE the tool runs: completeWithFinish
		// fires from AfterTool, which the ADK races against the next model
		// turn, so the runtime's StopCheck must see completion intent here to
		// deterministically refuse that extra model call.
		a.einoFinishAccepted.Store(true)
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
	if name == "finish" && callErr != nil {
		// The gate accepted this finish but the tool execution itself failed:
		// disarm the stop probe so the next model turn — a finish retry — is
		// allowed again. completeWithFinish never ran, so the scan is not
		// complete; AfterTool runs before the ADK schedules the next turn.
		a.einoFinishAccepted.Store(false)
	}
}

// llmMessageForNudge keeps the adapter isolated from the agent's message type
// construction helpers.
func llmMessageForNudge(content string) llm.Message {
	return llm.Message{Role: "user", Content: content}
}
