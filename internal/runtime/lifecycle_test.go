package runtime

import (
	"context"
	"testing"
	"time"
)

// lifecycleDriver is a Driver stub for reaping tests. Reaping only needs the
// state transitions (stop/remove) to be observable; nothing here touches a
// container runtime, PostgreSQL, or the network.
type lifecycleDriver struct {
	ensured []string
	stopped []string
	removed []string
}

func (d *lifecycleDriver) Backend() Backend { return BackendDocker }

func (d *lifecycleDriver) EnsureProject(_ context.Context, spec ProjectSpec) (ProjectHandle, error) {
	d.ensured = append(d.ensured, spec.ProjectID)
	return ProjectHandle{ProjectID: spec.ProjectID, ContainerID: "c-" + spec.ProjectID, State: StateReady}, nil
}

func (d *lifecycleDriver) Exec(context.Context, ExecSpec) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (d *lifecycleDriver) ExecStream(_ context.Context, spec ExecSpec, _ func(StreamFrame)) (*ExecResult, error) {
	return &ExecResult{CallSeq: spec.CallSeq}, nil
}

func (d *lifecycleDriver) StopProject(_ context.Context, projectID string, _ time.Duration) error {
	d.stopped = append(d.stopped, projectID)
	return nil
}

func (d *lifecycleDriver) RemoveProject(_ context.Context, projectID string) error {
	d.removed = append(d.removed, projectID)
	return nil
}

func (d *lifecycleDriver) Inspect(_ context.Context, projectID string) (ProjectState, error) {
	return ProjectState{ProjectID: projectID, State: StateReady}, nil
}

func (d *lifecycleDriver) ListProjects(context.Context) ([]ProjectState, error) {
	return nil, nil
}

func (d *lifecycleDriver) Capacity(context.Context) (CapacityReport, error) {
	return CapacityReport{Backend: BackendDocker}, nil
}

func (d *lifecycleDriver) Close() error { return nil }

// readyProject records a running project, marks it applied (the normal hot-path
// state) and pins created_at/updated_at to created. Returning the store lets
// callers resume its clock at "now".
func readyProject(t *testing.T, created time.Time, projectID string) *MemoryDesiredState {
	t.Helper()
	ctx := context.Background()
	store := NewMemoryDesiredState()
	store.now = func() time.Time { return created }
	row, err := store.RecordProject(ctx, DesiredProject{
		ProjectID:    projectID,
		ScanID:       "scan-" + projectID,
		InstanceID:   "inst",
		ImageDigest:  "sha256:deadbeef",
		Profile:      ProfileCore,
		NetProfile:   NetProfileNone,
		DesiredState: DesiredReady,
		Spec:         "{}",
	})
	if err != nil {
		t.Fatalf("record %s: %v", projectID, err)
	}
	if err := store.MarkApplied(ctx, projectID, row.DesiredGen, "c-"+projectID, StateReady); err != nil {
		t.Fatalf("mark applied %s: %v", projectID, err)
	}
	store.now = time.Now
	return store
}

func newReapToolbox(driver Driver, store DesiredStateStore) *Toolbox {
	tb := NewToolbox(driver, store, "inst", "sha256:deadbeef", ProfileCore)
	tb.logf = func(string, ...any) {}
	return tb
}

// TestReapIdleMaxLifeStopsAppliedProject is the regression for the highest-risk
// gap: a normally-applied running project is invisible to Store.Unreconciled,
// so max-life (and idle) reaping must be driven by Store.Active instead.
func TestReapIdleMaxLifeStopsAppliedProject(t *testing.T) {
	ctx := context.Background()
	store := readyProject(t, time.Now().Add(-3*time.Hour).UTC(), "p-max")
	driver := &lifecycleDriver{}
	tb := newReapToolbox(driver, store)
	// Touching makes the project freshly active, so only max life can trigger.
	tb.Touch("p-max")

	if err := tb.ReapIdle(ctx, time.Hour, 30*time.Minute); err != nil {
		t.Fatalf("reap: %v", err)
	}
	got, err := store.Get(ctx, "p-max")
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != DesiredStopping {
		t.Fatalf("desired_state = %q, want %q", got.DesiredState, DesiredStopping)
	}
	if len(driver.stopped) != 1 || driver.stopped[0] != "p-max" {
		t.Fatalf("stopped = %v, want [p-max]", driver.stopped)
	}
}

// TestReapIdleStillReapsIdleAppliedProject pins the existing idle contract on
// the new Active-based view.
func TestReapIdleStillReapsIdleAppliedProject(t *testing.T) {
	ctx := context.Background()
	store := readyProject(t, time.Now().Add(-2*time.Hour).UTC(), "p-idle")
	driver := &lifecycleDriver{}
	tb := newReapToolbox(driver, store)

	if err := tb.ReapIdle(ctx, time.Hour, 24*time.Hour); err != nil {
		t.Fatalf("reap: %v", err)
	}
	got, err := store.Get(ctx, "p-idle")
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != DesiredStopping {
		t.Fatalf("desired_state = %q, want %q", got.DesiredState, DesiredStopping)
	}
}

