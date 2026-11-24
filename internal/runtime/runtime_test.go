package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildMountsKeepsSamePathAndReadOnlyShared(t *testing.T) {
	scanDir := t.TempDir()
	shared := t.TempDir()
	if err := os.MkdirAll(filepath.Join(shared, "wordlists"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "wordlists", "common.txt"), []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(shared, "ca"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "ca", "ca.pem"), []byte("test-ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildMounts(scanDir, shared)
	if err != nil {
		t.Fatal(err)
	}
	absScan, _ := filepath.Abs(scanDir)
	if len(plan.Mounts) != 4 {
		t.Fatalf("mounts = %d, want 4 (%+v)", len(plan.Mounts), plan.Mounts)
	}
	if plan.Mounts[0].Host != absScan || plan.Mounts[0].Container != absScan {
		t.Fatalf("scan mount must be same-path: %+v", plan.Mounts[0])
	}
	if plan.Mounts[0].Mode != MountReadWrite {
		t.Fatalf("scan mount must be rw, got %q", plan.Mounts[0].Mode)
	}
	for _, m := range plan.Mounts[1:] {
		wantRo := m.Container != MountCache
		if wantRo && m.Mode != MountReadOnly {
			t.Errorf("shared mount %q must be ro, got %q", m.Container, m.Mode)
		}
		if !wantRo && m.Mode != MountReadWrite {
			t.Errorf("install cache %q must be rw, got %q", m.Container, m.Mode)
		}
	}
	if !plan.WritablePath(filepath.Join(absScan, "tool-outputs")) {
		t.Fatal("scan dir must be writable")
	}
	if plan.WritablePath("/etc/passwd") {
		t.Fatal("/etc/passwd must not be writable")
	}
	if _, err := plan.ContainerPath("/etc/passwd"); err == nil {
		t.Fatal("path outside mounts must be rejected")
	}
}

func TestBuildMountsSkipsEmptyWordlistsAndMissingCA(t *testing.T) {
	scanDir := t.TempDir()
	shared := t.TempDir()
	if err := os.MkdirAll(filepath.Join(shared, "wordlists"), 0o750); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildMounts(scanDir, shared)
	if err != nil {
		t.Fatal(err)
	}
	targets := make(map[string]bool, len(plan.Mounts))
	for _, m := range plan.Mounts {
		targets[m.Container] = true
	}
	if targets[MountWordlists] {
		t.Fatal("an empty wordlists directory must not shadow the image's baked wordlists")
	}
	if targets[MountCACert] {
		t.Fatal("a missing CA file must not be bind-mounted")
	}
	if !targets[MountCache] {
		t.Fatal("the writable install cache must still be mounted")
	}
}

func TestValidateMountsRejectsPathRewrite(t *testing.T) {
	err := ValidateMounts([]Mount{{Host: "/tmp/a", Container: "/data/b", Mode: MountReadWrite}})
	if err == nil {
		t.Fatal("business mounts must keep the same path")
	}
	if err := ValidateMounts([]Mount{{Host: "/data/w", Container: MountWordlists, Mode: MountReadOnly}}); err != nil {
		t.Fatalf("shared asset mount must be allowed: %v", err)
	}
	if err := ValidateMounts([]Mount{{Host: "/", Container: "/", Mode: MountReadWrite}}); err == nil {
		t.Fatal("mounting over / must be rejected")
	}
}

// TestValidateMountsRefusesDockerSocket pins the red line that tool
// containers never see the host docker daemon — BuildMounts constructs the
// mount set, and ValidateMounts is the choke point that must keep refusing a
// docker socket bind even if a future caller hand-crafts one.
func TestValidateMountsRefusesDockerSocket(t *testing.T) {
	cases := []Mount{
		{Host: "/var/run/docker.sock", Container: "/var/run/docker.sock", Mode: MountReadWrite},
		{Host: "/var/run/docker.sock", Container: "/var/run/docker.sock", Mode: MountReadOnly},
		{Host: "/run/containerd/docker.sock", Container: "/var/run/docker.sock", Mode: MountReadWrite},
		{Host: "/tmp/side/docker.sock", Container: "/tmp/side/docker.sock", Mode: MountReadWrite},
	}
	for _, m := range cases {
		if err := ValidateMounts([]Mount{m}); err == nil {
			t.Fatalf("docker socket mount must be rejected: %+v", m)
		}
	}
	// Unrelated sockets and scan dirs are unaffected.
	if err := ValidateMounts([]Mount{{Host: "/tmp/x/other.sock", Container: "/tmp/x/other.sock", Mode: MountReadWrite}}); err != nil {
		t.Fatalf("non-docker socket mount must be allowed: %v", err)
	}
}

func TestNativeDriverExecCapturesExitAndOutput(t *testing.T) {
	d := NewNative()
	res, err := d.Exec(context.Background(), ExecSpec{
		ProjectID: "p1",
		CallSeq:   7,
		Argv:      []string{"sh", "-c", "echo out; echo err >&2; exit 3"},
		Cwd:       t.TempDir(),
		Timeout:   10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "out") || !strings.Contains(res.Stderr, "err") {
		t.Fatalf("stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	if res.Success() {
		t.Fatal("non-zero exit must not be reported as success")
	}
}

func TestNativeDriverTimeoutIsResultNotError(t *testing.T) {
	d := NewNative()
	res, err := d.Exec(context.Background(), ExecSpec{
		ProjectID: "p1",
		Argv:      []string{"sh", "-c", "sleep 5"},
		Cwd:       t.TempDir(),
		Timeout:   200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("timeout must be a result, not an infrastructure error: %v", err)
	}
	if !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("timedOut=%v exit=%d", res.TimedOut, res.ExitCode)
	}
}

func TestNativeDriverArchivesFullOutput(t *testing.T) {
	dir := t.TempDir()
	d := NewNative()
	res, err := d.Exec(context.Background(), ExecSpec{
		ProjectID:   "p1",
		CallSeq:     12,
		Argv:        []string{"sh", "-c", "printf 'abcdefghij'"},
		Cwd:         dir,
		StdoutLimit: 4,
		ArtifactDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.StdoutTruncated {
		t.Fatal("stdout must be flagged truncated")
	}
	if res.Stdout != "abcd" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if res.ArchivePath == "" {
		t.Fatal("archive path must be set")
	}
	data, readErr := os.ReadFile(res.ArchivePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "abcdefghij" {
		t.Fatalf("archive = %q", string(data))
	}
}

func TestNativeDriverMissingBinaryIsTypedError(t *testing.T) {
	d := NewNative()
	_, err := d.Exec(context.Background(), ExecSpec{
		ProjectID: "p1",
		Argv:      []string{"definitely-not-a-real-binary-xyz"},
		Cwd:       t.TempDir(),
	})
	if !IsCode(err, CodeNotFound) {
		t.Fatalf("code = %q, want %q (err=%v)", CodeOf(err), CodeNotFound, err)
	}
}

func TestReconcilerAppliesAndDeletes(t *testing.T) {
	store := NewMemoryDesiredState()
	spec := ProjectSpec{ProjectID: "scan-a", ScanID: "scan-a", Image: "img@sha256:abc"}
	encoded := mustJSON(t, spec)
	if _, err := store.RecordProject(context.Background(), DesiredProject{
		ProjectID: "scan-a", ScanID: "scan-a", ImageDigest: "img@sha256:abc",
		DesiredState: DesiredPending, Spec: encoded,
	}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeDriver{}
	rec := NewReconciler(fake, store)
	rec.SetLogger(func(string, ...any) {})
	if err := rec.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.ensured) != 1 {
		t.Fatalf("ensured = %v", fake.ensured)
	}
	row, err := store.Get(context.Background(), "scan-a")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != ReconcileApplied || row.AppliedGen != row.DesiredGen {
		t.Fatalf("row = %+v", row)
	}

	if _, err := store.RecordProject(context.Background(), DesiredProject{
		ProjectID: "scan-a", ScanID: "scan-a", ImageDigest: "img@sha256:abc",
		DesiredState: DesiredRemoved, Spec: encoded,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rec.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.removed) != 1 {
		t.Fatalf("removed = %v", fake.removed)
	}
	if _, err := store.Get(context.Background(), "scan-a"); err == nil {
		t.Fatal("row must be deleted after removal")
	}
}

func TestOrphanSweeperOnlyReclaimsUnknownProjects(t *testing.T) {
	store := NewMemoryDesiredState()
	if _, err := store.RecordProject(context.Background(), DesiredProject{
		ProjectID: "known", DesiredState: DesiredPending,
	}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeDriver{listed: []ProjectState{
		{ProjectID: "known", ContainerID: "c1"},
		{ProjectID: "orphan", ContainerID: "c2"},
	}}
	sweeper := NewOrphanSweeper(fake, store, "inst")
	sweeper.logf = func(string, ...any) {}
	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.removed) != 1 || fake.removed[0] != "orphan" {
		t.Fatalf("removed = %v", fake.removed)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

type fakeDriver struct {
	ensured     []string
	removed     []string
	listed      []ProjectState
	cdpEndpoint string
}

func (f *fakeDriver) Backend() Backend { return BackendDocker }

func (f *fakeDriver) EnsureProject(_ context.Context, spec ProjectSpec) (ProjectHandle, error) {
	f.ensured = append(f.ensured, spec.ProjectID)
	return ProjectHandle{ProjectID: spec.ProjectID, ContainerID: "c-" + spec.ProjectID, State: StateReady}, nil
}

func (f *fakeDriver) Exec(context.Context, ExecSpec) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (f *fakeDriver) ExecStream(_ context.Context, spec ExecSpec, _ func(StreamFrame)) (*ExecResult, error) {
	return &ExecResult{CallSeq: spec.CallSeq}, nil
}

func (f *fakeDriver) StopProject(_ context.Context, projectID string, _ time.Duration) error {
	return nil
}

func (f *fakeDriver) RemoveProject(_ context.Context, projectID string) error {
	f.removed = append(f.removed, projectID)
	return nil
}

func (f *fakeDriver) Inspect(_ context.Context, projectID string) (ProjectState, error) {
	return ProjectState{ProjectID: projectID, State: StateReady}, nil
}

func (f *fakeDriver) ListProjects(context.Context) ([]ProjectState, error) { return f.listed, nil }

func (f *fakeDriver) Capacity(context.Context) (CapacityReport, error) {
	return CapacityReport{Backend: BackendDocker}, nil
}

func (f *fakeDriver) Close() error { return nil }

func (f *fakeDriver) CDPEndpoint(context.Context, string, string) (string, error) {
	return f.cdpEndpoint, nil
}
