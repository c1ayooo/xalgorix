package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// AppendSync appends data to path and fsyncs the file before returning.
//
// The file and any newly created directory entry are durable on platforms
// where directory fsync is supported. The caller must serialize concurrent
// appends to the same path; O_APPEND prevents truncation races with other
// writers, but it does not define record boundaries between application
// writers. Callers that share a journal should hold their own writer lock.
func AppendSync(path string, data []byte) error {
	if path == "" {
		return errors.New("storage: append: empty path")
	}
	if len(data) == 0 {
		return nil
	}

	dir := filepath.Dir(path)
	created := false
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("storage: stat append target %q: %w", path, err)
		}
		created = true
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("storage: open append target %q: %w", path, err)
	}

	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = fmt.Errorf("short write: wrote %d of %d bytes", n, len(data))
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("storage: append %q: %w", path, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("storage: close append target %q: %w", path, closeErr)
	}
	if created && runtime.GOOS != "windows" {
		d, err := os.Open(dir)
		if err != nil {
			return fmt.Errorf("storage: open append directory %q: %w", dir, err)
		}
		err = d.Sync()
		_ = d.Close()
		if err != nil {
			return fmt.Errorf("storage: fsync append directory %q: %w", dir, err)
		}
	}
	return nil
}
