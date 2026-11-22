package web

// Eino approval broker: creates the ordinary R2 pending row and lets the Agent
// goroutine wait for the operator decision. The ADK runner checkpoints the
// interruption; after WaitApproval returns, the Agent calls ResumeWithParams.

import (
	"context"
	"errors"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

type einoApprovalBroker struct {
	svc        *ApprovalService
	instanceID string
	sctx       *scanctx.ScanContext
	// secrets exposes the agent's operator-credential registry (HIGH-1):
	// bound after agent construction (scan_session.go), read at request time
	// because the values register during Run's prepareScanEnvironment.
	secrets func() []string
	// mcpExempt snapshots this scan's MCP servers with require_approval=false
	// (fetched once at broker construction). The policy classifies all
	// mcp_* calls command-class; this set is the operator's explicit
	// per-server exemption. nil = no exemptions.
	mcpExempt map[string]bool
}

// mcpServerExempt reports whether the named server opted out of per-call
// approvals (require_approval=false at definition time).
func (b *einoApprovalBroker) mcpServerExempt(server string) bool {
	if b == nil || b.mcpExempt == nil || strings.TrimSpace(server) == "" {
		return false
	}
	return b.mcpExempt[server]
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
	return &einoApprovalBroker{svc: svc, instanceID: instanceID, sctx: sctx, mcpExempt: s.mcpApprovalExemptSet()}
}

// mcpApprovalExemptSet snapshots the names of enabled MCP servers whose
// require_approval is false (best-effort: store unavailable = no exemptions).
func (s *Server) mcpApprovalExemptSet() map[string]bool {
	store := s.currentPlatformStore()
	if store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), integrationInputTimeout)
	defer cancel()
	servers, err := store.ListMcpServers(ctx)
	if err != nil {
		return nil
	}
	var out map[string]bool
	for _, m := range servers {
		if !m.Enabled || m.RequireApproval {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[m.Name] = true
	}
	return out
}

func (b *einoApprovalBroker) RequestApproval(_ context.Context, req engine.ApprovalRequest) (string, error) {
	// Defensive: a typed-nil *einoApprovalBroker can still arrive as a non-nil
	// interface. "No broker" means approvals are off, i.e. allow without a row.
	if b == nil || b.svc == nil {
		return "", nil
	}
	// Per-server MCP approval exemption ("require_approval=false"): the
	// policy classifies every mcp_* call as command-class (it cannot know
	// the server's flag — it stays store-free), so the broker resolves the
	// operator's explicit exemption from the args' provenance.
	if strings.HasPrefix(req.ToolName, "mcp_") && b.mcpServerExempt(req.Args["mcp_server"]) {
		return "", nil
	}
	verdict := b.svc.policy.Evaluate(req.ToolName, req.Args)
	if verdict.Decision == approval.Allow {
		return "", nil
	}
	// Project fence first (scope_fence.go): a remembered rule may only exempt
	// calls that already sit inside the authorization boundary. The denial
	// row is terminal, so WaitApproval resolves it without waiting for the
	// operator and the model receives the ⛔ FENCE DENIAL text.
	if row, denied := b.svc.fenceDenial(b.instanceID, b.sctx, verdict, b.secretValues()); denied {
		if row == nil {
			return "", errors.New("project fence denied the call (audit row unavailable)")
		}
		return row.ID, nil
	}
	// Scope observer (v4.6): adjudicates the call against the bound project's
	// operator rules AND the built-in baseline. allow → no interrupt/no row
	// (approval_rule_hit is the audit projection); deny → a terminal
	// scope-observer row (the model receives the ⛔ denial text).
	switch obs := b.svc.observeScope(b.instanceID, verdict); obs.Decision {
	case scopeAllow:
		return "", nil
	case scopeDeny:
		row, err := b.svc.submitScopeDenial(b.instanceID, verdict, obs.Reason, b.secretValues())
		if err != nil || row == nil {
			return "", err
		}
		return row.ID, nil
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
