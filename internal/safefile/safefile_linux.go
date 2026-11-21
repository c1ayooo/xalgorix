//go:build linux

package safefile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// MkdirAll creates path and missing parents without following symlinks.
func MkdirAll(path string, perm os.FileMode) error {
	clean, err := cleanAbsolute(path)
	if err != nil {
		return err
	}
	parts := split(clean)
	dirfd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("safefile: open root: %w", err)
	}
	defer unix.Close(dirfd)
	for _, part := range parts {
		fd, openErr := unix.Openat(dirfd, part, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr == unix.ENOENT {
			if mkErr := unix.Mkdirat(dirfd, part, uint32(perm.Perm())); mkErr != nil && mkErr != unix.EEXIST {
				return fmt.Errorf("safefile: mkdir %s: %w", part, mkErr)
			}
			fd, openErr = unix.Openat(dirfd, part, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			if openErr == unix.ELOOP || openErr == unix.ENOTDIR {
				return fmt.Errorf("%w: %s: %v", ErrUnsafePath, clean, openErr)
			}
			return fmt.Errorf("safefile: mkdir path %s: %w", clean, openErr)
		}
		unix.Close(dirfd)
		dirfd = fd
	}
	return nil
}

// ReadFile opens path without following symlinks and returns its bytes.
func ReadFile(path string) ([]byte, error) {
	f, err := openRead(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// WriteFile atomically replaces path with data. The temporary file is created
// in the same directory through the already-open directory fd.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dirfd, base, err := openParent(path)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	return writeAt(dirfd, base, data, perm, false)
}

// Create creates a new file, failing if it already exists.
func Create(path string, data []byte, perm os.FileMode) error {
	dirfd, base, err := openParent(path)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	return writeAt(dirfd, base, data, perm, true)
}

func openRead(path string) (*os.File, error) {
	clean, err := cleanAbsolute(path)
	if err != nil {
		return nil, err
	}
	parts := split(clean)
	dirfd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("safefile: open root: %w", err)
	}
	for i, part := range parts {
		last := i == len(parts)-1
		flags := unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if last {
			flags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		}
		fd, openErr := unix.Openat(dirfd, part, flags, 0)
		unix.Close(dirfd)
		if openErr != nil {
			if openErr == unix.ELOOP || openErr == unix.ENOTDIR {
				return nil, fmt.Errorf("%w: %s: %v", ErrUnsafePath, clean, openErr)
			}
			return nil, fmt.Errorf("safefile: open %s: %w", clean, openErr)
		}
		dirfd = fd
	}
	return os.NewFile(uintptr(dirfd), clean), nil
}

func openParent(path string) (int, string, error) {
	clean, err := cleanAbsolute(path)
	if err != nil {
		return -1, "", err
	}
	parts := split(clean)
	if len(parts) < 1 {
		return -1, "", ErrUnsafePath
	}
	base := parts[len(parts)-1]
	dirfd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", fmt.Errorf("safefile: open root: %w", err)
	}
	for _, part := range parts[:len(parts)-1] {
		fd, openErr := unix.Openat(dirfd, part, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(dirfd)
		if openErr != nil {
			if openErr == unix.ELOOP || openErr == unix.ENOTDIR {
				return -1, "", fmt.Errorf("%w: %s: %v", ErrUnsafePath, clean, openErr)
			}
			return -1, "", fmt.Errorf("safefile: open parent for %s: %w", clean, openErr)
		}
		dirfd = fd
	}
	return dirfd, base, nil
}

func writeAt(dirfd int, base string, data []byte, perm os.FileMode, exclusive bool) error {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := ".safefile-" + hex.EncodeToString(suffix[:]) + ".tmp"
	flags := unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Openat(dirfd, tmp, flags, uint32(perm.Perm()))
	if err != nil {
		return fmt.Errorf("safefile: create temp: %w", err)
	}
	cleanup := func() {
		_ = unix.Unlinkat(dirfd, tmp, 0)
	}
	f := os.NewFile(uintptr(fd), tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if exclusive {
		// linkat with no flags is an atomic "create only if absent" that
		// preserves the temp file's data. Unlink the temp afterwards.
		if err := unix.Linkat(dirfd, tmp, dirfd, base, 0); err != nil {
			cleanup()
			return fmt.Errorf("safefile: create %s: %w", base, err)
		}
		cleanup()
	} else if err := unix.Renameat(dirfd, tmp, dirfd, base); err != nil {
		cleanup()
		return fmt.Errorf("safefile: replace %s: %w", base, err)
	}
	// dirfd was opened O_PATH and cannot be fsynced directly. Open the same
	// directory read-only for fsync so a directory-entry failure is surfaced.
	syncFD, err := unix.Openat(dirfd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("safefile: reopen directory after %s: %w", base, err)
	}
	fsyncErr := unix.Fsync(syncFD)
	_ = unix.Close(syncFD)
	if fsyncErr != nil {
		return fmt.Errorf("safefile: fsync directory after %s: %w", base, fsyncErr)
	}
	return nil
}

func split(clean string) []string {
	trimmed := strings.TrimPrefix(clean, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
