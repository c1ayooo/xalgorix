// Package safefile provides path-based file operations that do not re-open an
// attacker-controlled path after a policy check. On Unix every component is
// opened with O_NOFOLLOW from a directory fd, so a symlink swapped in after the
// check cannot redirect the operation outside the validated tree.
package safefile

import (
	"errors"
	"path/filepath"
	"strings"
)

var (
	// ErrUnsafePath reports a relative path, a ".." component, or a symlink
	// found while walking the validated path.
	ErrUnsafePath = errors.New("safefile: unsafe path")
	// ErrNotSupported reports a platform without the required syscall
	// primitives. The caller must not silently fall back to a path reopen.
	ErrNotSupported = errors.New("safefile: secure open not supported on this platform")
)

func cleanAbsolute(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrUnsafePath
	}
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) || clean == "." {
		return "", ErrUnsafePath
	}
	trimmed := strings.TrimPrefix(clean, string(filepath.Separator))
	for _, part := range strings.Split(trimmed, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return "", ErrUnsafePath
		}
	}
	return clean, nil
}
