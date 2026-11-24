package graph

import (
	"context"
	"errors"
	"testing"
	"time"
)

func addIntentNode(t *testing.T, store *MemoryStore, sid, unit, payload string) *Node {
	t.Helper()
	n, err := store.AddNode(context.Background(), NodeInput{
		ScanID: sid, Kind: KindIntent, UnitKey: unit, State: StateOpen,
		Priority: 10, Payload: payload,
	})
	if err != nil {
		t.Fatalf("add intent %s: %v", unit, err)
	}
	return n
}

func TestConcludeIntent_WritesYieldAndReceipt(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sid := "conclude"
	intent := addIntentNode(t, store, sid, "T-1", `{"task_id":"T-1"}`)
	claimed, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %+v", err, claimed)
	}

	done, err := store.ConcludeIntent(ctx, sid, intent.ID, "work#1", StateDone, []NodeInput{{
		ScanID: sid, Kind: KindFact, State: StateOrigin,
		UnitKey: "intent-result:T-1", Payload: `{"result_of":"T-1"}`,
	}})
	if err != nil {
		t.Fatalf("conclude: %v", err)
	}
	if done.State != StateDone || done.Owner != "" || done.LeaseUntil != nil || done.CompletedAt == nil {
		t.Fatalf("concluded intent = %+v", done)
	}
	edges, _ := store.ListEdges(ctx, sid)
	if len(edges) != 1 || edges[0].Rel != RelYields || edges[0].SrcID != intent.ID {
		t.Fatalf("edges = %+v", edges)
	}
	nodes, _ := store.ListNodes(ctx, sid, KindFact)
	if len(nodes) != 1 || nodes[0].UnitKey != "intent-result:T-1" || nodes[0].State != StateOrigin {
		t.Fatalf("produced facts = %+v", nodes)
	}
	rep, err := ValidateStore(ctx, store, sid)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !rep.OK {
		t.Fatalf("concluded graph not conformant: %+v", rep.Violations)
	}

	if _, err := store.ConcludeIntent(ctx, sid, intent.ID, "work#1", StateDone, []NodeInput{{
		ScanID: sid, Kind: KindFact, State: StateOrigin,
		UnitKey: "intent-result:T-1", Payload: `{}`,
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second conclusion err = %v, want ErrConflict", err)
	}
}

func TestConcludeIntent_RequiresProduct(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sid := "no-product"
	intent := addIntentNode(t, store, sid, "T-1", `{}`)
	if _, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.ConcludeIntent(ctx, sid, intent.ID, "work#1", StateDone, nil); !errors.Is(err, ErrNoProduct) {
		t.Fatalf("err = %v, want ErrNoProduct", err)
	}
}

func TestConcludeIntent_RechecksPreconditions(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sid := "pre"
	if _, err := store.AddNode(ctx, NodeInput{
		ScanID: sid, Kind: KindFact, UnitKey: "cred:admin", State: StateConfirmed, Payload: `{}`,
	}); err != nil {
		t.Fatalf("add precondition: %v", err)
	}
	payload := `{"contract":{"version":1,"preconditions":[
		{"ref_kind":"fact","ref":"cred:admin","need_state":"confirmed"}],
		"yields":[{"kind":"fact"}]}}`
	intent := addIntentNode(t, store, sid, "T-1", payload)
	claimed, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim with satisfied precondition: %v %+v", err, claimed)
	}
	// The credential is rejected while the worker is in flight.
	if _, err := store.UpsertNode(ctx, NodeInput{
		ScanID: sid, Kind: KindFact, UnitKey: "cred:admin", State: StateDismissed, Payload: `{}`,
	}); err != nil {
		t.Fatalf("reject precondition: %v", err)
	}
	if _, err := store.ConcludeIntent(ctx, sid, intent.ID, "work#1", StateDone, []NodeInput{{
		ScanID: sid, Kind: KindFact, UnitKey: "intent-result:T-1", Payload: `{}`,
	}}); !errors.Is(err, ErrPreconditionUnmet) {
		t.Fatalf("err = %v, want ErrPreconditionUnmet", err)
	}
}

func TestConcludePlanned_DoesNotStealLiveWorker(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sid := "planned"
	intent := addIntentNode(t, store, sid, "T-1", `{}`)
	if _, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.ConcludePlanned(ctx, sid, intent.ID, StateDone, []NodeInput{{
		ScanID: sid, Kind: KindFact, UnitKey: "intent-result:T-1", Payload: `{}`,
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict for a live worker lease", err)
	}
}

func TestClaim_EnforcesConfirmationAndPreconditions(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sid := "gate"
	unconfirmed := `{"contract":{"version":1,"requires_confirmation":true,"yields":[{"kind":"fact"}]}}`
	addIntentNode(t, store, sid, "T-unconfirmed", unconfirmed)
	if got, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil || got != nil {
		t.Fatalf("unconfirmed claim = %+v err=%v, want nil", got, err)
	}

	missing := `{"contract":{"version":1,"preconditions":[
		{"ref_kind":"fact","ref":"nope","need_state":"confirmed"}],"yields":[{"kind":"fact"}]}}`
	addIntentNode(t, store, sid, "T-missing-pre", missing)
	if got, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil || got != nil {
		t.Fatalf("missing-precondition claim = %+v err=%v, want nil", got, err)
	}

	addIntentNode(t, store, sid, "T-malformed", `{"contract":"not-an-object"}`)
	if got, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil || got != nil {
		t.Fatalf("malformed-contract claim = %+v err=%v, want nil", got, err)
	}
}

func TestSetBlockedReason(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sid := "blocked-reason"
	intent := addIntentNode(t, store, sid, "T-1", `{}`)
	if _, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if ok, err := store.Transition(ctx, sid, intent.ID, KindIntent, StateRunning, StateBlocked); err != nil || !ok {
		t.Fatalf("transition: ok=%v err=%v", ok, err)
	}
	if err := store.SetBlockedReason(ctx, sid, intent.ID, "placeholder target"); err != nil {
		t.Fatalf("set reason: %v", err)
	}
	got, _ := store.GetNode(ctx, sid, intent.ID)
	if got.BlockedReason != "placeholder target" {
		t.Fatalf("blocked reason = %q", got.BlockedReason)
	}
}
