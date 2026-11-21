//go:build windows

package platformutil

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// TryLockFile attempts a non-blocking exclusive LockFileEx. A held lock returns
// (false, nil); other failures return an error.
func TryLockFile(f *os.File) (bool, error) {
	if f == nil {
		return false, errors.New("platformutil: nil lock file")
	}
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped,
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return false, err
}

// UnlockFile releases a LockFileEx lock.
func UnlockFile(f *os.File) error {
	if f == nil {
		return nil
	}
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
