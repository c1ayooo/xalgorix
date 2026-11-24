package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	toolrt "github.com/c1ayooo/xalgorix/v4/internal/runtime"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

const webTestToolpack = `schema: 1
tools:
  - name: subfinder
    bin: /root/go/bin/subfinder
    category: recon
    capabilities: []
    profiles: [core]
  - name: nmap
    bin: /usr/bin/nmap
    category: network
    capabilities: [NET_RAW]
    profiles: [full]
  - name: chromium
    bin: /usr/bin/chromium
    category: browser
    capabilities: []
    profiles: [web]
`

type containerTestDriver struct {
	projects   []toolrt.ProjectState
	listErr    error
	inspect    toolrt.ProjectState
	inspectErr error
	stdout     string
	execErr    error
	exitCode   int
}

func (d *containerTestDriver) Backend() toolrt.Backend { return toolrt.BackendDocker }

func (d *containerTestDriver) EnsureProject(_ context.Context, spec toolrt.ProjectSpec) (toolrt.ProjectHandle, error) {
	return toolrt.ProjectHandle{ProjectID: spec.ProjectID, ContainerID: "c-" + spec.ProjectID, State: toolrt.StateReady}, nil
}

func (d *containerTestDriver) Exec(_ context.Context, spec toolrt.ExecSpec) (*toolrt.ExecResult, error) {
	if d.execErr != nil {
		return nil, d.execErr
	}
	return &toolrt.ExecResult{ExitCode: d.exitCode, Stdout: d.stdout}, nil
}

func (d *containerTestDriver) ExecStream(ctx context.Context, spec toolrt.ExecSpec, _ func(toolrt.StreamFrame)) (*toolrt.ExecResult, error) {
	return d.Exec(ctx, spec)
}

func (d *containerTestDriver) StopProject(context.Context, string, time.Duration) error { return nil }
func (d *containerTestDriver) RemoveProject(context.Context, string) error              { return nil }

func (d *containerTestDriver) Inspect(context.Context, string) (toolrt.ProjectState, error) {
	if d.inspectErr != nil {
		return toolrt.ProjectState{}, d.inspectErr
	}
	return d.inspect, nil
}

func (d *containerTestDriver) ListProjects(context.Context) ([]toolrt.ProjectState, error) {
	if d.listErr != nil {
		return nil, d.listErr
	}
	return d.projects, nil
}

func (d *containerTestDriver) Capacity(context.Context) (toolrt.CapacityReport, error) {
	return toolrt.CapacityReport{Backend: toolrt.BackendDocker}, nil
}

func (d *containerTestDriver) Close() error { return nil }

func withContainerRuntime(t *testing.T, drv toolrt.Driver, store toolrt.DesiredStateStore) {
	t.Helper()
	prevDriver := toolrt.Active()
	prevStore := toolrt.StateStore()
	toolrt.SetActive(drv)
	toolrt.SetStateStore(store)
	t.Cleanup(func() {
		toolrt.SetActive(prevDriver)
		toolrt.SetStateStore(prevStore)
	})
}

func TestInstanceProjectIDLocked(t *testing.T) {
	inst := &ScanInstance{scanDir: "/data/target_ab12"}
	if got := instanceProjectIDLocked(inst); got != "target_ab12" {
		t.Fatalf("scanDir project id = %q", got)
	}
	inst.scanDir = ""
	if got := instanceProjectIDLocked(inst); got != "" {
		t.Fatalf("empty instance project id = %q", got)
	}
	inst.sctx = scanctx.New("sess-42", "/data/target_ab12")
	if got := instanceProjectIDLocked(inst); got != "sess-42" {
		t.Fatalf("live project id = %q", got)
	}
}

