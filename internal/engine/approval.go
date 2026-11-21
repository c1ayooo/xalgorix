package engine

// Approval tool wrapper. The first invocation creates a pending approval row
// through the injected broker and interrupts the Eino run. A decision is
// supplied to Runner.ResumeWithParams as an ApprovalDecision; the wrapper then
// either executes the underlying RegistryTool or returns a denial as a normal
// tool result. The existing R2 approvals table and REST/WS contract stay the
// system of record.

import (
	"context"
	"encoding/gob"
	"fmt"
	"strings"

	einoTool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func init() {
	// Eino serializes interrupt state/resume payloads through gob checkpoints.
	gob.Register(approvalState{})
	gob.Register(ApprovalDecision{})
	gob.Register(ApprovalInterruptInfo{})
}

// ApprovalRequest is the material the broker persists. InstanceID/ScanContextID
// are supplied by the caller's context adapter (the engine never owns scope).
type ApprovalRequest struct {
	ToolName      string
	Args          map[string]string
	InstanceID    string
	ScanContextID string
}

// ApprovalBroker creates the pending approval row and returns its ID. An empty
// ID means the policy auto-approved this call (no interrupt).
type ApprovalBroker interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (approvalID string, err error)
}

// ApprovalDecision is supplied as interrupt resume data.
type ApprovalDecision struct {
	Approved bool   `json:"approved"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

// ApprovalWaiter resolves one pending approval to a decision. The ADK runner
// needs this after emitting an interrupt: the scan goroutine waits here while
// the REST/WS layer updates the normal approvals row, then resumes the runner.
type ApprovalWaiter interface {
	WaitApproval(ctx context.Context, approvalID string) (ApprovalDecision, error)
}

// ApprovalInterruptInfo is user-facing data attached to the interrupt. The web
// layer reads ApprovalID to match the pending row with the resume target.
type ApprovalInterruptInfo struct {
	ApprovalID    string            `json:"approval_id"`
	ToolName      string            `json:"tool_name"`
	Args          map[string]string `json:"args,omitempty"`
	InstanceID    string            `json:"instance_id,omitempty"`
	ScanContextID string            `json:"scan_context_id,omitempty"`
}

type approvalState struct {
	ApprovalID string
	ToolName   string
}

// ApprovalTool wraps one InvokableTool with the interrupt/resume protocol.
type ApprovalTool struct {
	inner  einoTool.InvokableTool
	name   string
	broker ApprovalBroker
	req    ApprovalRequest
}

var _ einoTool.InvokableTool = (*ApprovalTool)(nil)

// NewApprovalTool wraps inner. instanceID/scanContextID are copied into each
// approval row; the broker is expected to be process-scoped.
func NewApprovalTool(inner einoTool.InvokableTool, name string, broker ApprovalBroker, instanceID, scanContextID string) *ApprovalTool {
	return &ApprovalTool{
		inner:  inner,
		name:   name,
		broker: broker,
		req: ApprovalRequest{
			ToolName:      name,
			InstanceID:    instanceID,
			ScanContextID: scanContextID,
		},
	}
}

func (t *ApprovalTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *ApprovalTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...einoTool.Option) (string, error) {
	if t.broker == nil {
		return t.inner.InvokableRun(ctx, argumentsInJSON, opts...)
	}
	wasInterrupted, _, state := einoTool.GetInterruptState[approvalState](ctx)
	if !wasInterrupted {
		args, err := decodeRegistryArgs(argumentsInJSON)
		if err != nil {
			return "", fmt.Errorf("engine: approval args for %q: %w", t.name, err)
		}
		req := t.req
		req.Args = args
		approvalID, err := t.broker.RequestApproval(ctx, req)
		if err != nil {
			return "", fmt.Errorf("engine: create approval for %q: %w", t.name, err)
		}
		if approvalID == "" {
			return t.inner.InvokableRun(ctx, argumentsInJSON, opts...)
		}
		state = approvalState{ApprovalID: approvalID, ToolName: t.name}
		info := ApprovalInterruptInfo{
			ApprovalID:    approvalID,
			ToolName:      t.name,
			Args:          args,
			InstanceID:    t.req.InstanceID,
			ScanContextID: t.req.ScanContextID,
		}
		return "", einoTool.StatefulInterrupt(ctx, info, state)
	}

	isResumeTarget, hasData, decision := einoTool.GetResumeContext[ApprovalDecision](ctx)
	if !isResumeTarget || !hasData {
		info := ApprovalInterruptInfo{
			ApprovalID:    state.ApprovalID,
			ToolName:      state.ToolName,
			InstanceID:    t.req.InstanceID,
			ScanContextID: t.req.ScanContextID,
		}
		return "", einoTool.StatefulInterrupt(ctx, info, state)
	}
	if decision.Approved {
		return t.inner.InvokableRun(ctx, argumentsInJSON, opts...)
	}
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = "operator rejected the request"
	}
	status := strings.TrimSpace(decision.Status)
	if status == "" {
		status = "rejected"
	}
	return fmt.Sprintf("⛔ APPROVAL %s — %s (approval %s). The operation was not executed; choose an alternative path.",
		strings.ToUpper(status), reason, state.ApprovalID), nil
}
