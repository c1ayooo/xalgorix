package web

import (
	"context"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// TestApprovalBrokerInstalledForObserveRun pins XALG016: the runtime must keep
// an approval adapter installed even when the run starts observe-only, so a
// later off→enforce hot switch applies to the next call. RequestApproval
// re-reads the run's effective mode per call.
func TestApprovalBrokerInstalledForObserveRun(t *testing.T) {
	s := &Server{}
	svc := &ApprovalService{srv: s}
	s.SetApprovalService(svc)

	sctx := scanctx.New("ctx-approval", "")
	sctx.AuditMode = "off"
	b := s.einoApprovalBroker("inst-approval", sctx)
	if b == nil {
		t.Fatal("observe-only run got no broker; an off→enforce hot switch could never gate existing workers")
	}
	id, err := b.RequestApproval(context.Background(), engine.ApprovalRequest{
		ToolName: "terminal_execute",
		Args:     map[string]string{"command": "id"},
	})
	if err != nil {
		t.Fatalf("observe-only approval: %v", err)
	}
	if id != "" {
		t.Fatalf("observe-only run created an approval row: %q", id)
	}
}
