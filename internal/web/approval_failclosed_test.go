package web

import (
	"context"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// TestApprovalSubmitFailsClosedUnderEnforce pins the decided XALG017 posture:
// enforce + no approval store refuses the regulated call; observe/off keeps the
// old availability behavior.
func TestApprovalSubmitFailsClosedUnderEnforce(t *testing.T) {
	s := &Server{}
	svc := &ApprovalService{srv: s}
	sctx := scanctx.New("ctx-failclosed", "")
	verdict := approval.Verdict{Decision: approval.Require, Kind: approval.KindCommand}

	sctx.AuditMode = "enforce"
	if out := svc.submit("inst-failclosed", sctx, verdict, nil); out.Approved {
		t.Fatalf("enforce without a store approved a regulated call: %+v", out)
	}
	sctx.AuditMode = "off"
	if out := svc.submit("inst-failclosed", sctx, verdict, nil); !out.Approved {
		t.Fatalf("observe/off without a store no longer fails open: %+v", out)
	}
}

func TestApprovalBrokerFailsClosedUnderEnforceWithoutStore(t *testing.T) {
	s := &Server{}
	svc := &ApprovalService{srv: s}
	s.SetApprovalService(svc)
	sctx := scanctx.New("ctx-broker-failclosed", "")
	sctx.AuditMode = "enforce"
	b := s.einoApprovalBroker("inst-broker-failclosed", sctx)
	if b == nil {
		t.Fatal("enforce run got no broker")
	}
	id, err := b.RequestApproval(context.Background(), engine.ApprovalRequest{
		ToolName: "terminal_execute",
		Args:     map[string]string{"command": "rm -rf /tmp/xalgorix-failclosed-probe"},
	})
	if err == nil {
		t.Fatalf("enforce without a store allowed a regulated call (id=%q)", id)
	}
}
