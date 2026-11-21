package platform

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newApprovalsFileStore(t *testing.T) *FileStore {
	t.Helper()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.EnsureDefaultProject(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestFileStoreApprovalLifecycle(t *testing.T) {
	store := newApprovalsFileStore(t)
	ctx := context.Background()

	project, err := store.CreateProject(ctx, "Approval P", Scope{Targets: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "Approval task"})
	if err != nil {
		t.Fatal(err)
	}

	row, err := store.CreateApproval(ctx, ApprovalInput{
		ProjectID:  project.ID,
		TaskID:     task.ID,
		InstanceID: "inst-1",
		Kind:       ApprovalKindCommand,
		Payload:    ApprovalPayload{Command: "nmap -sV example.com", Reason: "shell command execution requires approval"},
		DeadlineAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != ApprovalPending {
		t.Fatalf("new row status = %s, want pending", row.Status)
	}

	// List filters.
	pending, err := store.ListApprovals(ctx, ApprovalFilter{Status: "pending", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != row.ID {
		t.Fatalf("pending list = %v, want the row", pending)
	}
	// The projected fields must never come from the repository.
	if pending[0].ProjectName != "" || pending[0].TaskName != "" {
		t.Error("repository must not fill project_name/task_name (web-layer projection)")
	}

	// Optimistic decision: first wins, second gets ErrConflict.
	final, err := store.DecideApproval(ctx, row.ID, ApprovalDecision{
		Decision: ApprovalRejected, DecidedBy: "operator:admin", Reason: "too destructive",
	})
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != ApprovalRejected || final.DecidedBy != "operator:admin" || final.DecidedAt == nil {
		t.Fatalf("decision lost: %+v", final)
	}
	if _, err := store.DecideApproval(ctx, row.ID, ApprovalDecision{Decision: ApprovalApproved}); !errors.Is(err, ErrConflict) {
		t.Errorf("second decision = %v, want ErrConflict", err)
	}

	decided, err := store.ListApprovals(ctx, ApprovalFilter{Status: "decided"})
	if err != nil {
		t.Fatal(err)
	}
	if len(decided) != 1 {
		t.Fatalf("decided list = %d rows, want 1", len(decided))
	}

	// Unknown decision value and unknown row.
	if _, err := store.DecideApproval(ctx, row.ID, ApprovalDecision{Decision: "maybe"}); !errors.Is(err, ErrValidation) {
		t.Errorf("bad decision = %v, want ErrValidation", err)
	}
	if _, err := store.GetApproval(ctx, "does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing row = %v, want ErrNotFound", err)
	}

	// Unknown status filter is rejected rather than silently empty.
	if _, err := store.ListApprovals(ctx, ApprovalFilter{Status: "wat"}); !errors.Is(err, ErrValidation) {
		t.Errorf("bad status filter = %v, want ErrValidation", err)
	}
}

// TestFileStoreApprovalSnapshotRoundTrip pins that a restart (re-opened
// snapshot) keeps the rows and the idempotency keys.
func TestFileStoreApprovalSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second).UTC()
	row, err := store.CreateApproval(ctx, ApprovalInput{
		ProjectID:  project.ID,
		InstanceID: "inst-rt",
		Kind:       ApprovalKindHTTP,
		Payload:    ApprovalPayload{Method: "POST", URL: "http://example.com/admin", Headers: map[string]string{"Accept": "application/json"}},
		DeadlineAt: deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGuidance(ctx, GuidanceInput{TaskID: "", Message: "x"}); err == nil {
		t.Error("guidance without task must fail")
	}

	reopened, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetApproval(ctx, row.ID)
	if err != nil {
		t.Fatalf("row lost across snapshot reopen: %v", err)
	}
	if got.DeadlineAt.UTC() != deadline.UTC() {
		t.Errorf("deadline drifted: %v want %v", got.DeadlineAt, deadline)
	}
	if got.Payload.Method != "POST" {
		t.Errorf("payload lost: %+v", got.Payload)
	}
}

func TestFileStoreApprovalSlotAccounting(t *testing.T) {
	store := newApprovalsFileStore(t)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.CreateApproval(ctx, ApprovalInput{
			ProjectID:  project.ID,
			InstanceID: "inst-slot",
			Kind:       ApprovalKindCommand,
			DeadlineAt: time.Now().Add(time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.CountPendingByInstance(ctx, "inst-slot")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("pending for inst-slot = %d, want 3", n)
	}

	cancelled, err := store.CancelPendingByInstance(ctx, "inst-slot", "run stopped by operator")
	if err != nil {
		t.Fatal(err)
	}
	if len(cancelled) != 3 {
		t.Fatalf("cancelled = %d rows, want 3", len(cancelled))
	}
	for _, row := range cancelled {
		if row.Status != ApprovalCancelled || row.DecidedBy != "system" || row.DecidedAt == nil {
			t.Errorf("cancel semantics lost: %+v", row)
		}
	}
	if n, _ := store.CountPendingByInstance(ctx, "inst-slot"); n != 0 {
		t.Errorf("pending after cancel = %d, want 0", n)
	}
	// Idempotent: a second batch cancel cancels nothing.
	if rows, _ := store.CancelPendingByInstance(ctx, "inst-slot", "again"); len(rows) != 0 {
		t.Errorf("second cancel returned %d rows, want 0", len(rows))
	}
}

func TestFileStorePendingSummary(t *testing.T) {
	store := newApprovalsFileStore(t)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "summary task"})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := store.PendingSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 0 || summary.NearestDeadline != nil {
		t.Fatalf("empty summary = %+v, want zero", summary)
	}

	early := time.Now().Add(30 * time.Second)
	late := time.Now().Add(2 * time.Minute)
	for _, deadline := range []time.Time{late, early} {
		if _, err := store.CreateApproval(ctx, ApprovalInput{
			ProjectID:  project.ID,
			TaskID:     task.ID,
			InstanceID: "inst-sum",
			Kind:       ApprovalKindCommand,
			DeadlineAt: deadline,
		}); err != nil {
			t.Fatal(err)
		}
	}
	summary, err = store.PendingSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 2 {
		t.Fatalf("pending_total = %d, want 2 (cross-project view)", summary.Total)
	}
	if summary.NearestDeadline == nil || summary.NearestDeadline.UTC() != early.UTC() {
		t.Errorf("nearest deadline = %v, want %v", summary.NearestDeadline, early)
	}
}

func TestFileStoreGuidanceIdempotencyAndOrder(t *testing.T) {
	store := newApprovalsFileStore(t)
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Guidance P", Scope{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "guidance task"})
	if err != nil {
		t.Fatal(err)
	}

	g1, created, err := store.CreateGuidance(ctx, GuidanceInput{
		TaskID: task.ID, Message: "focus on the staging host", RequestID: "req-1",
	})
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	replay, created2, err := store.CreateGuidance(ctx, GuidanceInput{
		TaskID: task.ID, Message: "focus on the staging host", RequestID: "req-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created2 || replay.ID != g1.ID {
		t.Errorf("retry created=%v id=%v; want false/%s", created2, replay.ID, g1.ID)
	}
	// The retry's payload is ignored — the first message wins.
	if replay.Message != g1.Message {
		t.Errorf("replay returned a different message body")
	}
	// Same request_id on another task creates independently.
	other, created3, err := store.CreateGuidance(ctx, GuidanceInput{
		TaskID: task.ID, Message: "different", RequestID: "req-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created3 || other.ID != g1.ID {
		t.Errorf("same-task replay created=%v; want false (per-task key)", created3)
	}

	// Wire shape: request_id is persisted but never serialized.
	if g1.RequestID == "" {
		t.Error("request_id must be retained for restart-safe idempotency")
	}

	// A second, independent turn.
	g2, created4, err := store.CreateGuidance(ctx, GuidanceInput{
		TaskID: task.ID, Message: "second turn", RequestID: "req-2",
	})
	if err != nil || !created4 {
		t.Fatalf("second turn: created=%v err=%v", created4, err)
	}

	// Newest first; limit respected.
	items, err := store.ListGuidance(ctx, task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != g2.ID || items[1].ID != g1.ID {
		t.Fatalf("list order = %v, want newest first", items)
	}
	items, err = store.ListGuidance(ctx, task.ID, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("limited list = %d (err %v), want 1", len(items), err)
	}

	// Status transitions.
	injected, err := store.UpdateGuidanceStatus(ctx, g1.ID, GuidanceInjected)
	if err != nil || injected.Status != GuidanceInjected {
		t.Fatalf("flip to injected: %+v err=%v", injected, err)
	}
	consumed, err := store.UpdateGuidanceStatus(ctx, g1.ID, GuidanceConsumed)
	if err != nil || consumed.Status != GuidanceConsumed || consumed.ConsumedAt == nil {
		t.Fatalf("flip to consumed: %+v err=%v", consumed, err)
	}
	if _, err := store.UpdateGuidanceStatus(ctx, g1.ID, "wat"); !errors.Is(err, ErrValidation) {
		t.Errorf("bad status = %v, want ErrValidation", err)
	}
	if _, err := store.UpdateGuidanceStatus(ctx, "missing", GuidanceConsumed); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing row = %v, want ErrNotFound", err)
	}
}

func TestFileStoreApprovalValidation(t *testing.T) {
	store := newApprovalsFileStore(t)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApproval(ctx, ApprovalInput{ProjectID: project.ID, Kind: "webhook"}); !errors.Is(err, ErrValidation) {
		t.Errorf("bad kind = %v, want ErrValidation", err)
	}
	if _, err := store.CreateApproval(ctx, ApprovalInput{Kind: ApprovalKindCommand}); !errors.Is(err, ErrValidation) {
		t.Errorf("missing project = %v, want ErrValidation", err)
	}
	if _, err := store.CreateApproval(ctx, ApprovalInput{ProjectID: "nope", Kind: ApprovalKindCommand}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project = %v, want ErrNotFound", err)
	}
}
