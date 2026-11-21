package web

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

// utf8ReplacementChar mirrors utf8.RuneError for the validity probe.
const utf8ReplacementChar = utf8.RuneError

// TestReconcilePendingStartup pins the ghost-row cleanup: rows from a previous
// process (no live waiter can exist) flip at startup — deadline elapsed →
// timeout (decided_by=timeout), everything else → cancelled
// (decided_by=system, reason "startup reconciliation"). A second call is a
// no-op (one-shot guard).
func TestReconcilePendingStartup(t *testing.T) {
	s, store := newApprovalsTestServer(t, &config.Config{})
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Seed rows FIRST, then a FRESH service (the constructor built one that
	// already reconciled the empty store during SetPlatformStore wiring).
	expired := seedApproval(t, store, project.ID, "", "inst-dead",
		platform.ApprovalKindCommand, time.Now().Add(-time.Minute).Format(time.RFC3339))
	fresh := seedApproval(t, store, project.ID, "", "inst-ghost",
		platform.ApprovalKindHTTP, time.Now().Add(time.Hour).Format(time.RFC3339))
	svc := newApprovalService(s)
	s.SetApprovalService(svc)

	svc.ReconcilePending()

	got, err := store.GetApproval(ctx, expired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != platform.ApprovalTimeout || got.DecidedBy != "timeout" || got.DecidedAt == nil {
		t.Errorf("expired row = %+v, want timeout decided_by=timeout", got)
	}
	got, err = store.GetApproval(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != platform.ApprovalCancelled || got.DecidedBy != "system" || got.Reason != "startup reconciliation" {
		t.Errorf("orphan row = %+v, want cancelled decided_by=system reason=startup reconciliation", got)
	}

	// One-shot: a second reconcile must not touch rows again.
	rowsAfter, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending"})
	svc.ReconcilePending()
	rowsAgain, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending"})
	if len(rowsAfter) != 0 || len(rowsAgain) != 0 {
		t.Errorf("reconcile left %d/%d pending rows", len(rowsAfter), len(rowsAgain))
	}

	// pending_total is clean (no OpsPulse pollution).
	summary, err := store.PendingSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 0 {
		t.Errorf("pending_total after reconcile = %d, want 0", summary.Total)
	}
}

// ── MEDIUM-3: a wedged store must fail the gate open ─────────────────────────

// blockingStore wraps a real store and blocks CreateApproval until its
// context dies — the "PostgreSQL black hole" scenario.
type blockingStore struct {
	platform.Repository
	release chan struct{}
}

func (b *blockingStore) CreateApproval(ctx context.Context, in platform.ApprovalInput) (*platform.Approval, error) {
	select {
	case <-b.release:
		return b.Repository.CreateApproval(ctx, in)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestGateFailsOpenWhenStoreBlocks pins the charter §6.1 posture: a store
// call that wedges must fail the gate OPEN within the store-call deadline —
// never park the tool call until the operator timeout.
func TestGateFailsOpenWhenStoreBlocks(t *testing.T) {
	s, store := newEnforceGateServer(t, 600, 1)
	original := approvalStoreCallTimeout
	approvalStoreCallTimeout = 150 * time.Millisecond
	t.Cleanup(func() { approvalStoreCallTimeout = original })

	release := make(chan struct{})
	s.SetPlatformStore(&blockingStore{Repository: store, release: release})
	t.Cleanup(func() { close(release) })

	registerGateInstance(t, s, "inst-blocked")
	sctx := scanctx.New("gate-blocked", t.TempDir())

	start := time.Now()
	outcome := gateServiceFor(t, s).submit("inst-blocked", sctx, commandVerdict("nmap -sV target"), nil)
	if !outcome.Approved {
		t.Fatalf("wedged store must fail OPEN, got %+v", outcome)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("fail-open took %s, want ~%s", elapsed, approvalStoreCallTimeout)
	}
	// The reserved slot was released on the failed create — a second submit
	// proceeds immediately instead of queueing behind a phantom slot.
	outcome2 := gateServiceFor(t, s).submit("inst-blocked", sctx, commandVerdict("dig example.com"), nil)
	if !outcome2.Approved {
		t.Fatalf("second submit after fail-open must also proceed (slot released), got %+v", outcome2)
	}
}

// ── L3: rune-safe byte-bounded truncation ────────────────────────────────────

func TestTruncateRunesByteBound(t *testing.T) {
	// 5000 CJK code points = 15000 bytes; the excerpt must stay ≤2048 BYTES
	// (not ~6144 like a code-point cut) and remain valid UTF-8.
	cjk := strings.Repeat("字", 5000)
	got := truncateRunes(cjk, maxApprovalBodyBytes)
	if len(got) > maxApprovalBodyBytes {
		t.Errorf("CJK excerpt = %d bytes, want ≤%d (byte bound, not code points)", len(got), maxApprovalBodyBytes)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncation marker missing")
	}
	for _, r := range got {
		if r == utf8ReplacementChar {
			t.Fatalf("truncation produced invalid UTF-8: %q", got[len(got)-20:])
		}
	}
	// ASCII long input: bound and marker hold.
	ascii := strings.Repeat("A", 5000)
	got = truncateRunes(ascii, maxApprovalBodyBytes)
	if len(got) != maxApprovalBodyBytes {
		t.Errorf("ASCII excerpt = %d bytes, want exactly %d", len(got), maxApprovalBodyBytes)
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("ASCII truncation marker missing")
	}
	// Short input passes through untouched.
	if got := truncateRunes("short", maxApprovalBodyBytes); got != "short" {
		t.Errorf("short input changed: %q", got)
	}
	// Tiny ceiling stays byte-bounded.
	if got := truncateRunes("字字字", 4); len(got) > 4 {
		t.Errorf("tiny ceiling excerpt = %d bytes, want ≤4", len(got))
	}
}
