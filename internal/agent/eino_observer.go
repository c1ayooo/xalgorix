package agent

// Eino runtime adapter. This is the single place that knows both the ADK
// event/guard boundaries and the scan's obligations: phase/activity policy,
// budget, hooks, finish gates, provider waits and context compaction. The
// engine package stays free of Agent state; the agent never schedules its own
// model turns.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/xalgord/xalgorix/v4/internal/engine"
	"github.com/xalgord/xalgorix/v4/internal/llm"
	"github.com/xalgord/xalgorix/v4/internal/tools"
	"github.com/xalgord/xalgorix/v4/internal/tools/browser"
	"github.com/xalgord/xalgorix/v4/internal/tools/notes"
	"github.com/xalgord/xalgorix/v4/internal/tools/terminal"
)

type einoObserver struct {
	agent     *Agent
	toolCalls int
}

// EngineObserver exposes the scan obligation surface to the Eino runtime.
func (a *Agent) EngineObserver() engine.TurnObserver {
	if a == nil {
		return nil
	}
	return &einoObserver{agent: a}
}

// ModelRecovery implements the provider retry, empty/no-tool recovery and
// token-accounting policy for the Eino ChatModel adapter.
func (a *Agent) ModelRecovery() engine.ModelRecovery {
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
	// R-goal complement (goal_gate.go): the objective fully met closes the
	// run actively, ahead of the plan-complete tail that would otherwise
	// defer completion for more work.
	if a.goalAchievedAutoStop() {
		return engine.TurnDecision{StopReason: "goal_achieved"}
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
	normalized := normalizeEinoToolArgs(a, name, args)
	merged := make(map[string]string, len(normalized)+1)
	merged["tool_name"] = name
	for k, v := range normalized {
		merged[k] = v
	}
	// Block gates run in the canonical dispatch order:
	// activity policy → phase restriction → auth prerequisites → out-of-scope
	// → target scope. Every blocked path returns a normal tool result.
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
			a.setNextTemperature(TempValidator)
			return engine.GuardVerdict{Allowed: false, Output: msg}
		}
		// Mark the accepted finish BEFORE the tool runs: completeWithFinish fires
		// from AfterTool, which races the ADK next model turn, so StopCheck must
		// see completion intent here to deterministically refuse that call.
		a.finishAccepted.Store(true)
	}
	result := a.hooks.Fire(OnToolCall, a.state, merged)
	if result.Nudge != "" {
		a.appendUserNudge(result.Nudge)
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

	stuck := a.hooks.Fire(OnStuckCheck, a.state, merged)
	if a.stopForHook(stuck) {
		return engine.GuardVerdict{Allowed: false, Output: "⏹️ " + stuck.StopReason}
	}
	if stuck.Nudge != "" {
		a.appendUserNudge(stuck.Nudge)
	}
	if stuck.CleanupBrowser && a.scanCtx != nil {
		browser.CleanupContext(a.scanCtx.ID)
	}
	if stuck.ForceSkip {
		return engine.GuardVerdict{Allowed: false, Output: "⏭️ Tool call skipped by the stuck-loop guard: " + stuck.Nudge}
	}

	if a.scanBudget != nil {
		maxToolCalls := 0
		if a.cfg != nil {
			maxToolCalls = a.cfg.MaxToolCalls
		}
		if allowed := a.scanBudget.reserveToolCalls(1, maxToolCalls); allowed == 0 {
			msg := fmt.Sprintf("⏱️ Shared tool-call budget exhausted (scan cap %d). No further tool call was executed.", maxToolCalls)
			a.emit(Event{Type: "tool_result", ToolName: name, ToolResult: tools.Result{Output: msg}})
			return engine.GuardVerdict{Allowed: false, Output: msg}
		}
	}
	o.toolCalls++
	a.hooks.Fire(OnToolExecute, a.state, merged)
	a.state.ConsecutiveBlockedCalls = 0
	a.emit(Event{Type: "tool_call", ToolName: name, ToolArgs: merged})
	return engine.GuardVerdict{Allowed: true, Args: normalized}
}