func TestAttachInstanceContainersJoinsOnePage(t *testing.T) {
	store := toolrt.NewMemoryDesiredState()
	ctx := context.Background()
	for _, row := range []toolrt.DesiredProject{
		{ProjectID: "p1", ScanID: "p1", Profile: "core", DesiredState: toolrt.DesiredReady, Status: toolrt.ReconcileApplied, ImageDigest: "repo@sha256:0123456789abcdef"},
		{ProjectID: "p2", ScanID: "p2", Profile: "web", DesiredState: toolrt.DesiredStopping, Status: toolrt.ReconcileFailed, LastError: "stop timed out"},
	} {
		if _, err := store.RecordProject(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	// RecordProject resets status to pending on first insert; force the rows
	// into the states this test asserts on.
	if err := store.MarkApplied(ctx, "p1", 1, "cid1", toolrt.StateReady); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFailed(ctx, "p2", 1, "stop timed out"); err != nil {
		t.Fatal(err)
	}

	drv := &containerTestDriver{projects: []toolrt.ProjectState{
		{ProjectID: "p1", ContainerID: "cid1", State: toolrt.StateReady, Image: "repo@sha256:fnord"},
	}}
	withContainerRuntime(t, drv, store)

	i1 := &ScanInstance{ID: "i1"}
	i2 := &ScanInstance{ID: "i2"}
	instances := []*ScanInstance{i1, i2}
	projectIDs := map[*ScanInstance]string{i1: "p1", i2: "p2"}
	attachInstanceContainers(ctx, instances, projectIDs, time.Now())

	if i1.Container == nil || i1.Container.State != toolrt.StateReady || i1.Container.ContainerID != "cid1" {
		t.Fatalf("i1 container = %+v", i1.Container)
	}
	if i1.Container.ImageDigest != "sha256:0123456789ab" {
		t.Fatalf("i1 digest = %q", i1.Container.ImageDigest)
	}
	if i1.Container.ReconcileStatus != toolrt.ReconcileApplied || i1.Container.Profile != "core" {
		t.Fatalf("i1 desired projection = %+v", i1.Container)
	}
	if i2.Container == nil || i2.Container.State != containerStateAbsent {
		t.Fatalf("i2 container = %+v", i2.Container)
	}
	if i2.Container.ReconcileStatus != toolrt.ReconcileFailed || i2.Container.LastError != "stop timed out" {
		t.Fatalf("i2 desired projection = %+v", i2.Container)
	}

	// ScanInstance has a custom MarshalJSON; pin that the new field survives it.
	encoded, err := json.Marshal(i1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"container"`) {
		t.Fatalf("marshaled instance dropped the container field: %s", encoded)
	}
}

func TestAttachInstanceContainersDegradesOnListError(t *testing.T) {
	drv := &containerTestDriver{listErr: toolrt.NewExecError(toolrt.CodeRuntimeUnavailable, toolrt.PhaseInspect, "daemon down")}
	withContainerRuntime(t, drv, nil)
	inst := &ScanInstance{ID: "i1"}
	attachInstanceContainers(context.Background(), []*ScanInstance{inst}, map[*ScanInstance]string{inst: "p1"}, time.Now())
	if inst.Container != nil {
		t.Fatalf("container projection must be absent on runtime error, got %+v", inst.Container)
	}
}

func containerTestServer(t *testing.T, inst *ScanInstance) *Server {
	t.Helper()
	s := &Server{instances: map[string]*ScanInstance{}, dataDir: t.TempDir()}
	if inst != nil {
		s.instances[inst.ID] = inst
	}
	return s
}

func TestHandleInstanceContainerDocker(t *testing.T) {
	store := toolrt.NewMemoryDesiredState()
	if _, err := store.RecordProject(context.Background(), toolrt.DesiredProject{
		ProjectID: "target_abc", ScanID: "target_abc", Profile: "core",
		DesiredState: toolrt.DesiredReady,
	}); err != nil {
		t.Fatal(err)
	}
	drv := &containerTestDriver{
		inspect: toolrt.ProjectState{ProjectID: "target_abc", ContainerID: "cid-abc", State: toolrt.StateReady, Image: "repo@sha256:deadbeef"},
		stdout:  webTestToolpack,
	}
	withContainerRuntime(t, drv, store)

	inst := &ScanInstance{ID: "inst-1", Status: "running", scanDir: "/data/target_abc"}
	s := containerTestServer(t, inst)
	rr := httptest.NewRecorder()
	s.handleInstanceContainer(rr, httptest.NewRequest(http.MethodGet, "/api/instances/inst-1/container", nil), "inst-1")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp instanceContainerDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Available || resp.Backend != string(toolrt.BackendDocker) {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Actual == nil || resp.Actual.State != toolrt.StateReady || resp.Actual.ContainerID != "cid-abc" {
		t.Fatalf("actual = %+v", resp.Actual)
	}
	if resp.Desired == nil || resp.Desired.Profile != "core" {
		t.Fatalf("desired = %+v", resp.Desired)
	}
	if !resp.Tools.Available || resp.Tools.Source != toolrt.MountToolpack {
		t.Fatalf("tools = %+v", resp.Tools)
	}
	if len(resp.Tools.Items) != 1 || resp.Tools.Items[0].Name != "subfinder" {
		t.Fatalf("core tools = %+v", resp.Tools.Items)
	}
}

func TestHandleInstanceContainerAbsentKeepsDesiredState(t *testing.T) {
	store := toolrt.NewMemoryDesiredState()
	if _, err := store.RecordProject(context.Background(), toolrt.DesiredProject{
		ProjectID: "target_abc", ScanID: "target_abc", Profile: "core", DesiredState: toolrt.DesiredReady,
	}); err != nil {
		t.Fatal(err)
	}
	drv := &containerTestDriver{
		inspectErr: toolrt.NewExecError(toolrt.CodeNotFound, toolrt.PhaseInspect, "no such container"),
	}
	withContainerRuntime(t, drv, store)

	inst := &ScanInstance{ID: "inst-1", Status: "finished", scanDir: "/data/target_abc"}
	s := containerTestServer(t, inst)
	rr := httptest.NewRecorder()
	s.handleInstanceContainer(rr, httptest.NewRequest(http.MethodGet, "/api/instances/inst-1/container", nil), "inst-1")

	var resp instanceContainerDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Available || resp.Actual == nil || resp.Actual.State != containerStateAbsent {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Desired == nil || resp.Desired.DesiredState != toolrt.DesiredReady {
		t.Fatalf("desired must survive a missing container: %+v", resp.Desired)
	}
	if resp.Tools.Available {
		t.Fatalf("tools must be unavailable, got %+v", resp.Tools)
	}
}

func TestHandleInstanceContainerNative(t *testing.T) {
	withContainerRuntime(t, nil, nil)
	inst := &ScanInstance{ID: "inst-1", Status: "running", scanDir: "/data/target_abc"}
	s := containerTestServer(t, inst)
	rr := httptest.NewRecorder()
	s.handleInstanceContainer(rr, httptest.NewRequest(http.MethodGet, "/api/instances/inst-1/container", nil), "inst-1")

	var resp instanceContainerDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Backend != "native" || resp.Available || resp.Actual != nil {
		t.Fatalf("native resp = %+v", resp)
	}
	if resp.Reason == "" {
		t.Fatal("native response must explain why no container is shown")
	}
}

func TestHandleInstanceContainerNotFound(t *testing.T) {
	withContainerRuntime(t, nil, nil)
	s := containerTestServer(t, nil)
	rr := httptest.NewRecorder()
	s.handleInstanceContainer(rr, httptest.NewRequest(http.MethodGet, "/api/instances/missing/container", nil), "missing")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
