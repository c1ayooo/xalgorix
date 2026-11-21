package engine

// Durable Eino checkpoints. The runner uses this store to pause a tool call for
// human approval and resume the same run after a decision or restart. PostgreSQL
// is used when a DSN exists; without one the fallback is an atomic file per
// checkpoint under <dataDir>/_engine/checkpoints.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
)

var _ adk.CheckPointStore = (*memoryCheckpointStore)(nil)

// OpenCheckpointStore selects the cold-path checkpoint backend. Callers own the
// returned store's lifecycle (Close is optional on the interface).
func OpenCheckpointStore(ctx context.Context, dataDir, dsn string) (adk.CheckPointStore, error) {
	if strings.TrimSpace(dsn) != "" {
		return NewPGCheckpointStore(ctx, dsn)
	}
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("engine: data directory is required for file checkpoints")
	}
	return NewFileCheckpointStore(dataDir)
}

// checkpointStoreCloser is implemented by the PG backend.
type checkpointStoreCloser interface {
	Close()
}

// CloseCheckpointStore releases a PG-backed store when it owns a pool.
func CloseCheckpointStore(store adk.CheckPointStore) {
	if closer, ok := store.(checkpointStoreCloser); ok {
		closer.Close()
	}
}

// memoryCheckpointStore is used by tests and by a caller that deliberately
// wants non-durable checkpoints (for example a fake runtime test).
type memoryCheckpointStore struct {
	data map[string][]byte
}

func NewMemoryCheckpointStore() adk.CheckPointStore {
	return &memoryCheckpointStore{data: map[string][]byte{}}
}

func (s *memoryCheckpointStore) Get(_ context.Context, checkpointID string) ([]byte, bool, error) {
	payload, ok := s.data[checkpointID]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), payload...), true, nil
}

func (s *memoryCheckpointStore) Set(_ context.Context, checkpointID string, checkpoint []byte) error {
	if strings.TrimSpace(checkpointID) == "" {
		return errors.New("engine: empty checkpoint id")
	}
	if len(checkpoint) == 0 {
		return errors.New("engine: empty checkpoint payload")
	}
	s.data[checkpointID] = append([]byte(nil), checkpoint...)
	return nil
}

func validateCheckpointID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("engine: empty checkpoint id")
	}
	return nil
}

func wrapCheckpointErr(op, id string, err error) error {
	return fmt.Errorf("engine: checkpoint %s %q: %w", op, id, err)
}
