package web

// Eino approval broker: creates the ordinary R2 pending row and lets the Agent
// goroutine wait for the operator decision. The ADK runner checkpoints the
// interruption; after WaitApproval returns, the Agent calls ResumeWithParams.

import (
	"context"

	"github.com/xalgord/xalgorix/v4/internal/approval"
	"github.com/xalgord/xalgorix/v4/internal/engine"
	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

type einoApprovalBroker struct {
	svc        *ApprovalService
	instanceID string
	sctx       *scanctx.ScanContext
	// secrets exposes the agent's operator-credential registry (HIGH-1):
	// bound after agent construction (scan_session.go), read at request time
	// because the values register during Run's prepareScanEnvironment.
	secrets func() []string
}

// BindSecrets wires the agent's secret-value registry into the broker so
// approval payloads are by-value redacted before persistence.
func (b *einoApprovalBroker) BindSecrets(fn func() []string) { b.secrets = fn }

func (b *einoApprovalBroker) secretValues() []string {
	if b.secrets == nil {
		return nil
	}
	return b.secrets()
}

var _ engine.ApprovalBroker = (*einoApprovalBroker)(nil)
var _ engine.ApprovalWaiter = (*einoApprovalBroker)(nil)

func (s *Server) einoApprovalBroker(instanceID string, sctx *scanctx.ScanContext) *einoApprovalBroker {
	svc := s.approvalService()
	if svc == nil || svc.policy.Mode != approval.ModeEnforce {
		return nil
	}
	return &einoApprovalBroker{svc: svc, instanceID: instanceID, sctx: sctx}
}

func (b *einoApprovalBroker) RequestApproval(_ context.Context, req engine.ApprovalRequest) (string, error) {
	verdict := b.svc.policy.Evaluate(req.ToolName, req.Args)
	if verdict.Decision == approval.Allow {
		return "", nil
	}
	row, err := b.svc.createPendingRow(b.instanceID, b.sctx, verdict, b.secretValues())
	if err != nil || row == nil {
		return "", err
	}
	return row.ID, nil
}

func (b *einoApprovalBroker) WaitApproval(ctx context.Context, approvalID string) (engine.ApprovalDecision, error) {
	row, err := b.svc.waitApproval(ctx, approvalID)
	if err != nil {
		return engine.ApprovalDecision{}, err
	}
	return engine.ApprovalDecision{
		Approved: row.Status == platform.ApprovalApproved,
		Status:   row.Status,
		Reason:   row.Reason,
	}, nil
}
