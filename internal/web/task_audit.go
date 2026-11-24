package web

// Task-level HITL/audit switch resolution.
//
// The task owns an audit_mode (inherit | off | enforce). At dispatch the
// effective value is snapshotted onto ScanRequest.AuditMode and then onto the
// run's ScanContext, so:
//   - the agent's tool gate (Eino approval broker) and
//   - the MITM traffic-proxy wire gate
// read the SAME per-run posture;
//   - editing the task or the process default never changes an in-flight run;
//   - runs without a task (manual/CLI) keep the process default.
//
// "enforce" means every regulated call creates an approval row and blocks
// until the operator decides (timeout = rejection). "off" is observe-only:
// the call proceeds and stays in the ordinary tool-call audit trail.

import (
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// effectiveTaskAuditMode resolves a task's switch against the process default
// (XALGORIX_APPROVAL_MODE). Only "off" and "enforce" are returned — the
// inherit state is always collapsed here so the run carries one concrete
// posture.
func effectiveTaskAuditMode(task *platform.Task, cfg *config.Config) string {
	mode := platform.TaskAuditInherit
	if task != nil {
		if normalized := platform.NormalizeTaskAuditMode(task.AuditMode); normalized != "" {
			mode = normalized
		}
	}
	switch mode {
	case platform.TaskAuditEnforce:
		return platform.TaskAuditEnforce
	case platform.TaskAuditOff:
		return platform.TaskAuditOff
	}
	if cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.ApprovalMode), string(approval.ModeEnforce)) {
		return platform.TaskAuditEnforce
	}
	return platform.TaskAuditOff
}

// runAuditMode is the effective gate posture for one live scan context. The
// session snapshot wins; an empty/unknown value (CLI runs, pre-feature
// sessions) falls back to the process default.
func (s *Server) runAuditMode(sctx *scanctx.ScanContext) approval.Mode {
	if s == nil {
		return approval.ModeOff
	}
	if sctx != nil {
		switch strings.ToLower(strings.TrimSpace(sctx.AuditMode)) {
		case platform.TaskAuditEnforce:
			return approval.ModeEnforce
		case platform.TaskAuditOff:
			return approval.ModeOff
		}
	}
	if svc := s.approvalService(); svc != nil {
		return svc.policy.Mode
	}
	return approval.ModeOff
}