// TestReapIdleKeepsFreshProject guards against over-reaping: a recent, active
// project stays ready.
func TestReapIdleKeepsFreshProject(t *testing.T) {
	ctx := context.Background()
	store := readyProject(t, time.Now().UTC(), "p-fresh")
	driver := &lifecycleDriver{}
	tb := newReapToolbox(driver, store)
	tb.Touch("p-fresh")

	if err := tb.ReapIdle(ctx, time.Hour, 24*time.Hour); err != nil {
		t.Fatalf("reap: %v", err)
	}
	got, err := store.Get(ctx, "p-fresh")
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != DesiredReady {
		t.Fatalf("desired_state = %q, want %q", got.DesiredState, DesiredReady)
	}
	if len(driver.stopped) != 0 {
		t.Fatalf("stopped = %v, want none", driver.stopped)
	}
}

// TestReapIdleSkipsProjectWithInFlightExec is the regression for killing a
// project mid-exec: a single tool call that outlives the idle TTL has no
// heartbeat, so the in-flight counter is the only activity proof.
func TestReapIdleSkipsProjectWithInFlightExec(t *testing.T) {
	ctx := context.Background()
	store := readyProject(t, time.Now().Add(-2*time.Hour).UTC(), "p-busy")
	driver := &lifecycleDriver{}
	tb := newReapToolbox(driver, store)

	release := tb.BeginExec("p-busy")
	if err := tb.ReapIdle(ctx, time.Hour, 24*time.Hour); err != nil {
		t.Fatalf("reap: %v", err)
	}
	got, err := store.Get(ctx, "p-busy")
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != DesiredReady {
		t.Fatalf("in-flight project was reaped: desired_state = %q", got.DesiredState)
	}
	if len(driver.stopped) != 0 {
		t.Fatalf("stopped = %v, want none", driver.stopped)
	}

	release()
	// Once the exec ends the project becomes eligible again; age the activity
	// marker past the TTL to avoid waiting in real time.
	tb.mu.Lock()
	tb.lastExec["p-busy"] = time.Now().Add(-2 * time.Hour)
	tb.mu.Unlock()
	if err := tb.ReapIdle(ctx, time.Hour, 24*time.Hour); err != nil {
		t.Fatalf("reap after release: %v", err)
	}
	got, err = store.Get(ctx, "p-busy")
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != DesiredStopping {
		t.Fatalf("desired_state after release = %q, want %q", got.DesiredState, DesiredStopping)
	}
}

// TestReapIdleInFlightCountsConcurrentExecs pins the counter protocol: the
// project stays protected until the last exec ends, and a double release does
// not under-count.
func TestReapIdleInFlightCountsConcurrentExecs(t *testing.T) {
	ctx := context.Background()
	store := readyProject(t, time.Now().Add(-2*time.Hour).UTC(), "p-concurrent")
	driver := &lifecycleDriver{}
	tb := newReapToolbox(driver, store)

	releaseA := tb.BeginExec("p-concurrent")
	releaseB := tb.BeginExec("p-concurrent")
	releaseA()
	releaseA() // idempotent: must not drop B's protection

	if err := tb.ReapIdle(ctx, time.Hour, 24*time.Hour); err != nil {
		t.Fatalf("reap: %v", err)
	}
	got, err := store.Get(ctx, "p-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != DesiredReady {
		t.Fatalf("second in-flight exec was ignored: desired_state = %q", got.DesiredState)
	}

	releaseB()
	if tb.executing("p-concurrent") {
		t.Fatal("in-flight counter did not drain")
	}
}

// TestReapIdleMaxLifeBeatsInFlightExec pins the priority contract: the hard
// lifetime cap is not an activity heuristic and still wins over in-flight execs.
func TestReapIdleMaxLifeBeatsInFlightExec(t *testing.T) {
	ctx := context.Background()
	store := readyProject(t, time.Now().Add(-3*time.Hour).UTC(), "p-busy-old")
	driver := &lifecycleDriver{}
	tb := newReapToolbox(driver, store)

	release := tb.BeginExec("p-busy-old")
	defer release()
	if err := tb.ReapIdle(ctx, time.Hour, 30*time.Minute); err != nil {
		t.Fatalf("reap: %v", err)
	}
	got, err := store.Get(ctx, "p-busy-old")
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != DesiredStopping {
		t.Fatalf("max life must stop an over-age project: desired_state = %q", got.DesiredState)
	}
}

// TestBeginExecNilSafe guards the call-site convenience contract.
func TestBeginExecNilSafe(t *testing.T) {
	var tb *Toolbox
	tb.BeginExec("p")() // must not panic
}

// TestReapIdleZeroTTLStillEnforcesMaxLife covers the boundary the plan review
// flagged: XALGORIX_PROJECT_IDLE_TTL_SECONDS=0 must disable only idle reaping,
// never the hard lifetime cap.
func TestReapIdleZeroTTLStillEnforcesMaxLife(t *testing.T) {
	ctx := context.Background()
	store := readyProject(t, time.Now().Add(-2*time.Hour).UTC(), "p-ttl0")
	driver := &lifecycleDriver{}
	tb := newReapToolbox(driver, store)
	tb.Touch("p-ttl0")

	if err := tb.ReapIdle(ctx, 0, time.Hour); err != nil {
		t.Fatalf("reap: %v", err)
	}
	got, err := store.Get(ctx, "p-ttl0")
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != DesiredStopping {
		t.Fatalf("desired_state = %q, want %q", got.DesiredState, DesiredStopping)
	}
}

