package engine

// Lane checkpoint recovery (B8). The ADK runner writes a durable checkpoint to
// the configured CheckPointStore only when a run interrupts at a safe point
// (a business StatefulInterrupt) or is cancelled with an interrupt signal — it
// does NOT checkpoint every step (see cloudwego/eino adk/runner.go
// typedRunnerHandleIterImpl and adk/interrupt.go runnerSaveCheckPointImpl). The
// only durable state a lane can resume is therefore an interrupt/cancel
// checkpoint; nested compose checkpoints live in a per-run in-memory bridge
// store and are folded into that outer payload.
//
// This file recognizes the one interrupt shape the lane path can resume without
// additional external state: an approval interrupt produced by ApprovalTool.
// Every other shape (unknown/legacy payloads, non-approval interrupts) is
// reported as "not resumable" so callers fall back to their existing behaviour.

import (
	"bytes"
	"context"
	"encoding/gob"
	"strings"

	"github.com/cloudwego/eino/adk"
)

// LaneCheckpoint is the resumable approval interrupt found in a durable
// checkpoint. It carries only identifiers — never tool arguments or interrupt
// payload text — so it is safe to log or include in events.
type LaneCheckpoint struct {
	// TargetID is the fully-qualified interrupt address to pass to
	// Runtime.Resume (the same ID an interrupted event exposes as the
	// root-cause InterruptCtx.ID).
	TargetID string
	// ApprovalID identifies the persisted approval row.
	ApprovalID string
	// ToolName is the gated tool that interrupted.
	ToolName string
}

// HasCheckpoint reports whether a durable checkpoint payload exists for id. A
// nil runtime, an empty id, or a missing payload all report false with a nil
// error. Storage errors are surfaced so the caller can fall back to a fresh run
// without treating the probe as authoritative.
func (r *Runtime) HasCheckpoint(ctx context.Context, checkpointID string) (bool, error) {
	if r == nil || r.store == nil {
		return false, nil
	}
	if strings.TrimSpace(checkpointID) == "" {
		return false, nil
	}
	_, existed, err := r.store.Get(ctx, checkpointID)
	if err != nil {
		return false, err
	}
	return existed, nil
}

// approvalCheckpointWire mirrors the exported parts of the ADK runner
// checkpoint (adk.serialization) needed to recognize an approval interrupt. Gob
// matches fields by name, so the fields this struct omits (RunCtx, the
// interrupt state maps) are skipped during decode rather than required.
type approvalCheckpointWire struct {
	Info *adk.InterruptInfo
}

// InspectApprovalCheckpoint decodes a durable checkpoint payload and returns
// the single approval interrupt it carries. It reports false (never panics,
// never returns an error) for any payload that is not a decodable ADK
// checkpoint with exactly one approval root cause. The payload itself is never
// logged: it can contain the full interrupted conversation and tool state.
func InspectApprovalCheckpoint(payload []byte) (cp LaneCheckpoint, ok bool) {
	if len(payload) == 0 {
		return LaneCheckpoint{}, false
	}
	// A malformed payload is an expected "cannot resume" outcome, not a crash:
	// the caller falls back to the existing run path.
	defer func() {
		if recover() != nil {
			cp, ok = LaneCheckpoint{}, false
		}
	}()
	wire := &approvalCheckpointWire{}
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(wire); err != nil {
		return LaneCheckpoint{}, false
	}
	return approvalCheckpointFromInfo(wire.Info)
}

// approvalCheckpointFromInfo extracts the approval target from the runner's
// saved InterruptInfo. The runner stores the chat-model agent wrapper in
// Info.Data; its Info field is the compose-level interrupt info whose
// InterruptContexts carry the user-facing ApprovalInterruptInfo.
func approvalCheckpointFromInfo(info *adk.InterruptInfo) (LaneCheckpoint, bool) {
	if info == nil {
		return LaneCheckpoint{}, false
	}
	chat, ok := info.Data.(*adk.ChatModelAgentInterruptInfo)
	if !ok || chat == nil || chat.Info == nil {
		return LaneCheckpoint{}, false
	}
	var (
		found   LaneCheckpoint
		matches int
	)
	for _, ictx := range chat.Info.InterruptContexts {
		if ictx == nil || !ictx.IsRootCause {
			continue
		}
		switch detail := ictx.Info.(type) {
		case ApprovalInterruptInfo:
			if detail.ApprovalID == "" || strings.TrimSpace(ictx.ID) == "" {
				continue
			}
			found = LaneCheckpoint{TargetID: ictx.ID, ApprovalID: detail.ApprovalID, ToolName: detail.ToolName}
			matches++
		case *ApprovalInterruptInfo:
			if detail == nil || detail.ApprovalID == "" || strings.TrimSpace(ictx.ID) == "" {
				continue
			}
			found = LaneCheckpoint{TargetID: ictx.ID, ApprovalID: detail.ApprovalID, ToolName: detail.ToolName}
			matches++
		}
	}
	// Exactly one root-cause approval: multiple concurrent approvals are not a
	// single-target resume and must fall back rather than guess.
	if matches != 1 {
		return LaneCheckpoint{}, false
	}
	return found, true
}
