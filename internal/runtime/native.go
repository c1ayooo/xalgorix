package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/platformutil"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/iolimit"
)

const (
	// pipeDrainGrace bounds how long the readers may hold the drain open after
	// the process was reaped (a setsid escapee can keep the write ends alive).
	pipeDrainGrace = 5 * time.Second
	// nativeMaxArchiveBytes caps the full-output spool written for the agent's
	// reference. Inline output is bounded separately by the caller's limits.
	nativeMaxArchiveBytes = 64 << 20
)

// NativeDriver executes commands in the platform process.
//
// It exists so that (a) tests can exercise the whole dispatch path without a
// container runtime, and (b) tools introduced later have a reference execution
// plane. The historical tools (terminal_execute, python_action, port_scan,
// browser_action) intentionally keep their own local spawn paths when the
// backend is native: those paths carry bespoke behaviour (shell prelude, apt
// auto-install, credential files, RLIMIT guards) whose byte-level parity is an
// explicit acceptance criterion of the runtime migration. Routing them through
// this generic driver would mean re-implementing ~200 lines of delicate
// process/reap logic for no functional gain.
type NativeDriver struct{}

// NewNative returns the in-process driver.
func NewNative() *NativeDriver { return &NativeDriver{} }

// Backend implements Driver.
func (d *NativeDriver) Backend() Backend { return BackendNative }

// EnsureProject implements Driver. Native mode has no project containers; the
// platform process is the project.
func (d *NativeDriver) EnsureProject(_ context.Context, spec ProjectSpec) (ProjectHandle, error) {
	return ProjectHandle{
		ProjectID:   spec.ProjectID,
		ContainerID: "",
		State:       StateReady,
		Image:       "",
		CreatedAt:   time.Now().UTC(),
	}, nil
}

// StopProject implements Driver.
func (d *NativeDriver) StopProject(context.Context, string, time.Duration) error { return nil }

// RemoveProject implements Driver.
func (d *NativeDriver) RemoveProject(context.Context, string) error { return nil }

// Inspect implements Driver.
func (d *NativeDriver) Inspect(_ context.Context, projectID string) (ProjectState, error) {
	return ProjectState{ProjectID: projectID, State: StateReady}, nil
}

// Capacity implements Driver.
func (d *NativeDriver) Capacity(context.Context) (CapacityReport, error) {
	return CapacityReport{Backend: BackendNative, Reason: "native execution plane (no containers)"}, nil
}

// Close implements Driver.
func (d *NativeDriver) Close() error { return nil }

// Exec implements Driver.
func (d *NativeDriver) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	return d.ExecStream(ctx, spec, nil)
}

