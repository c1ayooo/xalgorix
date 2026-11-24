package postgres

// info_space_integration_test.go — IG0 acceptance: the unified information
// space views must project the aggregate (exploration graph) and atom
// (info_items) layers without loss, duplicate IDs, or write authority.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestInfoSpaceViewsProjectBothLayers(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	scanID := fmt.Sprintf("infospace-%d", time.Now().UnixNano())
	ctxID := scanID + "-ctx"

	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM info_item_edges WHERE scan_id = ANY($1)`,
			`DELETE FROM info_items WHERE scan_id = ANY($1)`,
			`DELETE FROM run_journal WHERE scan_id = ANY($1)`,
		} {
			if _, err := pool.Exec(ctx, stmt, []string{scanID, ctxID}); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
		for _, stmt := range []string{
			`DELETE FROM exploration_edges WHERE scan_id = $1`,
			`DELETE FROM exploration_nodes WHERE scan_id = $1`,
		} {
			if _, err := pool.Exec(ctx, stmt, scanID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})

	// Aggregate 1: explicit info block (IG2 convention).
	var parentID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO exploration_nodes (scan_id, kind, state, priority, origin, payload)
		VALUES ($1, 'intent', 'done', 5, 'llm', $2)
		RETURNING id`,
		scanID,
		`{"task_id":"i1","title":"probe","target":"10.0.1.5:8080","target_kind":"container",`+
			`"vuln_class":"ssrf","info":{"subject":{"kind":"container","identifier":"10.0.1.5:8080"},`+
			`"purpose":"ssrf","nature":"inferred","observed_at":"2026-11-24T00:00:00Z",`+
			`"context":{"phase":"6"}}}`,
	).Scan(&parentID); err != nil {
		t.Fatalf("insert aggregate with info block: %v", err)
	}

	// Aggregate 2: legacy payload without an info block. IG0 must fall back to
	// the fields projections already write so the view is useful before IG2.
	var legacyID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO exploration_nodes (scan_id, kind, state, priority, origin, payload)
		VALUES ($1, 'fact', 'confirmed', 3, 'worker', $2)
		RETURNING id`,
		scanID,
		`{"task_id":"f1","title":"legacy","target":"10.0.1.6:8080","target_kind":"web","vuln_class":"xss"}`,
	).Scan(&legacyID); err != nil {
		t.Fatalf("insert legacy aggregate: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO exploration_edges (scan_id, src_id, rel, dst_id)
		VALUES ($1, $2, 'supports', $3)`, scanID, legacyID, parentID); err != nil {
		t.Fatalf("insert aggregate edge: %v", err)
	}

	// Atom layer: two items plus one relation, matching the aggregate edge.
	for i, id := range []string{"atom-a", "atom-b"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO info_items (
				id, scan_id, source_seq, nature, content, coordinates, source, subject,
				purpose, context, relation_ids, supports, confidence, coordinate_status,
				observed_at, created_at, instance_id)
			VALUES ($1,$2,$3,'observed',
				'{"text":"observed response","media_type":"text/plain"}',
				'{"purpose":"ssrf"}',
				'{"kind":"tool","name":"http_request"}',
				'{"kind":"endpoint","identifier":"/api"}',
				'ssrf','{"role":"user"}','[]','[]',0.9,'complete',
				'2026-11-24T00:00:00Z','2026-11-24T00:00:00Z',$2)`,
			id, scanID, i+1); err != nil {
			t.Fatalf("insert atom %s: %v", id, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO info_item_edges (scan_id, source_seq, from_id, to_id, type, weight, created_at)
		VALUES ($1, 1, 'atom-a', 'atom-b', 'supports', 0.8, now())`, scanID); err != nil {
		t.Fatalf("insert atom edge: %v", err)
	}

	// A sibling context's atom: its native scan_id is the context id, but the
	// journal row carries the run identity. The view must expose run_id so a
	// run-scoped query still sees it.
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_journal (scan_id, instance_id, seq, record)
		VALUES ($1, $2, 1, '{}')`, ctxID, scanID); err != nil {
		t.Fatalf("insert journal row: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO info_items (
			id, scan_id, source_seq, nature, content, coordinates, source, subject,
			purpose, context, relation_ids, supports, confidence, coordinate_status,
			observed_at, created_at, instance_id)
		VALUES ('atom-ctx',$1,1,'observed',
			'{"text":"sibling context observation","media_type":"text/plain"}',
			'{"purpose":"ssrf"}',
			'{"kind":"tool","name":"http_request"}',
			'{"kind":"endpoint","identifier":"/sibling"}',
			'ssrf','{"role":"user"}','[]','[]',0.9,'complete',
			'2026-11-24T00:00:00Z','2026-11-24T00:00:00Z',$2)`, ctxID, scanID); err != nil {
		t.Fatalf("insert sibling-context atom: %v", err)
	}
	var atomRunID, atomScanID string
	if err := pool.QueryRow(ctx, `
		SELECT run_id, scan_id FROM info_space_items WHERE info_id = 'atom-ctx'`).
		Scan(&atomRunID, &atomScanID); err != nil {
		t.Fatalf("query run_id projection: %v", err)
	}
	if atomRunID != scanID || atomScanID != ctxID {
		t.Fatalf("run_id/scan_id = %q/%q, want %q/%q", atomRunID, atomScanID, scanID, ctxID)
	}

	type row struct {
		Layer    string
		Kind     string
		Subject  string
		SubjectK string
		Purpose  string
		Nature   string
		Payload  string
	}
	rows, err := pool.Query(ctx, `
		SELECT layer, kind, subject, subject_kind, purpose, nature, payload::text
		FROM info_space_items
		WHERE run_id = $1
		ORDER BY layer, info_id`, scanID)
	if err != nil {
		t.Fatalf("query info_space_items: %v", err)
	}
	defer rows.Close()
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Layer, &r.Kind, &r.Subject, &r.SubjectK, &r.Purpose, &r.Nature, &r.Payload); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("rows = %d, want 2 aggregates + 3 atoms: %+v", len(got), got)
	}

	// Identify rows by kind instead of relying on BIGSERIAL tie order.
	var infoBlock, legacy row
	atoms := 0
	for _, r := range got {
		switch {
		case r.Layer == "aggregate" && r.Kind == "intent":
			infoBlock = r
		case r.Layer == "aggregate" && r.Kind == "fact":
			legacy = r
		case r.Layer == "atom":
			atoms++
			if r.Kind != "info_item" || r.Purpose != "ssrf" || r.Nature != "observed" {
				t.Fatalf("atom row = %+v", r)
			}
			switch r.Subject {
			case "/api":
				if !containsAll(r.Payload, `"content"`, `"observed response"`) {
					t.Fatalf("atom payload lost content: %s", r.Payload)
				}
			case "/sibling":
				if !containsAll(r.Payload, `"content"`, `"sibling context observation"`) {
					t.Fatalf("sibling atom payload lost content: %s", r.Payload)
				}
			default:
				t.Fatalf("unexpected atom subject = %+v", r)
			}
		default:
			t.Fatalf("unexpected row = %+v", r)
		}
	}
	if infoBlock.Subject != "10.0.1.5:8080" || infoBlock.SubjectK != "container" ||
		infoBlock.Purpose != "ssrf" || infoBlock.Nature != "inferred" {
		t.Fatalf("info-block coordinates = %+v", infoBlock)
	}
	if legacy.Subject != "10.0.1.6:8080" || legacy.SubjectK != "web" || legacy.Purpose != "xss" {
		t.Fatalf("legacy fallback coordinates = %+v", legacy)
	}
	if atoms != 3 {
		t.Fatalf("atoms = %d, want 3", atoms)
	}

	var edgeCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM info_space_edges WHERE run_id = $1`, scanID).Scan(&edgeCount); err != nil {
		t.Fatalf("query info_space_edges: %v", err)
	}
	if edgeCount != 2 {
		t.Fatalf("info_space_edges = %d, want aggregate + atom", edgeCount)
	}

	// Read-only: a UNION view is not insertable, so no writer can mistake it
	// for an authority.
	if _, err := pool.Exec(ctx, `
		INSERT INTO info_space_items (scan_id, info_id, layer) VALUES ($1, 'forged', 'aggregate')`,
		scanID); err == nil {
		t.Fatal("info_space_items accepted a write")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
