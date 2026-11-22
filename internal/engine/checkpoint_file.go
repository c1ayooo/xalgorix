package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/c1ayooo/xalgorix/v4/internal/storage"
)

// FileCheckpointStore is the no-DSN degradation: one atomic file per
// checkpoint ID. IDs are hashed so an interrupt address can never escape the
// store directory.
type FileCheckpointStore struct {
	dir string
}

var _ CheckpointFileStoreInterface = (*FileCheckpointStore)(nil)

// CheckpointFileStoreInterface is a compile-time documentation helper for the
// ADK-compatible surface.
type CheckpointFileStoreInterface interface {
	Get(ctx context.Context, checkpointID string) ([]byte, bool, error)
	Set(ctx context.Context, checkpointID string, checkpoint []byte) error
}

func NewFileCheckpointStore(dataDir string) (*FileCheckpointStore, error) {
	if dataDir == "" {
		return nil, errors.New("engine: data directory is required for file checkpoints")
	}
	return &FileCheckpointStore{dir: filepath.Join(dataDir, "_engine", "checkpoints")}, nil
}

func (s *FileCheckpointStore) path(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".ckpt")
}

func (s *FileCheckpointStore) Get(_ context.Context, checkpointID string) ([]byte, bool, error) {
	if err := validateCheckpointID(checkpointID); err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(s.path(checkpointID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, wrapCheckpointErr("read", checkpointID, err)
	}
	return data, true, nil
}

func (s *FileCheckpointStore) Set(_ context.Context, checkpointID string, checkpoint []byte) error {
	if err := validateCheckpointID(checkpointID); err != nil {
		return err
	}
	if len(checkpoint) == 0 {
		return fmt.Errorf("engine: checkpoint %q payload is empty", checkpointID)
	}
	if err := storage.EnsureSecureDir(s.dir); err != nil {
		return wrapCheckpointErr("ensure dir", checkpointID, err)
	}
	if err := storage.WriteAtomic(s.path(checkpointID), checkpoint); err != nil {
		return wrapCheckpointErr("write", checkpointID, err)
	}
	return nil
}
