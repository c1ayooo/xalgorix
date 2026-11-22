package web

import (
	"context"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestEinoApprovalBrokerCreatesRowAndResumes(t *testing.T) {
	s, _ := newEnforceGateServer(t, 5, 1)
	sctx := scanctx.New("eino-broker-test", t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(func() { scanctx.Deactivate(sctx.ID) })

	broker := s.einoApprovalBroker(sctx.ID, sctx)
	if broker == nil {
		t.Fatal("enforce mode must expose the eino broker")
	}
	var waiter engine.ApprovalWaiter = broker // *einoApprovalBroker implements it
	id, err := broker.RequestApproval(context.Background(), engine.ApprovalRequest{
		ToolName: "terminal_execute",
		Args:     map[string]string{"command": "rm -rf /tmp/evidence"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("regulated command must create a pending approval")
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		if _, err := s.approvalService().Decide(context.Background(), id, "approved", "operator:test", "ok"); err != nil {
			t.Errorf("decide: %v", err)
		}
	}()
	decision, err := waiter.WaitApproval(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Approved || decision.Status != "approved" {
		t.Fatalf("decision = %+v", decision)
	}
}
