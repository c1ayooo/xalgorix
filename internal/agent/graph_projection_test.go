package agent

import (
	"context"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestSyncExplorationGraphProjectsBeliefsFactsGoals(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	sctx := scanctx.New("scan-proj", "")
	sctx.SetInstanceID("run-proj")
	h := sctx.Ledger.Upsert(scanctx.Hypothesis{
		Title:      "SQLi on /api/users",
		VulnClass:  "sqli",
		Endpoint:   "/api/users",
		Status:     scanctx.HypothesisQueued,
		Confidence: 0.7,
		Origin:     "recon",
	})
	if !sctx.Ledger.AddEvidence(h.ID, scanctx.Evidence{
		Kind:       "probe",
		Summary:    "quote returned a SQL error",
		Nature:     scanctx.EvidenceObserved,
		Confidence: 0.8,
		AgentID:    "sub_1",
	}) {
		t.Fatal("add evidence failed")
	}

	ag := &Agent{state: NewScanState(), scanCtx: sctx}
	ag.state.ScanContextID = sctx.ID
	ag.state.Objective = &ObjectiveSpec{
		Statement: "Prove SQLi",
		Criteria: []ObjectiveCriterion{{
			ID: "criterion-1", Kind: "finding_class", VulnClass: "sqli",
			VerifyBy: "auto", Required: true,
		}},
	}
	ag.syncExplorationGraph()

	ctx := context.Background()
	beliefs, err := store.ListNodes(ctx, "run-proj", graph.KindBelief)
	if err != nil || len(beliefs) != 1 {
		t.Fatalf("beliefs = %d err=%v", len(beliefs), err)
	}
	belief := beliefs[0]
	if belief.UnitKey != h.ID || belief.State != graph.StateQueued {
		t.Fatalf("belief node = %+v", belief)
	}
	if belief.PayloadJSON()["hypothesis_id"] != h.ID {
		t.Fatalf("belief payload = %#v", belief.PayloadJSON())
	}

	facts, _ := store.ListNodes(ctx, "run-proj", graph.KindFact)
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(facts))
	}
	fact := facts[0]
	if fact.State != graph.StateConfirmed || fact.PayloadJSON()["nature"] != string(scanctx.EvidenceObserved) {
		t.Fatalf("fact node = %+v payload=%#v", fact, fact.PayloadJSON())
	}

	goals, _ := store.ListNodes(ctx, "run-proj", graph.KindGoal)
	if len(goals) != 1 {
		t.Fatalf("goals = %d, want 1", len(goals))
	}
	if goals[0].UnitKey != "criterion-1" || goals[0].State != graph.StateOpen {
		t.Fatalf("goal node = %+v", goals[0])
	}

	edges, _ := store.ListEdges(ctx, "run-proj")
	var supports bool
	for _, e := range edges {
		if e.Rel == graph.RelSupports && e.SrcID == fact.ID && e.DstID == belief.ID {
			supports = true
		}
	}
	if !supports {
		t.Fatalf("missing fact->belief supports edge: %+v", edges)
	}

	// A lifecycle change is reflected on the next projection, idempotently.
	if !sctx.Ledger.SetStatus(h.ID, scanctx.HypothesisProven, "proof recorded") {
		t.Fatal("set status failed")
	}
	ag.syncExplorationGraph()
	beliefs, _ = store.ListNodes(ctx, "run-proj", graph.KindBelief)
	if len(beliefs) != 1 || beliefs[0].State != graph.StateProven {
		t.Fatalf("after status change = %+v", beliefs)
	}
	units, _ := store.Units(ctx, "run-proj")
	var sawBelief bool
	for _, u := range units {
		if u.Kind == "belief" && u.UnitID == h.ID && u.State == string(graph.StateProven) {
			sawBelief = true
		}
	}
	if !sawBelief {
		t.Fatalf("units missing proven belief: %+v", units)
	}
}
