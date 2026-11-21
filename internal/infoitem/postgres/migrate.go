// Package postgres implements the optional PostgreSQL cold path for InfoItem,
// relation edges, audit projection and synchronisation watermarks.
package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationTable = "xalgorix_schema_migrations"

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate applies embedded info-model migrations. It is safe to call on every
// startup; one transaction-level advisory lock serializes concurrent processes.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("infoitem/postgres: nil pool")
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: begin migrations: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('xalgorix_schema_migrations'))`); err != nil {
		return fmt.Errorf("infoitem/postgres: migration lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+migrationTable+` (
		version INT PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("infoitem/postgres: create migration table: %w", err)
	}

	for _, m := range migrations {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+migrationTable+` WHERE version = $1)`, m.version).Scan(&exists); err != nil {
			return fmt.Errorf("infoitem/postgres: check migration %d: %w", m.version, err)
		}
		if exists {
			continue
		}

		// pgx cannot prepare a multi-statement query through the extended
		// protocol. PgConn.Exec uses the simple protocol on the same connection,
		// which is still inside this transaction.
		results := tx.Conn().PgConn().Exec(ctx, m.sql)
		if _, err := results.ReadAll(); err != nil {
			return fmt.Errorf("infoitem/postgres: apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO `+migrationTable+` (version, name) VALUES ($1, $2)`,
			m.version, m.name,
		); err != nil {
			return fmt.Errorf("infoitem/postgres: record migration %d: %w", m.version, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("infoitem/postgres: commit migrations: %w", err)
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("infoitem/postgres: read embedded migrations: %w", err)
	}
	out := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("infoitem/postgres: migration %q has no numeric prefix", entry.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("infoitem/postgres: invalid migration version in %q", entry.Name())
		}
		data, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("infoitem/postgres: read migration %q: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return nil, fmt.Errorf("infoitem/postgres: migration %q is empty", entry.Name())
		}
		out = append(out, migration{version: version, name: entry.Name(), sql: string(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("infoitem/postgres: duplicate migration version %d", out[i].version)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("infoitem/postgres: no embedded migrations")
	}
	return out, nil
}
