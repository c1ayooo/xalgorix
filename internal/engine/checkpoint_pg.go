package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGCheckpointStore persists Eino runner checkpoints in the platform cold path.
// The table is created by infoitem/postgres migration 0005; this store does not
// run migrations itself so a normal startup keeps one migration owner.
type PGCheckpointStore struct {
	pool *pgxpool.Pool
}

var _ CheckpointFileStoreInterface = (*PGCheckpointStore)(nil)

func NewPGCheckpointStore(ctx context.Context, dsn string) (*PGCheckpointStore, error) {
	conf, err := pgxpool.ParseConfig(strings.TrimSpace(dsn))
	if err != nil {
		return nil, fmt.Errorf("engine: parse checkpoint database URL: %w", err)
	}
	if conf.MaxConns < 1 || conf.MaxConns > 4 {
		conf.MaxConns = 2
	}
	pool, err := pgxpool.NewWithConfig(ctx, conf)
	if err != nil {
		return nil, fmt.Errorf("engine: create checkpoint pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("engine: checkpoint PostgreSQL unavailable: %w", err)
	}
	return &PGCheckpointStore{pool: pool}, nil
}

func (s *PGCheckpointStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *PGCheckpointStore) Get(ctx context.Context, checkpointID string) ([]byte, bool, error) {
	if err := validateCheckpointID(checkpointID); err != nil {
		return nil, false, err
	}
	if s == nil || s.pool == nil {
		return nil, false, errors.New("engine: checkpoint store unavailable")
	}
	var payload []byte
	err := s.pool.QueryRow(ctx,
		`SELECT payload FROM engine_checkpoints WHERE checkpoint_id = $1`, checkpointID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, wrapCheckpointErr("read", checkpointID, err)
	}
	return payload, true, nil
}

func (s *PGCheckpointStore) Set(ctx context.Context, checkpointID string, checkpoint []byte) error {
	if err := validateCheckpointID(checkpointID); err != nil {
		return err
	}
	if len(checkpoint) == 0 {
		return fmt.Errorf("engine: checkpoint %q payload is empty", checkpointID)
	}
	if s == nil || s.pool == nil {
		return errors.New("engine: checkpoint store unavailable")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO engine_checkpoints (checkpoint_id, payload, created_at, updated_at)
		VALUES ($1, $2, now(), now())
		ON CONFLICT (checkpoint_id) DO UPDATE
		SET payload = EXCLUDED.payload, updated_at = now()`, checkpointID, checkpoint); err != nil {
		return wrapCheckpointErr("write", checkpointID, err)
	}
	return nil
}
