package graph

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPGConcludeIntent_ReceiptAndDoubleConclude(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	sid := scanID(t)
	cleanupScan(t, store, sid)

	intent, err := store.AddNode(ctx, NodeInput{
		ScanID: sid, Kind: KindIntent, UnitKey: "T-1",
		Payload: `{"task_id":"T-1"}`,
	})
	if err != nil {
		t.Fatalf("add intent: %v", err)
	}
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
		t.Fatalf("concluded = %+v", done)
	}
	edges, err := store.ListEdges(ctx, sid)
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	if len(edges) != 1 || edges[0].Rel != RelYields || edges[0].SrcID != intent.ID {
		t.Fatalf("edges = %+v", edges)
	}
	if rep, err := ValidateStore(ctx, store, sid); err != nil || !rep.OK {
		t.Fatalf("conformance = %+v err=%v", rep, err)
	}
	if _, err := store.ConcludeIntent(ctx, sid, intent.ID, "work#1", StateDone, []NodeInput{{
		ScanID: sid, Kind: KindFact, State: StateOrigin,
		UnitKey: "intent-result:T-1", Payload: `{}`,
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second conclude err = %v, want ErrConflict", err)
	}
}

func TestPGClaim_EnforcesContractAndConfirmations(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	sid := scanID(t)
	cleanupScan(t, store, sid)

	add := func(unit, payload string, priority int) *Node {
		n, err := store.AddNode(ctx, NodeInput{
			ScanID: sid, Kind: KindIntent, UnitKey: unit, Priority: priority, Payload: payload,
		})
		if err != nil {
			t.Fatalf("add %s: %v", unit, err)
		}
		return n
	}
	add("T-unconfirmed", `{"task_id":"T-unconfirmed","contract":{"version":1,"requires_confirmation":true,"yields":[{"kind":"fact"}]}}`, 100)
	add("T-missing-pre", `{"task_id":"T-missing-pre","contract":{"version":1,"preconditions":[{"ref_kind":"fact","ref":"cred:admin","need_state":"confirmed"}],"yields":[{"kind":"fact"}]}}`, 90)
	add("T-malformed", `{"task_id":"T-malformed","contract":"not-an-object"}`, 80)

	if got, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil || got != nil {
		t.Fatalf("claim with gated intents = %+v err=%v, want nil", got, err)
	}

	if _, err := store.AddNode(ctx, NodeInput{
		ScanID: sid, Kind: KindFact, UnitKey: "cred:admin", State: StateConfirmed, Payload: `{}`,
	}); err != nil {
		t.Fatalf("add precondition fact: %v", err)
	}
	got, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute)
	if err != nil {
		t.Fatalf("claim after precondition: %v", err)
	}
	if got == nil || got.UnitKey != "T-missing-pre" {
		t.Fatalf("claimed = %+v, want T-missing-pre", got)
	}
}

func TestPGConcludePlanned_ReceiptAndLiveWorkerGuard(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	sid := scanID(t)
	cleanupScan(t, store, sid)

	planned, err := store.AddNode(ctx, NodeInput{
		ScanID: sid, Kind: KindIntent, UnitKey: "T-plan", Payload: `{"task_id":"T-plan"}`,
	})
	if err != nil {
		t.Fatalf("add planned: %v", err)
	}
	if _, err := store.ConcludePlanned(ctx, sid, planned.ID, StateDone, []NodeInput{{
		ScanID: sid, Kind: KindFact, State: StateOrigin,
		UnitKey: "intent-result:T-plan", Origin: "plan", Payload: `{}`,
	}}); err != nil {
		t.Fatalf("planned conclude: %v", err)
	}
	if rep, err := ValidateStore(ctx, store, sid); err != nil || !rep.OK {
		t.Fatalf("conformance = %+v err=%v", rep, err)
	}

	live, err := store.AddNode(ctx, NodeInput{
		ScanID: sid, Kind: KindIntent, UnitKey: "T-live", Payload: `{"task_id":"T-live"}`,
	})
	if err != nil {
		t.Fatalf("add live: %v", err)
	}
	if _, err := store.ClaimNextIntent(ctx, sid, "work#1", time.Minute); err != nil {
		t.Fatalf("claim live: %v", err)
	}
	if _, err := store.ConcludePlanned(ctx, sid, live.ID, StateDone, []NodeInput{{
		ScanID: sid, Kind: KindFact, State: StateOrigin,
		UnitKey: "intent-result:T-live", Payload: `{}`,
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("planned conclude on live worker err = %v, want ErrConflict", err)
	}
}
