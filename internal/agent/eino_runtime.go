package agent

// Eino execution bridge for Agent.Run. The common prelude (prompt, state,
// checkpoint restore, budgets, target scope) runs once; the ADK owns the
// model/tool loop, checkpointing and approval interrupts. There is no second
// runtime and no fallback path.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/engine"
)

// WithRuntime injects the durable checkpoint store and the interrupt-aware
// approval broker. Production wires this from scan_session; tests and CLI
// callers that omit it get a per-scan file/memory store at run time.
func WithRuntime(checkpoint adk.CheckPointStore, broker engine.ApprovalBroker) AgentOption {
	return func(a *Agent) {
		a.checkpointStore = checkpoint
		a.approvalBroker = broker
	}
}

func (a *Agent) runLoop(systemPrompt string, tokenCount func() int) {
	a.scanFinished.Store(false)
	if a.scanCtx == nil || a.scanCtx.ID == "" {
		a.emit(Event{Type: "finished", Content: a.op("Scan stopped: scan context is unavailable.", "扫描已停止：扫描上下文不可用。"), Aborted: true, AbortReason: "eino_init_failed", TotalTokens: tokenCount()})
		return
	}
	// Final graph projection: the last turn's findings/evidence must land even
	// if the loop ends without another BeforeTurn (R7/M1).
	if a.delegatedAgentID == "" {
		defer a.syncExplorationGraph()
	}
	// R7: the main agent is a per-message chat, not a continuous scan loop.
	// It never executes tools; workers own all execution.
	if a.plannerWorkerMode {
		a.runMainChatLoop(tokenCount)
		return
	}

	store := a.checkpointStore
	if store == nil {
		var err error
		if a.scanCtx.ScanDir != "" {
			store, err = engine.NewFileCheckpointStore(a.scanCtx.ScanDir)
		} else {
			store = engine.NewMemoryCheckpointStore()
		}
		if err != nil {
			a.emit(Event{Type: "finished", Content: a.op("Scan stopped: checkpoint store is unavailable: ", "扫描已停止：检查点存储不可用：") + err.Error(), Aborted: true, AbortReason: "eino_init_failed", TotalTokens: tokenCount()})
			return
		}
	}

	observer := &einoObserver{agent: a}
	rt, err := engine.NewRuntime(a.ctx, engine.RuntimeConfig{
		Client:          a.client,
		Registry:        a.registry,
		CheckpointStore: store,
		Instruction:     systemPrompt,
		MaxIterations:   a.maxIter,
		Obligations:     observer,
		ModelRecovery:   observer,
		Compactor:       observer.CompactMessages,
		NativeTools:     a.nativeToolCalling,
		Streaming:       einoStreamingEnabled(),
		DynamicTools:    einoToolSearchEnabled(),
		Approvals:       a.approvalBroker,
		InstanceID:      a.scanCtx.ID,
		ScanContextID:   a.scanCtx.ID,
		ToolTimeout:     a.hardTimeoutFor,
		// Reasoning models (DeepSeek reasoning_content, Anthropic thinking
		// deltas) surface their raw thinking as a "thinking" event on both the
		// streaming and non-streaming channels. a.emit redacts secrets; the
		// transcript intentionally keeps thinking out of the assistant rows.
		OnReasoning: func(reasoning string) {
			if strings.TrimSpace(reasoning) == "" {
				return
			}
			a.emit(Event{Type: "thinking", Content: reasoning, TotalTokens: tokenCount()})
		},
		OnToolTimeout: func(name string) {
			if isExecChannelTool(name) {
				streak := int(a.execTimeoutStreak.Add(1))
				if streak == execChannelBreakerThreshold {
					a.emit(Event{Type: "error", Content: fmt.Sprintf("⛔ EXEC-CHANNEL CIRCUIT BREAKER OPEN: %d consecutive exec tools hit the hard timeout — the execution backend is not returning results. terminal_execute/python_action calls are capped at %s per call until one succeeds. If quick commands also fail, record a typed disposition for the remaining lane work and finish instead of queuing more commands.", streak, execChannelDegradedTimeout)})
				}
				return
			}
			if !a.registry.SupportsContext(name) {
				a.uncooperativeTimeouts.Add(1)
			}
		},
		StopCheck: func() bool { return a.scanFinished.Load() || a.finishAccepted.Load() },
	})
	if err != nil {
		log.Printf("[eino] runtime construction failed for %s: %v", a.scanCtx.ID, err)
		a.emit(Event{Type: "finished", Content: a.op("Scan stopped: runtime could not be constructed: ", "扫描已停止：运行时构建失败：") + err.Error(), Aborted: true, AbortReason: "eino_init_failed", TotalTokens: tokenCount()})
		return
	}
	checkpointID := a.scanCtx.ID
	if a.delegatedAgentID != "" {
		// Specialists share the root ScanContext but must not share its ADK
		// checkpoint (concurrent root/specialist checkpoints would collide).
		checkpointID += ":agent:" + a.delegatedAgentID
	}
	runCtx, cancelRun := context.WithCancel(a.ctx)
	defer cancelRun()
	// Publish the run cancel so completeWithFinish can stop the ADK graph the
	// moment the scan is complete; the graph otherwise runs ahead of the event
	// consumer and issues one extra discarded model call.
	a.runCancel.Store(&cancelRun)
	// B8 (default off): a delegated lane whose durable approval checkpoint
	// survived a restart resumes at the interrupt instead of replaying the lane
	// from scratch. Any doubt — missing/unsupported checkpoint, non-lane id,
	// flag off — falls back to the fresh run below, unchanged.
	iter := a.einoRunOrResumeLane(rt, store, checkpointID, runCtx, tokenCount)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if a.scanFinished.Load() {
			return
		}
		if a.stopped.Load() || (a.ctx != nil && a.ctx.Err() != nil) {
			break
		}
		if ev.Err != nil {
			var abort *engine.ErrScanAborted
			if errors.As(ev.Err, &abort) {
				a.emit(Event{Type: "finished", Content: abort.Content, Aborted: true, AbortReason: abort.Reason, TotalTokens: tokenCount()})
				return
			}
			var paused *engine.ErrProviderPaused
			if errors.As(ev.Err, &paused) {
				// The pause event was already emitted by the provider-wait
				// policy; pause is resumable and is not a terminal assessment.
				return
			}
			var turnStop *engine.ErrTurnStopped
			if errors.As(ev.Err, &turnStop) {
				a.stopForHook(HookResult{StopReason: turnStop.Reason, EmitMessage: a.op("Scan stopped: ", "扫描已停止：") + turnStop.Reason + a.op("; saved findings are preserved.", "；已保存的发现不受影响。")})
				return
			}
			log.Printf("[eino] runtime event error for %s: %v", a.scanCtx.ID, ev.Err)
			a.emit(Event{Type: "finished", Content: a.op("Scan stopped: runtime error: ", "扫描已停止：运行时错误：") + ev.Err.Error(), Aborted: true, AbortReason: "eino_runtime_error", TotalTokens: tokenCount()})
			return
		}
		// Approval interrupts are checked before MessageOutput and are
		// independent of the streaming mode: a streamed turn still interrupts
		// through Action.Interrupted and still resumes via
		// engine.Runtime.Resume → ResumeWithParams.
		if ev.Action != nil && ev.Action.Interrupted != nil {
			next, resumeErr := a.resumeEinoInterrupt(rt, checkpointID, ev)
			if resumeErr != nil {
				log.Printf("[eino] approval resume failed for %s: %v", a.scanCtx.ID, resumeErr)
				a.emit(Event{Type: "finished", Content: a.op("Scan stopped: approval resume failed: ", "扫描已停止：审批恢复失败：") + resumeErr.Error(), Aborted: true, AbortReason: "eino_approval_resume_failed", TotalTokens: tokenCount()})
				return
			}
			iter = next
			continue
		}
		if ev.Output != nil && ev.Output.MessageOutput != nil {
			a.consumeEinoOutput(ev.Output.MessageOutput, tokenCount)
		}
	}
	if a.scanFinished.Load() {
		return
	}
	switch {
	case a.ctx != nil && a.ctx.Err() == context.DeadlineExceeded:
		a.emit(Event{Type: "finished", Content: a.op("Scan stopped: duration budget exhausted; saved findings are preserved.", "扫描已停止：时长预算耗尽；已保存的发现不受影响。"), Aborted: true, AbortReason: "resource_budget", TotalTokens: tokenCount()})
	case a.stopped.Load():
		a.emit(Event{Type: "finished", Content: a.op("Scan stopped", "扫描已停止"), TotalTokens: tokenCount()})
	case a.ctx != nil && a.ctx.Err() != nil:
		// The semantic watchdog cancels the context on a liveness expiry. That
		// is an engine-owned partial terminal outcome, not an external pause.
		if a.livenessExpired.Load() || a.semanticIdleRemaining() <= 0 {
			a.emitContextStop()
			return
		}
		// External shutdown/cancellation is resumable: persisted plan,
		// coverage and checkpoints remain executable on the next start.
		a.emit(Event{Type: "finished", Content: a.op("Scan canceled: ", "扫描已取消：") + a.ctx.Err().Error(), TotalTokens: tokenCount(), Resumable: true})
	default:
		a.emit(Event{Type: "finished", Content: a.op("Scan ended", "扫描结束"), TotalTokens: tokenCount()})
	}
}