func (o *einoObserver) TransformToolOutput(_ context.Context, name string, _ map[string]string, output string, metadata map[string]any, callErr error) string {
	a := o.agent
	if callErr != nil {
		return output
	}
	result := tools.Result{Output: output, Metadata: metadata}
	formatted := formatToolResult(name, result)
	if a.boundedContextEnabled() && a.scanCtx != nil && len(output) >= a.toolArchiveMinBytes() {
		if id := a.scanCtx.ToolOutputs.Archive(name, output, a.toolArchiveMinBytes()); id != "" {
			formatted = strings.TrimRight(formatted, "\n") + "\n" + archiveToolResultMarker(id)
		}
	}
	return formatted
}

func (o *einoObserver) AfterTool(_ context.Context, name string, args map[string]string, output string, metadata map[string]any, callErr error) {
	a := o.agent
	merged := make(map[string]string, len(args)+3)
	merged["tool_name"] = name
	for k, v := range args {
		merged[k] = v
	}
	merged["output"] = output
	if callErr != nil {
		merged["error"] = callErr.Error()
	}
	if metadata != nil {
		if v, ok := metadata["skill_name"].(string); ok && v != "" {
			merged["skill_name"] = v
		}
		if v, ok := metadata["skill_duplicate"].(bool); ok && v {
			merged["skill_duplicate"] = "true"
		}
	}
	if callErr == nil && isExecChannelTool(name) {
		a.execTimeoutStreak.Store(0)
	}

	previousTargetErrors := a.state.ConsecutiveTargetErrors
	result := a.hooks.Fire(OnToolResult, a.state, merged)
	if a.stopForHook(result) {
		return
	}
	if result.EmitMessage != "" {
		a.emit(Event{Type: "message", Content: result.EmitMessage, TotalTokens: a.syncBudgetTokens()})
	}
	if result.Nudge != "" {
		a.appendUserNudge(result.Nudge)
	}
	// Metadata must survive the projection: the web layer gates the live
	// report_vulnerability persist on ToolResult.Metadata["vuln_id"]
	// (scan_session). Dropping it here stranded accepted findings in the
	// reporting store until session teardown — scan.json vulns stayed empty
	// for the whole run on the eino path.
	a.emit(Event{Type: "tool_result", ToolName: name, ToolResult: tools.Result{
		Output: output, Error: merged["error"], Success: merged["error"] == "",
		Metadata: metadata,
	}, TotalTokens: a.syncBudgetTokens()})

	if a.targetUnresponsiveAfterTool(previousTargetErrors) {
		return
	}

	finished := name == "finish"
	if metadata != nil {
		if v, ok := metadata["finished"].(bool); ok && v {
			finished = true
		}
	}
	if finished {
		if callErr == nil {
			a.completeWithFinish(output, func() int { return a.syncBudgetTokens() })
			return
		}
		// The gate accepted this finish but execution failed: disarm the stop
		// probe so the next model turn — a finish retry — is allowed again.
		a.finishAccepted.Store(false)
	}

	switch name {
	case "report_vulnerability":
		a.setNextTemperature(TempReporter)
	case "add_note", "read_notes":
		a.setNextTemperature(TempReasoner)
	default:
		a.setNextTemperature(TempScanner)
	}
}

// targetUnresponsiveAfterTool preserves the sustained-outage breaker: a stale
// failure streak alone cannot cancel a scan, but a corroborated outage can.
func (a *Agent) targetUnresponsiveAfterTool(previousTargetErrors int) bool {
	if a.state.TargetUnresponsiveSince.IsZero() ||
		time.Since(a.state.TargetUnresponsiveSince) <= targetUnresponsiveKillThreshold ||
		a.state.ConsecutiveTargetErrors < 3 ||
		a.state.ConsecutiveTargetErrors <= previousTargetErrors {
		return false
	}
	if !a.confirmTargetUnresponsive() {
		a.state.ConsecutiveTargetErrors = 0
		a.state.TargetUnresponsiveSince = time.Time{}
		a.emit(Event{Type: "message", Content: a.op("Target outage was not confirmed; scan continues on available routes.", "目标失联未被证实；扫描在可用路由上继续。"), TotalTokens: a.syncBudgetTokens()})
		return false
	}
	downFor := time.Since(a.state.TargetUnresponsiveSince).Round(time.Minute)
	detail := fmt.Sprintf(
		"Agent stopped before clean completion: the target was unreachable or returning gateway failures continuously for %s across %d consecutive failed requests. Existing findings are preserved; re-run the scan once the target recovers.",
		downFor, a.state.ConsecutiveTargetErrors)
	a.emit(Event{Type: "error", Content: "⛔ TARGET UNRESPONSIVE: " + detail, TotalTokens: a.syncBudgetTokens()})
	if a.scanCtx != nil {
		a.scanCtx.Terminal.KillAll()
	}
	a.stopped.Store(true)
	if a.cancel != nil {
		a.cancel()
	}
	a.terminalOutcome.Store(&terminalCheckpoint{Content: detail, Aborted: true, Reason: "target_unresponsive"})
	a.emit(Event{Type: "finished", Content: detail, TotalTokens: a.syncBudgetTokens(), Aborted: true, AbortReason: "target_unresponsive"})
	return true
}

