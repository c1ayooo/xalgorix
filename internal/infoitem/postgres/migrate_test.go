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
	// Versions must be strictly increasing, but need not be contiguous:
	// concurrent feature branches reserve ranges (for example the information
	// space views at 9000+) so two sessions adding migrations cannot collide on
	// the next sequential number.
	someMigrationDefinesInfoItems := false
	prevVersion := 0
	for _, m := range migrations {
		if m.version <= prevVersion {
			t.Fatalf("migration %q has version %d after %d", m.name, m.version, prevVersion)
		}
		prevVersion = m.version
		if strings.Contains(m.sql, "info_items") {
			someMigrationDefinesInfoItems = true
		}
		// Later migrations are not all table-creating: 0010 adds a column to
		// guidance. Any of the DDL forms counts as a real migration; an empty
		// or comment-only file still fails.
		if !strings.Contains(m.sql, "CREATE TABLE") &&
			!strings.Contains(m.sql, "ALTER TABLE") &&
			!strings.Contains(m.sql, "CREATE INDEX") &&
			!strings.Contains(m.sql, "CREATE UNIQUE INDEX") &&
			!strings.Contains(m.sql, "CREATE VIEW") &&
			!strings.Contains(m.sql, "CREATE OR REPLACE VIEW") {
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