func (a *Agent) resumeEinoInterrupt(rt *engine.Runtime, checkpointID string, ev *adk.AgentEvent) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	info, targetID, ok := einoInterruptInfo(ev)
	if !ok {
		return nil, errors.New("interrupt did not carry an approval context")
	}
	waiter, ok := a.approvalBroker.(engine.ApprovalWaiter)
	if !ok {
		return nil, errors.New("approval broker does not support interrupt/resume")
	}
	if a.ctx == nil {
		return nil, errors.New("scan context is nil")
	}
	decision, err := waiter.WaitApproval(a.ctx, info.ApprovalID)
	if err != nil {
		return nil, err
	}
	return rt.Resume(a.ctx, checkpointID, targetID, decision)
}

func einoInterruptInfo(ev *adk.AgentEvent) (engine.ApprovalInterruptInfo, string, bool) {
	if ev == nil || ev.Action == nil || ev.Action.Interrupted == nil {
		return engine.ApprovalInterruptInfo{}, "", false
	}
	for _, ictx := range ev.Action.Interrupted.InterruptContexts {
		if ictx == nil || !ictx.IsRootCause {
			continue
		}
		switch info := ictx.Info.(type) {
		case engine.ApprovalInterruptInfo:
			if info.ApprovalID != "" {
				return info, ictx.ID, true
			}
		case *engine.ApprovalInterruptInfo:
			if info != nil && info.ApprovalID != "" {
				return *info, ictx.ID, true
			}
		}
	}
	return engine.ApprovalInterruptInfo{}, "", false
}