// TestReapIdleZeroMaxLifeUsesToolboxDefault covers the 0-value path the River
// worker relies on while main.go does not pass MaxLifeSeconds yet.
func TestReapIdleZeroMaxLifeUsesToolboxDefault(t *testing.T) {
	t.Setenv(ProjectMaxLifeEnv, "3600")
	ctx := context.Background()
	store := readyProject(t, time.Now().Add(-2*time.Hour).UTC(), "p-default")
	driver := &lifecycleDriver{}
	tb := newReapToolbox(driver, store)
	if tb.maxLife != time.Hour {
		t.Fatalf("toolbox maxLife = %s, want 1h", tb.maxLife)
	}
	tb.Touch("p-default")

	if err := tb.ReapIdle(ctx, 0, 0); err != nil {
		t.Fatalf("reap: %v", err)
	}
	got, err := store.Get(ctx, "p-default")
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredState != DesiredStopping {
		t.Fatalf("desired_state = %q, want %q", got.DesiredState, DesiredStopping)
	}
}

// TestReapIdleZeroCreatedAtFallsBackToUpdatedAt covers legacy rows without a
// created_at: the age anchor falls back to updated_at, and a row with neither
// timestamp is never hard-reaped.
func TestReapIdleZeroCreatedAtFallsBackToUpdatedAt(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := NewMemoryDesiredState()
	store.mu.Lock()
	store.rows["p-no-created-old"] = DesiredProject{
		ProjectID: "p-no-created-old", ScanID: "s", InstanceID: "inst",
		ImageDigest: "sha256:deadbeef", Profile: ProfileCore, NetProfile: NetProfileNone,
		DesiredState: DesiredReady, DesiredGen: 1, AppliedGen: 1, Status: ReconcileApplied,
		Spec: "{}", UpdatedAt: now.Add(-2 * time.Hour),
	}
	store.rows["p-no-created-fresh"] = DesiredProject{
		ProjectID: "p-no-created-fresh", ScanID: "s", InstanceID: "inst",
		ImageDigest: "sha256:deadbeef", Profile: ProfileCore, NetProfile: NetProfileNone,
		DesiredState: DesiredReady, DesiredGen: 1, AppliedGen: 1, Status: ReconcileApplied,
		Spec: "{}", UpdatedAt: now,
	}
	store.rows["p-no-timestamps"] = DesiredProject{
		ProjectID: "p-no-timestamps", ScanID: "s", InstanceID: "inst",
		ImageDigest: "sha256:deadbeef", Profile: ProfileCore, NetProfile: NetProfileNone,
		DesiredState: DesiredReady, DesiredGen: 1, AppliedGen: 1, Status: ReconcileApplied,
		Spec: "{}",
	}
	store.mu.Unlock()
	driver := &lifecycleDriver{}
	tb := newReapToolbox(driver, store)
	// Fresh activity for every row isolates the max-life branch below.
	tb.Touch("p-no-created-old")
	tb.Touch("p-no-created-fresh")
	tb.Touch("p-no-timestamps")

	if err := tb.ReapIdle(ctx, time.Hour, time.Hour); err != nil {
		t.Fatalf("reap: %v", err)
	}
	cases := map[string]string{
		"p-no-created-old":   DesiredStopping, // updated_at stands in for created_at
		"p-no-created-fresh": DesiredReady,
		"p-no-timestamps":    DesiredReady, // age unknowable: never hard-reap
	}
	for id, want := range cases {
		got, err := store.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.DesiredState != want {
			t.Errorf("%s desired_state = %q, want %q", id, got.DesiredState, want)
		}
	}
}

func TestProjectMaxLifeFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", DefaultProjectMaxLife},
		{"abc", DefaultProjectMaxLife},
		{"0", DefaultProjectMaxLife},
		{"-5", DefaultProjectMaxLife},
		{"120", 120 * time.Second},
		{" 240 ", 240 * time.Second},
	}
	for _, tc := range cases {
		t.Setenv(ProjectMaxLifeEnv, tc.env)
		if got := projectMaxLifeFromEnv(); got != tc.want {
			t.Errorf("projectMaxLifeFromEnv(%q) = %s, want %s", tc.env, got, tc.want)
		}
	}
}

func TestNewToolboxParsesMaxLifeEnv(t *testing.T) {
	t.Setenv(ProjectMaxLifeEnv, "90")
	tb := NewToolbox(nil, nil, "inst", "img", ProfileCore)
	if tb.maxLife != 90*time.Second {
		t.Fatalf("maxLife = %s, want 90s", tb.maxLife)
	}
	// Native mode (nil driver/store) still carries the configured cap.
	if tb.Enabled() {
		t.Fatal("nil driver/store must not enable housekeeping")
	}
}
