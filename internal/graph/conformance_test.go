package graph

import (
	"context"
	"testing"
	"time"
)

func node(id int64, kind Kind, state State, unit, payload string) Node {
	return Node{ID: id, ScanID: "c", Kind: kind, State: state, UnitKey: unit, Payload: payload}
}

func hasCode(rep Report, code string) bool {
	for _, v := range rep.Violations {
		if v.Code == code {
			return true
		}
	}
	return false
}

func TestValidateSnapshot_HealthyGraph(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		node(1, KindBelief, StateQueued, "H-1", `{"hypothesis_id":"H-1"}`),
		node(2, KindFact, StateConfirmed, "E-1", `{"evidence_id":"E-1"}`),
		node(3, KindIntent, StateDone, "T-1", `{"task_id":"T-1"}`),
		node(4, KindFact, StateOrigin, "intent-result:T-1", `{"result_of":"T-1"}`),
	}
	edges := []Edge{
		{SrcID: 2, DstID: 1, Rel: RelSupports},
		{SrcID: 3, DstID: 4, Rel: RelYields},
	}
	rep := ValidateSnapshot("c", nodes, edges, now)
	if !rep.OK || rep.Errors != 0 || rep.Warnings != 0 {
		t.Fatalf("healthy graph report = %+v", rep)
	}
}

func TestValidateSnapshot_DetectsDesignDrift(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-time.Minute)
	nodes := []Node{
		node(1, KindIntent, StateRunning, "T-running", `{"task_id":"T-running"}`),   // no owner/lease
		node(2, KindIntent, StateDone, "T-done", `{"task_id":"T-done"}`),            // no yields
		node(3, KindFact, StateConfirmed, "E-orphan", `{"evidence_id":"E-orphan"}`), // no provenance
		node(4, KindIntent, StateOpen, "T-open", `{}`),
		node(5, KindBelief, StateProven, "H-proven", `{}`), // no supporting fact
	}
	nodes[3].CompletedAt = &past
	// running lease present but expired to isolate the warning code
	nodes[0].Owner = "work#1"
	nodes[0].LeaseUntil = &past

	edges := []Edge{{SrcID: 99, DstID: 1, Rel: RelSupports}} // dangling source
	rep := ValidateSnapshot("c", nodes, edges, now)

	for _, code := range []string{
		CodeEdgeDangling, CodeIntentDoneNoYield, CodeFactNoProvenance,
		CodeIntentOpenComplete, CodeBeliefProvenNoFact, CodeIntentLeaseExpired,
	} {
		if !hasCode(rep, code) {
			t.Fatalf("missing %s in %+v", code, rep.Violations)
		}
	}
	if rep.OK {
		t.Fatal("report with errors must not be OK")
	}
	if rep.Errors == 0 {
		t.Fatalf("errors = 0, want > 0: %+v", rep)
	}
}

func TestValidateSnapshot_ContractRules(t *testing.T) {
	now := time.Now().UTC()
	missingPre := `{"contract":{"version":1,"preconditions":[
		{"ref_kind":"fact","ref":"cred:admin","need_state":"confirmed"}],
		"yields":[{"kind":"fact"}]}}`
	needsConfirm := `{"contract":{"version":1,"requires_confirmation":true,
		"yields":[{"kind":"fact"}]}}`
	nodes := []Node{
		node(1, KindIntent, StateRunning, "T-pre", missingPre),
		node(2, KindIntent, StateDone, "T-confirm", needsConfirm),
		node(3, KindFact, StateOrigin, "intent-result:T-confirm", `{}`),
	}
	nodes[0].Owner = "work#1"
	future := now.Add(time.Minute)
	nodes[0].LeaseUntil = &future
	edges := []Edge{{SrcID: 2, DstID: 3, Rel: RelYields}}

	rep := ValidateSnapshot("c", nodes, edges, now)
	if !hasCode(rep, CodePreconditionUnmet) {
		t.Fatalf("missing precondition violation: %+v", rep.Violations)
	}
	if !hasCode(rep, CodeConfirmMissing) {
		t.Fatalf("missing confirmation violation: %+v", rep.Violations)
	}
}

func TestCheckProjection_MissingAndStale(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sid := "proj"
	for _, unit := range []string{"H-1", "H-extra"} {
		if _, err := store.AddNode(ctx, NodeInput{
			ScanID: sid, Kind: KindBelief, UnitKey: unit, Payload: `{}`,
		}); err != nil {
			t.Fatalf("add %s: %v", unit, err)
		}
	}
	expected := map[Kind][]string{
		KindBelief: {"H-1", "H-2"},
		KindFact:   {"E-1"},
	}
	counts, violations, err := CheckProjection(ctx, store, sid, expected)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if len(counts.Missing) != 2 {
		t.Fatalf("missing = %v, want H-2 and E-1", counts.Missing)
	}
	if len(counts.Stale) != 1 || counts.Stale[0] != "H-extra" {
		t.Fatalf("stale = %v, want H-extra", counts.Stale)
	}
	var missing, stale int
	for _, v := range violations {
		switch v.Code {
		case CodeProjectionMissing:
			missing++
		case CodeProjectionStale:
			stale++
		}
	}
	if missing != 2 || stale != 1 {
		t.Fatalf("violations missing=%d stale=%d: %+v", missing, stale, violations)
	}
}

func TestCheckProjection_ExemptsIntentReceipts(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sid := "proj-receipt"
	if _, err := store.AddNode(ctx, NodeInput{
		ScanID: sid, Kind: KindFact, UnitKey: "intent-result:T-1",
		State: StateOrigin, Payload: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddNode(ctx, NodeInput{
		ScanID: sid, Kind: KindFact, UnitKey: "E-1", State: StateConfirmed, Payload: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
	counts, violations, err := CheckProjection(ctx, store, sid, map[Kind][]string{
		KindFact: {"E-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts.Stale) != 0 {
		t.Fatalf("receipt counted as stale: %v", counts.Stale)
	}
	for _, v := range violations {
		if v.Code == CodeProjectionStale {
			t.Fatalf("receipt produced stale violation: %+v", v)
		}
	}
}