func normalizeEinoToolArgs(a *Agent, name string, args map[string]string) map[string]string {
	if name != "terminal_execute" {
		return args
	}
	command, ok := args["command"]
	if !ok {
		return args
	}
	contextID := ""
	if a.scanCtx != nil {
		contextID = a.scanCtx.ID
	}
	normalized, flagNote := terminal.NormalizeToolFlags(command)
	normalized, note := terminal.NormalizeCommandForRequestRatePolicy(contextID, normalized)
	note = flagNote + note
	normalized = terminal.InjectScanHeadersIntoCommand(normalized)
	normalized = terminal.CapDirbusterRuntime(normalized)
	if normalized == command {
		return args
	}
	if note != "" {
		a.emit(Event{Type: "message", Content: note, TotalTokens: a.syncBudgetTokens()})
	}
	out := make(map[string]string, len(args))
	for k, v := range args {
		out[k] = v
	}
	out["command"] = normalized
	return out
}

// BeforeModel restores the scanner's adaptive temperature before every
// provider attempt (including retries within one ADK turn).
func (o *einoObserver) BeforeModel(_ context.Context, _ int) engine.ModelRequestAction {
	a := o.agent
	temp := scannerTemperatureFor(a.state)
	if next := a.nextTemperature.Swap(nil); next != nil {
		temp = *next
	}
	a.client.SetTemperature(&temp)
	return engine.ModelRequestAction{Category: reasoningRequestCategory(a.state)}
}

func (a *Agent) setNextTemperature(v *float64) {
	a.nextTemperature.Store(v)
}

