package agent

// B8: delegated-lane checkpoint recovery. Always enabled, and never used for the
// root scan checkpoint. A delegated lane whose durable ADK approval checkpoint
// survived a restart resumes at the interrupted tool step instead of replaying
// the lane from scratch; every other shape falls back to a fresh run. This file
// owns only the policy; the ADK checkpoint payload is decoded by
// engine.InspectApprovalCheckpoint and the actual resume is engine.Runtime.Resume
// (the existing ResumeWithParams path used by the in-process approval interrupt).

import (
	"context"
	"log"
	"strings"

	"github.com/cloudwego/eino/adk"

	"github.com/c1ayooo/xalgorix/v4/internal/engine"
)

// einoLaneResumeEnabled reports whether the lane-resume path is on. It is always
// on: resuming is itself gated by a durable, parseable approval checkpoint that
// belongs to a delegated lane; anything else falls back to a fresh run.
func einoLaneResumeEnabled() bool {
	return true
}

// isDelegatedLaneCheckpoint reports whether the checkpoint ID belongs to a
// specialist lane. runLoop namespaces delegated checkpoints as
// "<scanID>:agent:<agentID>"; the root scan checkpoint must never take this
// path.
func isDelegatedLaneCheckpoint(checkpointID string) bool {
	return strings.Contains(checkpointID, ":agent:")
}

// einoRunOrResumeLane prefers a provably safe delegated-lane resume and
// otherwise starts a fresh run. It is the single selection point used by
// runLoop, so the fallback semantics are pinned by test.
func (a *Agent) einoRunOrResumeLane(rt *engine.Runtime, store adk.CheckPointStore, checkpointID string, runCtx context.Context, tokenCount func() int) *adk.AsyncIterator[*adk.AgentEvent] {
	if iter := a.resumeLaneCheckpoint(rt, store, checkpointID, runCtx, tokenCount); iter != nil {
		return iter
	}
	return rt.Run(runCtx, a.einoMessages(), checkpointID)
}

// resumeLaneCheckpoint tries to continue a delegated lane from a durable
// approval-interrupt checkpoint. It returns nil whenever the resume is not
// provably safe, and the caller then starts a fresh run exactly as before.
//
// Safety contract:
//   - Only delegated lanes (:agent:); the root checkpoint never takes this path.
//   - Only an ADK approval interrupt is eligible; any other checkpoint shape is
//     "not resumable" (no panic, no partial run).
//   - The stale approval is resumed with a denial. Across a process restart the
//     pending approval row has already been reconciled to a terminal
//     cancel/timeout; resuming with a denial guarantees a previously gated — and
//     possibly already executed — action is never executed a second time. The
//     model receives the normal denial tool result and, if it wants the action,
//     must request approval again through the live operator path.
func (a *Agent) resumeLaneCheckpoint(rt *engine.Runtime, store adk.CheckPointStore, checkpointID string, runCtx context.Context, tokenCount func() int) *adk.AsyncIterator[*adk.AgentEvent] {
	if !einoLaneResumeEnabled() {
		return nil
	}
	if a == nil || a.delegatedAgentID == "" || !isDelegatedLaneCheckpoint(checkpointID) {
		return nil
	}
	if rt == nil || store == nil {
		return nil
	}
	present, err := rt.HasCheckpoint(runCtx, checkpointID)
	if err != nil {
		log.Printf("[eino] lane resume probe failed for %s: %v", a.einoLogID(), err)
		return nil
	}
	if !present {
		return nil
	}
	payload, existed, err := store.Get(runCtx, checkpointID)
	if err != nil || !existed {
		if err != nil {
			log.Printf("[eino] lane resume read failed for %s: %v", a.einoLogID(), err)
		}
		return nil
	}
	cp, ok := engine.InspectApprovalCheckpoint(payload)
	if !ok {
		// Present but not a resumable approval interrupt: fall back untouched.
		return nil
	}
	iter, err := rt.Resume(runCtx, checkpointID, cp.TargetID, engine.ApprovalDecision{
		Approved: false,
		Status:   "cancelled",
		Reason:   "delegated lane resumed after a restart; the stale approval was not executed and must be requested again",
	})
	if err != nil {
		log.Printf("[eino] lane resume failed for %s: %v", a.einoLogID(), err)
		return nil
	}
	a.emit(Event{
		Type: "message",
		Content: a.op(
			"Resumed delegated lane from a saved approval checkpoint; the stale gated call was not executed and will be requested again if needed.",
			"已从保存的审批检查点恢复委派子任务；先前的受控调用未执行，如需执行将重新申请审批。",
		),
		TotalTokens: tokenCount(),
	})
	return iter
}
