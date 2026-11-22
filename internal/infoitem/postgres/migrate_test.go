package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEmbeddedMigrationsAreOrderedAndNonEmpty(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations")
	}
	// The info-model migration defines the cold-path tables; later migrations
	// (e.g. 0002 platform) add their own schemas, so the containment check
	// applies to the set as a whole rather than every file.
	someMigrationDefinesInfoItems := false
	for i, m := range migrations {
		if m.version != i+1 {
			t.Fatalf("migration %d has version %d", i, m.version)
		}
		if strings.Contains(m.sql, "info_items") {
			someMigrationDefinesInfoItems = true
		}
		// Later migrations are not all table-creating: 0010 adds a column to
		// guidance. Any of the DDL forms counts as a real migration; an empty
		// or comment-only file still fails.
		if !strings.Contains(m.sql, "CREATE TABLE") &&
			!strings.Contains(m.sql, "ALTER TABLE") &&
			!strings.Contains(m.sql, "CREATE INDEX") &&
			!strings.Contains(m.sql, "CREATE UNIQUE INDEX") {
			t.Fatalf("migration %q contains no DDL", m.name)
		}
	}
	if !someMigrationDefinesInfoItems {
		t.Fatal("no migration defines info_items")
	}
}

func TestMigratePostgres(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// A second run must be an idempotent no-op.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second migration run: %v", err)
	}

	for _, table := range []string{"info_items", "info_item_edges", "info_audit_records", "info_sync_watermarks"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("table %s was not created", table)
		}
	}
}