func (o *einoObserver) OnModelError(ctx context.Context, err error) engine.ModelErrorAction {
	a := o.agent
	if a.stopped.Load() || (ctx != nil && ctx.Err() != nil) {
		return engine.ModelErrorAction{Fatal: &engine.ErrTurnStopped{Reason: "stopped"}}
	}
	a.state.ConsecutiveErrors++
	classified := llm.ClassifyErrorString(err.Error())
	if classified.Class == llm.ErrorClassContextWindow {
		a.emit(Event{Type: "error", Content: "⚠️ Context window overflow — compacting the model context.", TotalTokens: a.syncBudgetTokens()})
		a.state.ConsecutiveErrors--
		if a.state.ConsecutiveErrors < 0 {
			a.state.ConsecutiveErrors = 0
		}
		return engine.ModelErrorAction{
			Retry:          true,
			CompactContext: true,
			Nudge:          "The previous request exceeded the model context window. Continue from the current plan and saved notes; do not call finish solely because history was compacted.",
		}
	}

	switch classified.Class {
	case llm.ErrorClassRateLimit, llm.ErrorClassOverloaded, llm.ErrorClassQuotaExhausted:
		a.state.ConsecutiveErrors--
		if a.state.ConsecutiveErrors < 0 {
			a.state.ConsecutiveErrors = 0
		}
		a.state.ConsecutiveRateLimits++
		abortReason := "provider_rate_limited"
		switch classified.Class {
		case llm.ErrorClassOverloaded:
			abortReason = "provider_overloaded"
		case llm.ErrorClassQuotaExhausted:
			abortReason = "provider_quota_exhausted"
		}
		backoff := a.rateLimitBackoff(a.state.ConsecutiveRateLimits)
		if classified.Class == llm.ErrorClassQuotaExhausted && a.rateLimitBackoffFn == nil && backoff < 30*time.Second {
			backoff = 30 * time.Second
		}
		maxWait := a.maxRateLimitWait()
		if maxWait > 0 {
			if a.state.CumulativeRateLimitWait >= maxWait {
				a.pauseForProviderWait(err, abortReason, maxWait, a.syncBudgetTokens())
				return engine.ModelErrorAction{Fatal: &engine.ErrProviderPaused{Reason: abortReason}}
			}
			if remaining := maxWait - a.state.CumulativeRateLimitWait; remaining < backoff {
				backoff = remaining
			}
		}
		a.logProviderWait(err, backoff, maxWait)
		a.pauseIdleClock(backoff)
		if !sleepContext(ctx, backoff) {
			return engine.ModelErrorAction{Fatal: &engine.ErrTurnStopped{Reason: "stopped"}}
		}
		if a.stopped.Load() {
			return engine.ModelErrorAction{Fatal: &engine.ErrTurnStopped{Reason: "stopped"}}
		}
		a.state.CumulativeRateLimitWait += backoff
		a.touchActivity()
		if maxWait > 0 && a.state.CumulativeRateLimitWait >= maxWait {
			a.pauseForProviderWait(err, abortReason, maxWait, a.syncBudgetTokens())
			return engine.ModelErrorAction{Fatal: &engine.ErrProviderPaused{Reason: abortReason}}
		}
		return engine.ModelErrorAction{Retry: true}
	}

	a.emit(Event{Type: "error", Content: fmt.Sprintf("LLM error (attempt %d/25): %s", a.state.ConsecutiveErrors, llm.SafeErrorDiagnostic(err)), TotalTokens: a.syncBudgetTokens()})
	if a.state.ConsecutiveErrors >= 25 {
		return engine.ModelErrorAction{Fatal: &engine.ErrScanAborted{
			Reason:  "llm_repeated_errors",
			Content: fmt.Sprintf("Agent stopped: LLM failed %d consecutive times. Last error: %s", a.state.ConsecutiveErrors, err),
		}}
	}
	backoff := time.Duration(a.state.ConsecutiveErrors*10) * time.Second
	if backoff > 120*time.Second {
		backoff = 120 * time.Second
	}
	a.pauseIdleClock(backoff)
	if !sleepContext(ctx, backoff) {
		return engine.ModelErrorAction{Fatal: &engine.ErrTurnStopped{Reason: "stopped"}}
	}
	return engine.ModelErrorAction{Retry: true}
}

func (o *einoObserver) OnModelResponse(_ context.Context, request []llm.Message, content string, calls []llm.ToolCall, usage *llm.TokenUsage) engine.ModelResponseAction {
	a := o.agent
	a.recordTokenAttribution(usage, request, a.state.Iteration, reasoningRequestCategory(a.state), 0)
	if usage != nil && usage.TotalTokens > 0 && a.state != nil {
		a.state.FinishRecoveryPending = false
	}
	a.touchActivity()

	if strings.TrimSpace(content) == "" && len(calls) == 0 {
		empty := a.hooks.Fire(OnEmptyResponse, a.state, nil)
		a.emit(Event{Type: "message", Content: a.opf("⚠️ LLM returned empty response (%d/12)", "⚠️ LLM 返回空响应（%d/12）", a.state.EmptyResponseCount), TotalTokens: a.syncBudgetTokens()})
		if empty.ForceSkip {
			return engine.ModelResponseAction{Fatal: &engine.ErrScanAborted{Reason: "llm_empty_responses", Content: "Agent stopped: LLM returned too many empty responses"}}
		}
		return engine.ModelResponseAction{Retry: true, Nudge: empty.Nudge}
	}
	if len(calls) == 0 {
		noTool := a.hooks.Fire(OnNoToolResponse, a.state, map[string]string{"response": content})
		if noTool.ForceSkip {
			reason, detail := classifyNoToolAbort(a.state)
			return engine.ModelResponseAction{Fatal: &engine.ErrScanAborted{Reason: reason, Content: detail}}
		}
		if noTool.PruneContext {
			a.state.MalformedToolOutputCount = 0
			return engine.ModelResponseAction{ResetContext: true, Nudge: a.protocolResetNudge(noTool.Nudge)}
		}
		return engine.ModelResponseAction{Retry: true, Nudge: noTool.Nudge}
	}

	if a.state.ConsecutiveRateLimits > 0 {
		log.Printf("[agent:%s] Upstream LLM recovered: wait_attempts=%d cumulative_wait=%s; continuing assessment",
			a.ID, a.state.ConsecutiveRateLimits, a.state.CumulativeRateLimitWait)
	}
	a.hooks.Fire(OnHealthyResponse, a.state, nil)
	return engine.ModelResponseAction{}
}

