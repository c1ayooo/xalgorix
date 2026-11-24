package graph

// heatmap_integration_test.go — aggregation acceptance for the information
// space heatmap (subject × info type). It proves the map counts both layers
// under one run_id, excludes unsubstantiated inferred atoms exactly like
// Region, orders rows deterministically, and discloses truncation/unlabeled
// items instead of silently losing them.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
)

func TestInfoSpaceHeatmapCountsBySubjectAndType(t *testing.T) {
	store := integrationPG(t)
	ctx := context.Background()
	runID := scanID(t)
	ctxID := runID + "-ctx"
	cleanupInfoSpaceScan(t, store, runID, ctxID)

	addNode := func(kind Kind, subject, kindLabel string) {
		t.Helper()
		payload := fmt.Sprintf(
			`{"target":%q,"vuln_class":"sqli","info":{"subject":{"kind":%q,"identifier":%q},`+
				`"purpose":"sqli","nature":"inferred","observed_at":"2026-11-24T01:00:00Z"}}`,
			subject, kindLabel, subject)
		if _, err := store.AddNode(ctx, NodeInput{
			ScanID: runID, Kind: kind, State: DefaultState(kind), Priority: 1, Payload: payload,
		}); err != nil {
			t.Fatalf("add %s node: %v", kind, err)
		}
	}
	addNode(KindBelief, "/api", "endpoint")
	addNode(KindIntent, "/api", "endpoint")
	addNode(KindFact, "/api", "endpoint")
	addNode(KindFinding, "/api", "endpoint")
	addNode(KindFact, "10.0.1.5:8080", "container")

	for seq := 1; seq <= 4; seq++ {
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO run_journal (scan_id, instance_id, seq, record)
			VALUES ($1, $2, $3, '{}')`, ctxID, runID, seq); err != nil {
			t.Fatalf("journal seq %d: %v", seq, err)
		}
	}
	for _, atom := range []struct {
		id      string
		seq     int
		nature  string
		support string
	}{
		{id: "h-atom-a", seq: 1, nature: "observed", support: "[]"},
		{id: "h-atom-b", seq: 2, nature: "observed", support: "[]"},
		{id: "h-atom-c", seq: 3, nature: "inferred", support: `["seed"]`},
		{id: "h-atom-d", seq: 4, nature: "inferred", support: "[]"}, // unsubstantiated
	} {
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO info_items (
				id, scan_id, source_seq, nature, content, coordinates, source, subject,
				purpose, context, relation_ids, supports, confidence, coordinate_status,
				observed_at, created_at, instance_id)
			VALUES ($1,$2,$3,$4,
				'{"text":"observed response","media_type":"text/plain"}',
				'{"purpose":"sqli"}',
				'{"kind":"tool","name":"http_request"}',
				'{"kind":"endpoint","identifier":"/api"}',
				'sqli','{"role":"user"}','[]',$5,0.9,'complete',
				'2026-11-24T01:00:00Z','2026-11-24T01:00:00Z',$6)`,
			atom.id, ctxID, atom.seq, atom.nature, atom.support, runID); err != nil {
			t.Fatalf("insert atom %s: %v", atom.id, err)
		}
	}

	reader := infospace.HeatmapReader(store)
	res, err := reader.Heatmap(ctx, infospace.HeatmapQuery{
		RunIDs: []string{runID}, RowLimit: 10,
	})
	if err != nil {
		t.Fatalf("heatmap: %v", err)
	}
	if len(res.Types) != len(infospace.DefaultInfoTypes()) {
		t.Fatalf("types = %v", res.Types)
	}
	if res.Unlabeled != 0 || res.SubjectTotal != 2 || res.Truncated {
		t.Fatalf("totals = unlabeled:%d subjects:%d truncated:%v", res.Unlabeled, res.SubjectTotal, res.Truncated)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	api := res.Rows[0]
	if api.Subject != "/api" || api.SubjectKind != "endpoint" || api.Total != 7 {
		t.Fatalf("first row = %+v", api)
	}
	want := map[infospace.InfoType]int{
		infospace.InfoTypeObservation: 2,
		infospace.InfoTypeInference:   1, // h-atom-d has no supports → off the map
		infospace.InfoTypeBelief:      1,
		infospace.InfoTypeIntent:      1,
		infospace.InfoTypeFact:        1,
		infospace.InfoTypeFinding:     1,
	}
	for typ, n := range want {
		if api.Counts[typ] != n {
			t.Fatalf("%s count = %d, want %d (all counts %v)", typ, api.Counts[typ], n, api.Counts)
		}
	}
	if res.Rows[1].Subject != "10.0.1.5:8080" || res.Rows[1].Counts[infospace.InfoTypeFact] != 1 {
		t.Fatalf("second row = %+v", res.Rows[1])
	}

	// Row cap: one row, truncation disclosed against the true subject total.
	capped, err := reader.Heatmap(ctx, infospace.HeatmapQuery{
		RunIDs: []string{runID}, RowLimit: 1,
	})
	if err != nil {
		t.Fatalf("capped heatmap: %v", err)
	}
	if len(capped.Rows) != 1 || !capped.Truncated || capped.SubjectTotal != 2 {
		t.Fatalf("capped = rows:%d truncated:%v subjects:%d", len(capped.Rows), capped.Truncated, capped.SubjectTotal)
	}

	// Type narrowing removes the container row entirely (it only has a fact).
	beliefOnly, err := reader.Heatmap(ctx, infospace.HeatmapQuery{
		RunIDs: []string{runID}, Types: []infospace.InfoType{infospace.InfoTypeBelief},
	})
	if err != nil {
		t.Fatalf("belief heatmap: %v", err)
	}
	if len(beliefOnly.Rows) != 1 || beliefOnly.Rows[0].Subject != "/api" ||
		beliefOnly.Rows[0].Total != 1 || beliefOnly.SubjectTotal != 1 {
		t.Fatalf("belief-only = %+v (subjects %d)", beliefOnly.Rows, beliefOnly.SubjectTotal)
	}

	// Time window excludes everything: an empty map, not an error.
	after, err := time.Parse(time.RFC3339, "2026-11-24T02:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := reader.Heatmap(ctx, infospace.HeatmapQuery{
		RunIDs: []string{runID}, ObservedAfter: &after,
	})
	if err != nil {
		t.Fatalf("time-filtered heatmap: %v", err)
	}
	if len(empty.Rows) != 0 || empty.SubjectTotal != 0 || empty.Unlabeled != 0 || empty.Truncated {
		t.Fatalf("time-filtered = %+v", empty)
	}
}
