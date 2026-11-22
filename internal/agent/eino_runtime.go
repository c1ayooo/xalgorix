package agent

// Eino execution bridge for Agent.Run. The common prelude (prompt, state,
// checkpoint restore, budgets, target scope) runs once; the ADK owns the
// model/tool loop, checkpointing and approval interrupts. There is no second
// runtime and no fallback path.

import (
	"context"
	"errors"
	"fmt"
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
		Approvals:       a.approvalBroker,
		InstanceID:      a.scanCtx.ID,
		ScanContextID:   a.scanCtx.ID,
		ToolTimeout:     a.hardTimeoutFor,
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
	iter := rt.Run(runCtx, a.einoMessages(), checkpointID)
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
		if ev.Output != nil && ev.Output.MessageOutput != nil && ev.Output.MessageOutput.Message != nil {
			msg := ev.Output.MessageOutput.Message
			if msg.Content != "" {
				a.emit(Event{Type: "message", Content: msg.Content, TotalTokens: tokenCount()})
			}
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
