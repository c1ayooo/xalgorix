//go:build !windows

package platformutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// ErrSelfProcessGroup is returned when the target process belongs to the same
// process group as xalgorix; killing that group would kill the agent itself.
var ErrSelfProcessGroup = errors.New("refusing to kill xalgorix process group")

// OpenReadNoFollow opens path read-only without following a symlink at the
// final component.
func OpenReadNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// ShutdownSignals returns the signals that trigger a graceful shutdown.
func ShutdownSignals() []os.Signal {
	return []os.Signal{syscall.SIGTERM, syscall.SIGINT}
}

// RestartSignal returns SIGUSR1, or nil when the platform has no equivalent.
func RestartSignal() os.Signal { return syscall.SIGUSR1 }

// ExecReplace replaces the current process image. Used by the restart path.
func ExecReplace() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}

// ConfigureProcessGroup starts a child in its own process group so its
// descendants can be terminated together.
func ConfigureProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// KillProcessTree terminates the child's whole process group, then the direct
// process as a fallback. Unix semantics are unchanged from the previous
// terminal/scanctx helpers.
func KillProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	var groupErr error
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 0 {
		selfPGID, selfErr := syscall.Getpgid(os.Getpid())
		if selfErr != nil || pgid != selfPGID {
			if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
				groupErr = fmt.Errorf("kill process group %d for pid %d: %w", pgid, pid, err)
			}
		} else {
			groupErr = fmt.Errorf("%w (pgid %d, pid %d)", ErrSelfProcessGroup, pgid, pid)
		}
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		if groupErr != nil {
			return fmt.Errorf("%v; direct kill pid %d: %w", groupErr, pid, err)
		}
		return fmt.Errorf("kill process pid %d: %w", pid, err)
	}
	if groupErr != nil && !errors.Is(groupErr, ErrSelfProcessGroup) {
		return groupErr
	}
	return nil
}
