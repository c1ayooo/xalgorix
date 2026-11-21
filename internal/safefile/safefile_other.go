//go:build !linux

package safefile

import (
	"fmt"
	"os"
	"path/filepath"
)

// MkdirAll is unavailable on platforms without openat semantics.
func MkdirAll(string, os.FileMode) error {
	return ErrNotSupported
}

// ReadFile is unavailable on platforms without openat/O_NOFOLLOW semantics.
// Returning an explicit error avoids a silent fallback to an unsafe reopen.
func ReadFile(string) ([]byte, error) {
	return nil, ErrNotSupported
}

// WriteFile is unavailable on platforms without openat/O_NOFOLLOW semantics.
func WriteFile(string, []byte, os.FileMode) error {
	return ErrNotSupported
}

// Create is unavailable on platforms without openat/O_NOFOLLOW semantics.
func Create(string, []byte, os.FileMode) error {
	return ErrNotSupported
}

// ResolveCanonical is a bounded helper for callers that need the canonical
// path on non-Unix platforms; it follows symlinks once and reports a vanished
// target rather than reopening an unchecked path.
func ResolveCanonical(path string) (string, error) {
	clean, err := cleanAbsolute(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("safefile: resolve: %w", err)
	}
	return resolved, nil
}
