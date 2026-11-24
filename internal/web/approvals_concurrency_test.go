package web

// Concurrency regression: a multi-worker run queues for the per-run pending
// slot (MaxPending is 1 by default). The store deadline must start AFTER the
// slot is granted — otherwise a worker that waited on a human decision for
// longer than the budget fails its row creation with "context deadline
// exceeded" even though PostgreSQL is healthy.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// ctxCheckingRepo simulates the PostgreSQL driver's deadline handling for the
// one call the bug affected (the FileStore ignores ctx cancellation).
type ctxCheckingRepo struct {
	platform.Repository
}

func (c ctxCheckingRepo) CreateApproval(ctx context.Context, in platform.ApprovalInput) (*platform.Approval, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create approval: %w", err)
	}
	return c.Repository.CreateApproval(ctx, in)
}

func TestCreatePendingRowOpensStoreDeadlineAfterSlotWait(t *testing.T) {
	cfg := &config.Config{ApprovalMode: "enforce", ApprovalTimeoutSeconds: 30, ApprovalMaxPending: 1}
	s, store := newApprovalsTestServer(t, cfg)
	t.Cleanup(func() { trafficproxy.SetHighRiskGate(nil) })
	s.SetPlatformStore(ctxCheckingRepo{Repository: store})
	svc := s.approvalService()
	oldTimeout := svc.storeTimeout
	svc.storeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { svc.storeTimeout = oldTimeout })

	sctx := scanctx.New("slot-wait-scan", t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(func() {
		scanctx.Deactivate(sctx.ID)
		sctx.Close()
	})

	const instanceID = "slot-wait-inst"
	// Another worker already holds the run's only pending slot.
	svc.mu.Lock()
	svc.pendingCount[instanceID] = 1
	svc.mu.Unlock()

	type outcome struct {
		row *platform.Approval
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		row, err := svc.createPendingRow(instanceID, sctx, approval.Verdict{
			Decision: approval.Require,
			Kind:     approval.KindCommand,
			Reason:   "concurrency probe",
			Command:  "touch slot-wait",
		}, nil)
		done <- outcome{row: row, err: err}
	}()

	// Wait well past the store budget, then hand the slot over. With the old
	// ordering the context had already expired here and row creation failed.
	time.Sleep(3 * svc.storeTimeout)
	svc.releaseSlot(instanceID)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("createPendingRow after slot handoff: %v", got.err)
		}
		if got.row == nil || got.row.Status != platform.ApprovalPending {
			t.Fatalf("row = %+v, want a pending approval", got.row)
		}
		if _, err := svc.Decide(context.Background(), got.row.ID, platform.ApprovalRejected, "test", "cleanup"); err != nil {
			t.Fatalf("cleanup decision: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("createPendingRow never returned")
	}

	svc.mu.Lock()
	left := svc.pendingCount[instanceID]
	svc.mu.Unlock()
	if left != 0 {
		t.Fatalf("pending slots after cleanup = %d, want 0", left)
	}
}