func (a *Agent) einoMessages() []*schema.Message {
	out := make([]*schema.Message, 0, len(a.messages))
	for i, msg := range a.messages {
		if i == 0 && strings.EqualFold(msg.Role, "system") {
			continue // supplied as AgentConfig.Instruction
		}
		out = append(out, &schema.Message{
			Role:    schema.RoleType(msg.Role),
			Content: msg.Content,
		})
	}
	if len(out) == 0 {
		out = append(out, schema.UserMessage(fmt.Sprintf("Begin the security assessment for %s", strings.Join(a.targets, ", "))))
	}
	return out
}

// einoStreamingEnabled reports whether the incremental assistant-text
// projection is on. Streaming is always on: the env-var experiment was retired
// (no settings-page switch), and the recovery + projection paths are the
// production behavior.
func einoStreamingEnabled() bool {
	return true
}

// einoToolSearchEnabled reports whether the B7 dynamic tool surface is on. It is
// always on; the engine remains the authority on whether it is effective — inert
// in XML fallback mode and when a lane has nothing curated as searchable, and
// the curated pool is conservative (only tools with no prompt/gate dependency),
// so ordinary lanes keep their full tool list.
func einoToolSearchEnabled() bool {
	return true
}

// consumeEinoOutput projects one ADK MessageOutput into session events.
//
// Non-streaming output keeps its existing shape: a complete non-empty message
// becomes exactly one "message" event. A streaming output carries the whole
// model generation as a chunk stream, so the stream is always drained here —
// Eino's producer blocks on an unread stream. Every delta and the closing
// assistant_done of one turn share a MessageID:
//
//   - live clients append the deltas to one bubble (the chat store keys its
//     streaming buffer on message_id) and finalize it from assistant_done;
//   - the durable transcript folds assistant_done into one assistant row (see
//     web/chat_transcript.go), so the turn survives a REST refetch.
//
// No duplicate "message" is emitted for a streamed turn: that would give the
// live store a second assistant row for the same text.
//
// Tool-role streams are drained and discarded: their result events come from
// the tool observer, and projecting tool output as assistant text would corrupt
// or duplicate the transcript.
func (a *Agent) consumeEinoOutput(mv *adk.MessageVariant, tokenCount func() int) {
	if mv == nil {
		return
	}
	if mv.IsStreaming && mv.MessageStream != nil {
		a.consumeEinoStream(mv, tokenCount)
		return
	}
	if mv.Message != nil && mv.Message.Content != "" {
		a.emit(Event{Type: "message", Content: mv.Message.Content, TotalTokens: tokenCount()})
	}
}

