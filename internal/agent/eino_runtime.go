package agent

// Eino execution bridge for Agent.Run. The legacy prelude (prompt, state,
// checkpoint restore, budgets, target scope) is shared; only the model/tool
// loop switches. A construction or resume failure is terminal and visible — it
// never silently falls back to legacy.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/xalgord/xalgorix/v4/internal/engine"
)

// WithEinoRuntime injects the durable checkpoint store and the interrupt-aware
// approval broker. Both are required when XALGORIX_ENGINE=eino.
func WithEinoRuntime(checkpoint adk.CheckPointStore, broker engine.ApprovalBroker) AgentOption {
	return func(a *Agent) {
		a.einoCheckpointStore = checkpoint
		a.einoApprovalBroker = broker
	}
}

func (a *Agent) runEinoLoop(systemPrompt string, tokenCount func() int) {
	a.einoFinished.Store(false)
	if a.einoCheckpointStore == nil {
		a.emit(Event{Type: "finished", Content: "Scan stopped: Eino checkpoint store is unavailable.", Aborted: true, AbortReason: "eino_init_failed", TotalTokens: tokenCount()})
		return
	}
	if a.scanCtx == nil || a.scanCtx.ID == "" {
		a.emit(Event{Type: "finished", Content: "Scan stopped: scan context is unavailable for Eino.", Aborted: true, AbortReason: "eino_init_failed", TotalTokens: tokenCount()})
		return
	}
	// Fail closed on protocol capability: the ADK speaks only the native
	// tool-calling channel and the llm client attaches the tools array on the
	// OpenAI-compatible branch only. Anthropic, Gemini and Responses endpoints
	// would run a deterministic one-turn empty scan here; legacy remains their
	// engine until internal/llm sends tools on those protocols.
	if a.client != nil && !a.client.NativeToolCallingSupported() {
		a.emit(Event{Type: "finished", Content: "Scan stopped: the configured provider protocol does not support native tool calling, which the Eino engine requires. Set XALGORIX_ENGINE=legacy for this endpoint.", Aborted: true, AbortReason: "eino_init_failed", TotalTokens: tokenCount()})
		return
	}
	rt, err := engine.NewRuntime(a.ctx, engine.RuntimeConfig{
		Client:          a.client,
		Registry:        a.registry,
		CheckpointStore: a.einoCheckpointStore,
		Instruction:     systemPrompt,
		MaxIterations:   a.maxIter,
		Obligations:     a.EngineObserver(),
		Approvals:       a.einoApprovalBroker,
		InstanceID:      a.scanCtx.ID,
		ScanContextID:   a.scanCtx.ID,
		ToolTimeout:     a.hardTimeoutFor,
		OnToolTimeout: func(name string) {
			if !a.registry.SupportsContext(name) {
				a.uncooperativeTimeouts.Add(1)
			}
		},
		StopCheck: func() bool { return a.einoFinished.Load() || a.einoFinishAccepted.Load() },
	})
	if err != nil {
		log.Printf("[eino] runtime construction failed for %s: %v", a.scanCtx.ID, err)
		a.emit(Event{Type: "finished", Content: "Scan stopped: Eino runtime could not be constructed: " + err.Error(), Aborted: true, AbortReason: "eino_init_failed", TotalTokens: tokenCount()})
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
	// consumer and issues one extra model call whose result is discarded.
	cancelFunc := context.CancelFunc(cancelRun)
	a.einoRunCancel.Store(&cancelFunc)
	iter := rt.Run(runCtx, a.einoMessages(), checkpointID)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if a.einoFinished.Load() {
			// finish already emitted the terminal summary; cancelling the child
			// context stops the ADK loop from issuing another model turn.
			return
		}
		if a.stopped.Load() || (a.ctx != nil && a.ctx.Err() != nil) {
			break
		}
		if ev.Err != nil {
			var turnStop *engine.ErrTurnStopped
			if errors.As(ev.Err, &turnStop) {
				a.stopForHook(HookResult{StopReason: turnStop.Reason, EmitMessage: "Scan stopped: " + turnStop.Reason + "; saved findings are preserved."})
				return
			}
			log.Printf("[eino] runtime event error for %s: %v", a.scanCtx.ID, ev.Err)
			a.emit(Event{Type: "finished", Content: "Scan stopped: Eino runtime error: " + ev.Err.Error(), Aborted: true, AbortReason: "eino_runtime_error", TotalTokens: tokenCount()})
			return
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			next, resumeErr := a.resumeEinoInterrupt(rt, checkpointID, ev)
			if resumeErr != nil {
				log.Printf("[eino] approval resume failed for %s: %v", a.scanCtx.ID, resumeErr)
				a.emit(Event{Type: "finished", Content: "Scan stopped: approval resume failed: " + resumeErr.Error(), Aborted: true, AbortReason: "eino_approval_resume_failed", TotalTokens: tokenCount()})
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
	if a.einoFinished.Load() {
		return
	}
	switch {
	case a.ctx != nil && a.ctx.Err() == context.DeadlineExceeded:
		a.emit(Event{Type: "finished", Content: "Scan stopped: duration budget exhausted; saved findings are preserved.", Aborted: true, AbortReason: "resource_budget", TotalTokens: tokenCount()})
	case a.stopped.Load():
		a.emit(Event{Type: "finished", Content: "Scan stopped", TotalTokens: tokenCount()})
	case a.ctx != nil && a.ctx.Err() != nil:
		a.emit(Event{Type: "finished", Content: "Scan canceled: " + a.ctx.Err().Error(), TotalTokens: tokenCount()})
	default:
		a.emit(Event{Type: "finished", Content: "Scan ended", TotalTokens: tokenCount()})
	}
}

func (a *Agent) resumeEinoInterrupt(rt *engine.Runtime, checkpointID string, ev *adk.AgentEvent) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	info, targetID, ok := einoInterruptInfo(ev)
	if !ok {
		return nil, errors.New("interrupt did not carry an approval context")
	}
	waiter, ok := a.einoApprovalBroker.(engine.ApprovalWaiter)
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
