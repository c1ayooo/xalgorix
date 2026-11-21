package platform

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	infopg "github.com/xalgord/xalgorix/v4/internal/infoitem/postgres"
)

// Config controls repository construction. An empty DSN selects the file
// store (single-binary degradation, REDESIGN §3.3); a DSN selects PostgreSQL.
type Config struct {
	DSN     string
	DataDir string
}

// Open builds the repository for the given configuration. Like the cold path
// service, callers must treat an error as "platform disabled" — scanning
// never depends on it (charter §6.6).
func Open(ctx context.Context, cfg Config) (Repository, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return NewFileStore(cfg.DataDir)
	}
	return openPostgres(ctx, cfg.DSN)
}

// openPostgres builds its own pool (independent of the cold path's pool —
// both clamp MaxConns so two pools stay within the same connection budget),
// runs the shared embedded migrations, and seeds the default project.
func openPostgres(ctx context.Context, dsn string) (Repository, error) {
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("platform: parse database URL: %w", err)
	}
	if poolConfig.MaxConns < 2 || poolConfig.MaxConns > 8 {
		poolConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("platform: create PostgreSQL pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("platform: PostgreSQL unavailable: %w", err)
	}
	// The shared migrator owns both the info-model and the platform tables;
	// adding 0002 here required zero migrator changes.
	if err := infopg.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	store := NewPostgres(pool)
	if _, err := store.EnsureDefaultProject(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}
