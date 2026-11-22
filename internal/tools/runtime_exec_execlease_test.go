package tools

import (
	"context"
	"sync"
	"testing"
	"time"

	toolrt "github.com/c1ayooo/xalgorix/v4/internal/runtime"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// execLeaseDriver blocks inside Exec until the test releases it, so the test can
// observe the project while a tool call is genuinely in flight.
type execLeaseDriver struct {
	started  chan struct{}
	release  chan struct{}
	startOne sync.Once

	mu      sync.Mutex
	stopped []string
}

func (d *execLeaseDriver) Backend() toolrt.Backend { return toolrt.BackendDocker }

func (d *execLeaseDriver) EnsureProject(_ context.Context, spec toolrt.ProjectSpec) (toolrt.ProjectHandle, error) {
	return toolrt.ProjectHandle{
		ProjectID:   spec.ProjectID,
		ContainerID: "c-" + spec.ProjectID,
		State:       toolrt.StateReady,
	}, nil
}

func (d *execLeaseDriver) Exec(ctx context.Context, spec toolrt.ExecSpec) (*toolrt.ExecResult, error) {
	d.startOne.Do(func() { close(d.started) })
	select {
	case <-d.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &toolrt.ExecResult{CallSeq: spec.CallSeq, ExitCode: 0}, nil
}

func (d *execLeaseDriver) ExecStream(ctx context.Context, spec toolrt.ExecSpec, _ func(toolrt.StreamFrame)) (*toolrt.ExecResult, error) {
	return d.Exec(ctx, spec)
}

func (d *execLeaseDriver) StopProject(_ context.Context, projectID string, _ time.Duration) error {
	d.mu.Lock()
	d.stopped = append(d.stopped, projectID)
	d.mu.Unlock()
	return nil
}

func (d *execLeaseDriver) RemoveProject(context.Context, string) error { return nil }

func (d *execLeaseDriver) Inspect(_ context.Context, projectID string) (toolrt.ProjectState, error) {
	return toolrt.ProjectState{ProjectID: projectID, State: toolrt.StateReady}, nil
}

func (d *execLeaseDriver) Capacity(context.Context) (toolrt.CapacityReport, error) {
	return toolrt.CapacityReport{Backend: toolrt.BackendDocker}, nil
}

func (d *execLeaseDriver) Close() error { return nil }

func (d *execLeaseDriver) stops() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.stopped...)
}

// TestRunInProjectHoldsExecLeaseUntilReturn is the regression for "library has
// BeginExec, production call site does not use it": it drives the real
// RunInProject wrapper and asserts that a single tool call outliving the idle
// TTL is not reaped, and that the lease is released once the call returns.
func TestRunInProjectHoldsExecLeaseUntilReturn(t *testing.T) {
	ctx := context.Background()
	sc := &scanctx.ScanContext{ID: "exec-lease", ScanDir: t.TempDir()}

	driver := &execLeaseDriver{started: make(chan struct{}), release: make(chan struct{})}
	store := toolrt.NewMemoryDesiredState()
	row, err := store.RecordProject(ctx, toolrt.DesiredProject{
		ProjectID:    sc.ID,
		ScanID:       sc.ID,
		InstanceID:   "inst",
		ImageDigest:  "sha256:deadbeef",
		Profile:      toolrt.ProfileCore,
		NetProfile:   toolrt.NetProfileNone,
		DesiredState: toolrt.DesiredReady,
		Spec:         "{}",
	})
	if err != nil {
		t.Fatalf("record project: %v", err)
	}
	if err := store.MarkApplied(ctx, sc.ID, row.DesiredGen, "c-"+sc.ID, toolrt.StateReady); err != nil {
		t.Fatalf("mark applied: %v", err)
	}

	tb := toolrt.NewToolbox(driver, store, "inst", "sha256:deadbeef", toolrt.ProfileCore)
	toolrt.SetActive(driver)
	toolrt.SetToolbox(tb)
	// Seed the ensure cache so EnsureScanProject does not rebuild a spec from
	// ambient config; this test targets the exec lease, not spec assembly.
	ensuredMu.Lock()
	ensured[sc.ID] = ensuredProject{at: time.Now()}
	ensuredMu.Unlock()
	defer func() {
		invalidateProject(sc.ID)
		toolrt.SetToolbox(nil)
		toolrt.SetActive(nil)
	}()

	done := make(chan error, 1)
	go func() {
		_, execErr := RunInProject(ctx, sc, toolrt.ExecSpec{
			Tool:    "terminal",
			Argv:    []string{"true"},
			Timeout: time.Minute,
		})
		done <- execErr
	}()

	select {
	case <-driver.started:
	case <-time.After(10 * time.Second):
		t.Fatal("exec did not start")
	}

	// Let the simulated command outlive the idle TTL by orders of magnitude.
	time.Sleep(20 * time.Millisecond)
	if err := tb.ReapIdle(ctx, time.Millisecond, 24*time.Hour); err != nil {
		t.Fatalf("reap during exec: %v", err)
	}
	got, err := store.Get(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != toolrt.DesiredReady {
		t.Fatalf("project reaped mid-exec: desired_state = %q", got.DesiredState)
	}
	if stops := driver.stops(); len(stops) != 0 {
		t.Fatalf("container stopped mid-exec: %v", stops)
	}

	close(driver.release)
	if err := <-done; err != nil {
		t.Fatalf("RunInProject: %v", err)
	}

	// The lease must be released when the call returns: the same idle TTL now
	// reaps the project.
	time.Sleep(20 * time.Millisecond)
	if err := tb.ReapIdle(ctx, time.Millisecond, 24*time.Hour); err != nil {
		t.Fatalf("reap after exec: %v", err)
	}
	got, err = store.Get(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != toolrt.DesiredStopping {
		t.Fatalf("exec lease not released: desired_state = %q, want %q", got.DesiredState, toolrt.DesiredStopping)
	}
}
