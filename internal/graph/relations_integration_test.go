package graph

// Relations integration acceptance: the subject-level project graph must expose
// cross-subject chain edges with real titles, exclude same-subject edges, and
// honour the observed-at window.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
)

func TestInfoSpaceRelationsAcrossSubjects(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	runID := scanID(t)
	ctxID := runID + "-ctx"
	cleanupInfoSpaceScan(t, store, runID, ctxID)

	addNode := func(kind Kind, subject, kindLabel, title string) *Node {
		t.Helper()
		payload := fmt.Sprintf(
			`{"target":%q,"vuln_class":"sqli","title":%q,`+
				`"info":{"subject":{"kind":%q,"identifier":%q},`+
				`"purpose":"sqli","nature":"observed","observed_at":"2026-11-24T01:00:00Z"}}`,
			subject, title, kindLabel, subject)
		node, err := store.AddNode(ctx, NodeInput{
			ScanID: runID, Kind: kind, State: DefaultState(kind), Priority: 1, Payload: payload,
		})
		if err != nil {
			t.Fatalf("add %s node: %v", kind, err)
		}
		return node
	}

	// Cross-subject chain: a fact on the pivot host supports a belief on the
	// target host. This is the shape the map renders as an attack/evidence edge.
	shell := addNode(KindFact, "10.0.1.5:8080", "container", "shell on 10.0.1.5")
	pivot := addNode(KindBelief, "/api", "endpoint", "pivot candidate")
	if err := store.AddEdge(ctx, EdgeInput{
		ScanID: runID, SrcID: shell.ID, DstID: pivot.ID, Rel: RelSupports,
	}); err != nil {
		t.Fatalf("add cross-subject edge: %v", err)
	}
	// Same-subject relation: must not appear in the subject-level graph.
	localFact := addNode(KindFact, "/api", "endpoint", "local evidence")
	localBelief := addNode(KindBelief, "/api", "endpoint", "local belief")
	if err := store.AddEdge(ctx, EdgeInput{
		ScanID: runID, SrcID: localFact.ID, DstID: localBelief.ID, Rel: RelSupports,
	}); err != nil {
		t.Fatalf("add same-subject edge: %v", err)
	}

	reader := infospace.RelationsReader(store)
	res, err := reader.Relations(ctx, infospace.RelationsQuery{
		RunIDs: []string{runID}, Limit: 50,
	})
	if err != nil {
		t.Fatalf("relations: %v", err)
	}
	if len(res.Edges) != 1 {
		t.Fatalf("edges = %+v", res.Edges)
	}
	edge := res.Edges[0]
	if edge.SrcSubject != "10.0.1.5:8080" || edge.SrcKind != "container" ||
		edge.DstSubject != "/api" || edge.DstKind != "endpoint" {
		t.Fatalf("edge endpoints = %+v", edge)
	}
	if edge.Rel != "supports" || edge.Category != infospace.RelationChain || edge.Count != 1 {
		t.Fatalf("edge = %+v", edge)
	}
	if edge.SrcTitle != "shell on 10.0.1.5" || edge.DstTitle != "pivot candidate" {
		t.Fatalf("titles = %+v", edge)
	}
	if edge.LastAt == nil || !edge.LastAt.Equal(time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("last_at = %v", edge.LastAt)
	}

	// Time window excludes both endpoints.
	after, err := time.Parse(time.RFC3339, "2026-11-24T02:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := reader.Relations(ctx, infospace.RelationsQuery{
		RunIDs: []string{runID}, ObservedAfter: &after,
	})
	if err != nil {
		t.Fatalf("time-filtered relations: %v", err)
	}
	if len(empty.Edges) != 0 {
		t.Fatalf("time-filtered edges = %+v", empty.Edges)
	}
}
