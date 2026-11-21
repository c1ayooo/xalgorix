//go:build windows

package platformutil

import (
	"errors"
	"os"
	"os/exec"
)

// OpenReadNoFollow opens path read-only. Windows has no O_NOFOLLOW; callers
// that need symlink rejection must perform an explicit reparse-point check.
func OpenReadNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}

// ShutdownSignals returns the interrupt signal available on Windows.
func ShutdownSignals() []os.Signal { return []os.Signal{os.Interrupt} }

// RestartSignal is unavailable on Windows (no SIGUSR1 equivalent).
func RestartSignal() os.Signal { return nil }

// ExecReplace is unavailable on Windows; callers must spawn a new process.
func ExecReplace() error {
	return errors.New("platformutil: process replace is not supported on windows")
}

// ConfigureProcessGroup is a no-op on Windows: there is no Unix process group
// with negative-PID signal semantics.
func ConfigureProcessGroup(*exec.Cmd) {}

// KillProcessTree kills only the direct child on Windows. Descendants are not
// claimed to be terminated; callers that need job-object semantics must add a
// dedicated backend.
func KillProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
