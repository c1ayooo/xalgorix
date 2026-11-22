package terminal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

// TestOrphanHoldingPipeDoesNotWedgeTheCommand pins the wg.Wait/cmd.Wait
// ordering deadlock: a process-group escapee (setsid) that inherits stdout
// and outlives its killed parent keeps the pipe write ends open, so the
// reader goroutines never see EOF. The old inline ordering — drain readers
// strictly before cmd.Wait() — could never reach Wait's own pipe close (the
// only thing that unblocks those readers) and blocked the tool call, and
// with it the whole scan loop, until the escapee happened to exit. The fixed
// ordering runs the reaper concurrently and bounds the drain, returning the
// [TIMEOUT] result within seconds.
func TestOrphanHoldingPipeDoesNotWedgeTheCommand(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not available")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not available")
	}

	sc := scanctx.New("term-orphan", t.TempDir())
	scanctx.Activate(sc)
	t.Cleanup(func() {
		CleanupContext(sc.ID)
		scanctx.Deactivate(sc.ID)
	})

	pidFile := filepath.Join(t.TempDir(), "orphan.pid")
	// The escapee must INHERIT bash's stdout/stderr (no redirect): it is the
	// unowned pipe write end that wedged the old ordering.
	command := fmt.Sprintf(`setsid sh -c 'echo $$ > %s; exec sleep 120' & sleep 120`, pidFile)

	killOrphan := func() {
		raw, rerr := os.ReadFile(pidFile)
		if rerr != nil {
			return
		}
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
	t.Cleanup(killOrphan)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan struct{})
	var res tools.Result
	var execErr error
	go func() {
		res, execErr = executeCommandWithContextIDContext(ctx, sc.ID, map[string]string{"command": command})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(45 * time.Second):
		killOrphan()
		t.Fatal("command with an escaped pipe-holding orphan did not return after the group kill")
	}
	if execErr != nil {
		t.Fatal(execErr)
	}
	if res.Error == "" {
		t.Fatalf("timed-out command returned success: %+v", res)
	}
}