// nextStreamMessageID names one streamed assistant turn. The agent id keeps it
// unique across runs; the counter keeps turns distinct within a run.
func (a *Agent) nextStreamMessageID() string {
	return fmt.Sprintf("%s-msg-%d", a.ID, a.streamMsgSeq.Add(1))
}

func (a *Agent) consumeEinoStream(mv *adk.MessageVariant, tokenCount func() int) {
	stream := mv.MessageStream
	defer stream.Close()
	// Assistant output is what the session transcript projects. An empty role
	// is treated as assistant for forward compatibility with Eino paths that
	// do not tag model output (the ADK's own model output event uses
	// schema.Assistant).
	assistant := mv.Role == "" || mv.Role == schema.Assistant
	messageID := a.nextStreamMessageID()
	var full strings.Builder
	// pending holds the tail of the assistant text that is withheld from the
	// delta projection because it may be the beginning of a registered secret
	// split across chunks. a.emit redacts complete secrets, but a secret split
	// over two deltas would otherwise leak in fragments, so the boundary is
	// never allowed to fall inside one.
	var pending string
	for {
		chunk, err := stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("[eino] assistant stream error for %s: %v", a.einoLogID(), err)
			}
			break
		}
		if chunk == nil || chunk.Content == "" || !assistant {
			continue
		}
		full.WriteString(chunk.Content)
		emit, keep := a.splitEinoDelta(pending + chunk.Content)
		pending = keep
		if emit != "" {
			a.emit(Event{Type: "assistant_delta", MessageID: messageID, Delta: emit, Content: emit, TotalTokens: tokenCount()})
		}
	}
	if !assistant {
		return
	}
	if pending != "" {
		a.emit(Event{Type: "assistant_delta", MessageID: messageID, Delta: pending, Content: pending, TotalTokens: tokenCount()})
	}
	if text := full.String(); text != "" {
		a.emit(Event{Type: "assistant_done", MessageID: messageID, Content: text, TotalTokens: tokenCount()})
	}
}

// splitEinoDelta returns the prefix of accumulated assistant text that is safe
// to emit as a delta and the suffix that must be held back. When no secrets are
// registered the whole accumulation is safe. Otherwise the held-back suffix is
// the longest proper suffix that is a prefix of a registered secret, so no
// delta boundary can split a secret across two events.
func (a *Agent) splitEinoDelta(accumulated string) (emit, keep string) {
	if len(a.secretValues) == 0 || accumulated == "" {
		return accumulated, ""
	}
	safe := len(accumulated)
	for _, secret := range a.secretValues {
		if secret == "" {
			continue
		}
		max := len(secret) - 1
		if max > len(accumulated) {
			max = len(accumulated)
		}
		for n := max; n >= 1; n-- {
			if strings.HasSuffix(accumulated, secret[:n]) {
				if cut := len(accumulated) - n; cut < safe {
					safe = cut
				}
				break
			}
		}
	}
	return accumulated[:safe], accumulated[safe:]
}

func (a *Agent) einoLogID() string {
	if a != nil && a.scanCtx != nil && a.scanCtx.ID != "" {
		return a.scanCtx.ID
	}
	if a != nil {
		return a.ID
	}
	return ""
}
