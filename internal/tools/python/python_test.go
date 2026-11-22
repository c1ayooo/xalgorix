package python

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/resources"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

func TestExecutePython_ValidationErrors(t *testing.T) {
	if _, err := executePython(map[string]string{}); err == nil {
		t.Fatal("missing code did not return an error")
	}

	res, err := executePython(map[string]string{"code": "print('x')", "timeout": "abc"})
	if err != nil {
		t.Fatalf("invalid timeout returned hard error: %v", err)
	}
	if !strings.Contains(res.Error, "invalid timeout") {
		t.Fatalf("invalid timeout result = %#v", res)
	}
}

func TestExecutePython_SuccessAndStderr(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("python"); err != nil {
			t.Skip("python is not installed")
		}
	}

	res, err := executePython(map[string]string{
		"code":    "import sys\nprint('stdout ok')\nprint('stderr ok', file=sys.stderr)",
		"timeout": "5",
	})
	if err != nil {
		t.Fatalf("executePython returned hard error: %v", err)
	}
	if !strings.Contains(res.Output, "stdout ok") || !strings.Contains(res.Output, "STDERR:") || !strings.Contains(res.Output, "stderr ok") {
		t.Fatalf("unexpected python output: %q", res.Output)
	}
}

func TestExecutePython_UsesScanContextWorkDir(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("python"); err != nil {
			t.Skip("python is not installed")
		}
	}

	sc := scanctx.New("python-ctx", t.TempDir())
	sc.Terminal.SetWorkDir(sc.ScanDir)
	scanctx.Activate(sc)
	defer func() {
		scanctx.Deactivate(sc.ID)
	}()

	res, err := executePythonForContext(sc.ID, map[string]string{
		"code":    "import os\nprint(os.getcwd())",
		"timeout": "5",
	})
	if err != nil {
		t.Fatalf("executePythonForContext returned hard error: %v", err)
	}
	if strings.TrimSpace(res.Output) != sc.ScanDir {
		t.Fatalf("python cwd = %q, want %q", strings.TrimSpace(res.Output), sc.ScanDir)
	}
}

func TestExecutePython_RunsInSeparateProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("python"); err != nil {
			t.Skip("python is not installed")
		}
	}

	parentPGID, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatalf("Getpgid(parent): %v", err)
	}

	res, err := executePython(map[string]string{
		"code":    "import os\nprint(os.getpgrp())",
		"timeout": "5",
	})
	if err != nil {
		t.Fatalf("executePython returned hard error: %v", err)
	}
	fields := strings.Fields(res.Output)
	if len(fields) == 0 {
		t.Fatalf("missing process group output: %q", res.Output)
	}
	childPGID, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("parse child process group from %q: %v", res.Output, err)
	}
	if childPGID == parentPGID {
		t.Fatalf("python_action used parent process group %d; cleanup could kill xalgorix", parentPGID)
	}
}

func TestPrependPythonPath(t *testing.T) {
	env := []string{"PATH=/bin", "PYTHONPATH=/existing"}
	got := prependPythonPath(env, "/guard")
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "PYTHONPATH=/guard:/existing") {
		t.Fatalf("guard not prepended to existing PYTHONPATH: %v", got)
	}
	if strings.Count(joined, "PYTHONPATH=") != 1 {
		t.Fatalf("duplicate PYTHONPATH keys: %v", got)
	}

	if got := prependPythonPath(env, ""); strings.Join(got, "\n") != strings.Join(env, "\n") {
		t.Fatalf("empty dir changed env: %v", got)
	}

	got = prependPythonPath([]string{"PATH=/bin"}, "/guard")
	if !strings.Contains(strings.Join(got, "\n"), "PYTHONPATH=/guard") {
		t.Fatalf("guard not added without a prior PYTHONPATH: %v", got)
	}
}

func TestPythonProcessMemoryLimitPrefersTighterCeiling(t *testing.T) {
	old := resources.PythonMemLimitBytes
	t.Cleanup(func() { resources.PythonMemLimitBytes = old })

	const mb = 1024 * 1024
	resources.PythonMemLimitBytes = 2048 * mb
	for _, tc := range []struct {
		lease, want int64
	}{
		{0, 2048 * mb},
		{1024 * mb, 1024 * mb},
		{4096 * mb, 2048 * mb},
	} {
		if got := pythonProcessMemoryLimit(tc.lease); got != tc.want {
			t.Errorf("pythonProcessMemoryLimit(%d) = %d, want %d", tc.lease, got, tc.want)
		}
	}

	resources.PythonMemLimitBytes = 0
	if got := pythonProcessMemoryLimit(1024 * mb); got != 1024*mb {
		t.Errorf("disabled python limit should keep the lease limit, got %d", got)
	}
	if got := pythonProcessMemoryLimit(0); got != 0 {
		t.Errorf("both limits disabled should yield 0, got %d", got)
	}
}

func TestExecutePython_MemoryGuardFailsOversizedScript(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("python"); err != nil {
			t.Skip("python is not installed")
		}
	}

	old := resources.PythonMemLimitBytes
	resources.PythonMemLimitBytes = 256 << 20
	t.Cleanup(func() { resources.PythonMemLimitBytes = old })

	sc := scanctx.New("python-mem-ctx", t.TempDir())
	sc.Terminal.SetWorkDir(sc.ScanDir)
	scanctx.Activate(sc)
	defer scanctx.Deactivate(sc.ID)

	res, err := executePythonForContext(sc.ID, map[string]string{
		"code":    "a = bytearray(512 * 1024 * 1024)\nprint('allocated')",
		"timeout": "10",
	})
	if err != nil {
		t.Fatalf("executePythonForContext returned hard error: %v", err)
	}
	if strings.Contains(res.Output, "allocated") {
		t.Fatalf("script allocated above the ceiling: %q", res.Output)
	}
	if !strings.Contains(res.Output, "MemoryError") {
		t.Fatalf("expected MemoryError from the guard, got %q", res.Output)
	}
}
