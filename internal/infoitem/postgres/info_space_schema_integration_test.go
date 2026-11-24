package postgres

// info_space_schema_integration_test.go — IG1b schema/plan acceptance: the
// unified views, the aggregate↔atom link table, and the expression indexes the
// information-space queries rely on must exist after migration.

import (
	"context"
	"strings"
	"testing"
)

func TestInfoSpaceSchemaObjectsAndIndexPlan(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()

	for _, name := range []string{"info_space_items", "info_space_edges", "graph_info_links"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
			t.Fatalf("to_regclass %s: %v", name, err)
		}
		if !exists {
			t.Fatalf("%s was not created", name)
		}
	}
	for _, name := range []string{
		"exploration_nodes_info_subject_idx",
		"exploration_nodes_info_purpose_idx",
		"info_items_scan_observed_idx",
		"info_items_run_observed_idx",
		"graph_info_links_info_idx",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1
			)`, name).Scan(&exists); err != nil {
			t.Fatalf("index %s: %v", name, err)
		}
		if !exists {
			t.Fatalf("index %s was not created", name)
		}
	}

	// Force index use with a seqscan-off transaction; the plan must reference
	// the subject expression index rather than a full scan.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	var plan string
	if err := tx.QueryRow(ctx, `
		EXPLAIN (FORMAT JSON)
		SELECT id FROM exploration_nodes
		WHERE scan_id = 'plan-check'
		  AND COALESCE(
		        xalgorix_safe_jsonb(payload) -> 'info' -> 'subject' ->> 'identifier',
		        xalgorix_safe_jsonb(payload) ->> 'target',
		        xalgorix_safe_jsonb(payload) ->> 'endpoint',
		        ''
		      ) = '/api'
		ORDER BY created_at DESC
		LIMIT 1`).Scan(&plan); err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(plan, "exploration_nodes_info_subject_idx") {
		t.Fatalf("plan did not use the subject index: %s", plan)
	}

	var atomPlan string
	if err := tx.QueryRow(ctx, `
		EXPLAIN (FORMAT JSON)
		SELECT id FROM info_items
		WHERE COALESCE(NULLIF(instance_id, ''), scan_id) = 'plan-check'
		ORDER BY observed_at DESC, id
		LIMIT 1`).Scan(&atomPlan); err != nil {
		t.Fatalf("explain atom run key: %v", err)
	}
	if !strings.Contains(atomPlan, "info_items_run_observed_idx") {
		t.Fatalf("atom plan did not use the run index: %s", atomPlan)
	}
}