// ExecStream implements Driver.
func (d *NativeDriver) ExecStream(ctx context.Context, spec ExecSpec, onFrame func(StreamFrame)) (*ExecResult, error) {
	if len(spec.Argv) == 0 {
		return nil, NewExecError(CodeExecSpawn, PhaseExec, "empty argv")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	cmd := exec.CommandContext(runCtx, spec.Argv[0], spec.Argv[1:]...) //nolint:gosec // argv is caller-validated
	cmd.Dir = spec.Cwd
	if len(spec.Env) > 0 {
		cmd.Env = spec.Env
	}
	platformutil.ConfigureProcessGroup(cmd)
	cmd.Cancel = func() error {
		_ = platformutil.KillProcessTree(cmd)
		return os.ErrProcessDone
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, NewExecError(CodeExecSpawn, PhaseExec, "stdout pipe: %v", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, NewExecError(CodeExecSpawn, PhaseExec, "stderr pipe: %v", err)
	}

	started := time.Now()
	if err := cmd.Start(); err != nil {
		code := CodeExecSpawn
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			code = CodeNotFound
		}
		return nil, NewExecError(code, PhaseExec, "start %s: %v", spec.Argv[0], err)
	}
	if spec.OnStart != nil {
		pid := 0
		if cmd.Process != nil {
			pid = cmd.Process.Pid
		}
		spec.OnStart(Process{
			PID:      pid,
			KillTree: func() error { return platformutil.KillProcessTree(cmd) },
		})
	}

	limitOut := int(spec.StdoutLimit)
	if limitOut <= 0 {
		limitOut = iolimit.DefaultStdoutLimit
	}
	limitErr := int(spec.StderrLimit)
	if limitErr <= 0 {
		limitErr = iolimit.DefaultStderrLimit
	}
	stdout := iolimit.New(limitOut)
	stderr := iolimit.New(limitErr)
	spool := newSpool(spec.ArtifactDir, spec.CallSeq, nativeMaxArchiveBytes)

	var seqMu sync.Mutex
	var seq uint64
	emit := func(kind StreamKind, data []byte) {
		if onFrame == nil || len(data) == 0 {
			return
		}
		seqMu.Lock()
		seq++
		frame := StreamFrame{Kind: kind, Seq: seq, Data: string(data)}
		seqMu.Unlock()
		onFrame(frame)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := stdoutPipe.Read(buf)
			if n > 0 {
				_, _ = stdout.Write(buf[:n])
				spool.writeStdout(buf[:n])
				emit(StreamStdout, buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := stderrPipe.Read(buf)
			if n > 0 {
				_, _ = stderr.Write(buf[:n])
				spool.writeStderr(buf[:n])
				emit(StreamStderr, buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Reap concurrently with the readers so a process-group escapee holding the
	// write ends cannot wedge the drain (same reason as the terminal tool).
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	drainDone := make(chan struct{})
	go func() { wg.Wait(); close(drainDone) }()

	awaitDrain := func() {
		timer := time.NewTimer(pipeDrainGrace)
		defer timer.Stop()
		select {
		case <-drainDone:
		case <-timer.C:
			_ = stdoutPipe.Close()
			_ = stderrPipe.Close()
			select {
			case <-drainDone:
			case <-time.After(2 * time.Second):
			}
		}
	}

	var waitErr error
	select {
	case <-drainDone:
		waitErr = <-waitDone
	case waitErr = <-waitDone:
		awaitDrain()
	case <-ctx.Done():
		awaitDrain()
		select {
		case waitErr = <-waitDone:
		case <-time.After(5 * time.Second):
			waitErr = fmt.Errorf("process did not exit after group kill")
		}
	}

	result := &ExecResult{
		CallSeq:         spec.CallSeq,
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
		ArchivePath:     spool.finish(),
		Duration:        time.Since(started),
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		result.Canceled = errors.Is(ctxErr, context.Canceled)
		result.TimedOut = errors.Is(ctxErr, context.DeadlineExceeded)
		result.ExitCode = -1
		return result, nil
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return nil, NewExecError(CodeExecSpawn, PhaseExec, "wait: %v", waitErr)
	}
	result.ExitCode = 0
	return result, nil
}

// spool tees output to a bounded file so the agent can reference the full text
// after the inline cap is hit. A nil spool (no ArtifactDir) accepts writes and
// does nothing, which keeps the hot path allocation-free.
type spool struct {
	mu       sync.Mutex
	stdout   *os.File
	stderr   *os.File
	maxBytes int64
	written  int64
	dropped  bool
	err      error
}

func newSpool(dir string, callSeq uint64, maxBytes int64) *spool {
	s := &spool{maxBytes: maxBytes}
	if dir == "" {
		return s
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		s.err = err
		return s
	}
	open := func(name string) *os.File {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			s.err = err
			return nil
		}
		return f
	}
	s.stdout = open(fmt.Sprintf("%04d.out", callSeq))
	s.stderr = open(fmt.Sprintf("%04d.err", callSeq))
	return s
}

func (s *spool) writeStdout(p []byte) { s.write(s.stdout, p) }
func (s *spool) writeStderr(p []byte) { s.write(s.stderr, p) }

func (s *spool) write(f *os.File, p []byte) {
	if f == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written >= s.maxBytes {
		s.dropped = true
		return
	}
	n := int64(len(p))
	if s.written+n > s.maxBytes {
		p = p[:s.maxBytes-s.written]
		s.dropped = true
	}
	if _, err := f.Write(p); err != nil && s.err == nil {
		s.err = err
	}
	s.written += int64(len(p))
}

// finish closes the spools and returns the stdout archive path when usable.
func (s *spool) finish() string {
	if s == nil || s.stdout == nil {
		return ""
	}
	path := s.stdout.Name()
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.stdout.Close()
	if s.stderr != nil {
		_ = s.stderr.Close()
	}
	if s.err != nil {
		return ""
	}
	return path
}

// readAllLimited is a small helper kept for callers that need to bound a reader
// without allocating an unbounded buffer.
func readAllLimited(r io.Reader, max int64) ([]byte, bool, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, max+1))
	if err != nil {
		return nil, false, err
	}
	if n > max {
		return buf.Bytes()[:max], true, nil
	}
	return buf.Bytes(), false, nil
}