// CompactMessages enforces the bounded working context over the ADK state.
// Kept bytes remain reachable through notes; compacted history is converted
// into one deterministic digest before the next provider request.
func (o *einoObserver) CompactMessages(input []*schema.Message) []*schema.Message {
	a := o.agent
	if a == nil || a.cfg == nil || len(input) < 2 {
		return input
	}
	if a.boundedContextEnabled() {
		input = ageOutSchemaToolOutputs(input, a.toolArchiveActiveWindow())
	}
	threshold := a.compactThresholdBytes()
	if threshold <= 0 {
		return input
	}
	size := 0
	for _, msg := range input {
		if msg != nil {
			size += len(msg.Content) + perMessageOverheadBytes
		}
	}
	if size <= threshold && len(input) <= maxMessagesBeforePrune {
		return input
	}
	const keepRecent = 40
	if len(input) <= keepRecent+1 {
		return input
	}
	cutoff := len(input) - keepRecent
	if cutoff < 1 {
		cutoff = 1
	}
	old := make([]llm.Message, 0, cutoff-1)
	for _, msg := range input[1:cutoff] {
		if msg == nil {
			continue
		}
		old = append(old, llm.Message{Role: string(msg.Role), Content: msg.Content})
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[CONTEXT PRUNED: %d older messages were compacted to save context space.]\n\n", len(old)))
	sb.WriteString("## Compacted History\n")
	sb.WriteString(compactMessages(old))
	if notes := notes.FormatForContextID(a.scanCtx.ID); notes != "" {
		sb.WriteString("\n\n## Your Saved Notes\n")
		sb.WriteString(notes)
	}
	sb.WriteString("\n\nYou are still in the MIDDLE of your scan. DO NOT call finish — continue testing from where you left off.")
	out := make([]*schema.Message, 0, keepRecent+2)
	out = append(out, input[0])
	out = append(out, schema.UserMessage(sb.String()))
	for _, msg := range input[cutoff:] {
		if msg == nil {
			continue
		}
		cp := *msg
		if len(cp.Content) > maxRecentMsgBytes {
			cp.Content = cp.Content[:maxRecentMsgBytes] + "\n\n[OUTPUT TRUNCATED TO FIT CONTEXT WINDOW — re-run with grep/head/jq to extract specific data if needed]"
		}
		out = append(out, &cp)
	}
	a.compactionCount++
	return out
}

// ageOutSchemaToolOutputs is the Eino-state equivalent of ageOutToolOutputs:
// only archived messages are stubbed, so no unretrievable information is lost.
func ageOutSchemaToolOutputs(input []*schema.Message, window int) []*schema.Message {
	if window <= 0 {
		window = 8
	}
	indices := make([]int, 0, window+4)
	for i := len(input) - 1; i >= 0; i-- {
		if input[i] != nil && isToolResultMessage(input[i].Content) {
			indices = append(indices, i)
		}
	}
	if len(indices) <= window {
		return input
	}
	out := append([]*schema.Message(nil), input...)
	for k, idx := range indices {
		if k < window {
			continue
		}
		content := out[idx].Content
		if strings.Contains(content, archiveStubPrefix) {
			continue
		}
		id, ok := archiveIDFromMessage(content)
		if !ok {
			continue // never archived → leave verbatim
		}
		firstLine := content
		if i := strings.IndexByte(content, '\n'); i >= 0 {
			firstLine = content[:i]
		}
		head := trimToValidUTF8(content, archiveHeadBytes)
		cp := *out[idx]
		cp.Content = fmt.Sprintf(
			"%s %s%s — full original retrievable]\nHead: %s\n…[archived head only — use read_tool_output(id=\"%s\") to view the complete output]\n",
			firstLine, archiveStubPrefix, id, head, id,
		)
		out[idx] = &cp
	}
	return out
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	if ctx == nil {
		time.Sleep(d)
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
