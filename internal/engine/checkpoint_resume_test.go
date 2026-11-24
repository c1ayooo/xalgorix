package engine

import (
	"bytes"
	"context"
	"encoding/gob"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// newApprovalCheckpointRuntime builds a runtime whose approval tool interrupts
// on its one tool call, so Run writes a durable approval checkpoint.
func newApprovalCheckpointRuntime(t *testing.T, store adk.CheckPointStore, client ChatClient) *Runtime {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{
		Name: "danger", Description: "danger",
		Parameters: []tools.Parameter{{Name: "x", Required: true}},
		Execute: func(map[string]string) (tools.Result, error) {
			return tools.Result{Success: true, Output: "executed"}, nil
		},
	})
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: client, Registry: reg, CheckpointStore: store,
		Instruction: "test", MaxIterations: 5, Approvals: &fixedApprovalBroker{},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

// TestInspectApprovalCheckpointRoundTrip proves the durable runner checkpoint
// is structurally decodable without importing ADK's unexported serialization:
// the target it yields is exactly the live interrupt target.
func TestInspectApprovalCheckpointRoundTrip(t *testing.T) {
	store := NewMemoryCheckpointStore()
	rt := newApprovalCheckpointRuntime(t, store, &scriptedClient{})
	const cpID = "scan-1:agent:sub-1"

	events := drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, cpID))
	targetID, _ := interruptedTarget(events)
	if targetID == "" {
		t.Fatalf("no interrupt emitted: %+v", events)
	}

	payload, existed, err := store.Get(context.Background(), cpID)
	if err != nil || !existed {
		t.Fatalf("checkpoint missing: existed=%v err=%v", existed, err)
	}
	cp, ok := InspectApprovalCheckpoint(payload)
	if !ok {
		t.Fatal("InspectApprovalCheckpoint rejected a valid approval checkpoint")
	}
	if cp.TargetID != targetID {
		t.Fatalf("target = %q, want the live interrupt target %q", cp.TargetID, targetID)
	}
	if cp.ApprovalID != "approval-1" || cp.ToolName != "danger" {
		t.Fatalf("approval identity = %+v, want approval-1/danger", cp)
	}
}

func TestInspectApprovalCheckpointRejectsUnsupported(t *testing.T) {
	// A decodable struct that carries no approval interrupt.
	var noApproval bytes.Buffer
	if err := gob.NewEncoder(&noApproval).Encode(&approvalCheckpointWire{}); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	cases := map[string][]byte{
		"nil":         nil,
		"empty":       {},
		"not gob":     []byte("this is not a checkpoint payload"),
		"no approval": noApproval.Bytes(),
	}
	for name, payload := range cases {
		if _, ok := InspectApprovalCheckpoint(payload); ok {
			t.Fatalf("%s: unsupported payload reported resumable", name)
		}
	}
}

func TestRuntimeHasCheckpoint(t *testing.T) {
	store := NewMemoryCheckpointStore()
	rt := newApprovalCheckpointRuntime(t, store, &scriptedClient{})
	ctx := context.Background()

	got, err := rt.HasCheckpoint(ctx, "missing")
	if err != nil || got {
		t.Fatalf("missing checkpoint = (%v,%v), want (false,nil)", got, err)
	}
	if err := store.Set(ctx, "present", []byte("payload")); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	got, err = rt.HasCheckpoint(ctx, "present")
	if err != nil || !got {
		t.Fatalf("present checkpoint = (%v,%v), want (true,nil)", got, err)
	}

	var nilRuntime *Runtime
	if got, err := nilRuntime.HasCheckpoint(ctx, "present"); err != nil || got {
		t.Fatalf("nil runtime = (%v,%v), want (false,nil)", got, err)
	}
}

// TestResumeUnsupportedCheckpointReturnsErrorWithoutPanic pins the ADK load
// path: a present-but-garbage checkpoint fails cleanly and never starts a run.
func TestResumeUnsupportedCheckpointReturnsErrorWithoutPanic(t *testing.T) {
	store := NewMemoryCheckpointStore()
	rt := newApprovalCheckpointRuntime(t, store, &scriptedClient{})
	ctx := context.Background()
	if err := store.Set(ctx, "bad", []byte("garbage")); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	iter, err := rt.Resume(ctx, "bad", "agent:x", ApprovalDecision{})
	if err == nil {
		t.Fatal("resume of an unsupported checkpoint returned no error")
	}
	if iter != nil {
		t.Fatal("resume of an unsupported checkpoint returned an iterator")
	}
}
