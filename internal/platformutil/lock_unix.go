//go:build !windows

package platformutil

import (
	"errors"
	"os"
	"syscall"
)

// TryLockFile attempts a non-blocking exclusive flock. A held lock returns
// (false, nil); other failures return an error.
func TryLockFile(f *os.File) (bool, error) {
	if f == nil {
		return false, errors.New("platformutil: nil lock file")
	}
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

// UnlockFile releases an exclusive flock.
func UnlockFile(f *os.File) error {
	if f == nil {
		return nil
	}
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
