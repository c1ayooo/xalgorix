package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/resources"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/iolimit"
)

// Container labels. They are the only way the platform recognises its own
// containers after a restart, so they are part of the contract, not decoration.
const (
	LabelRole     = "xalgorix.role"
	LabelProject  = "xalgorix.project"
	LabelScan     = "xalgorix.scan"
	LabelInstance = "xalgorix.instance"
	LabelProfile  = "xalgorix.profile"
	LabelCreated  = "xalgorix.created_at"

	RoleToolbox = "toolbox"
)

// Image profiles published by the toolbox image.
const (
	ProfileCore = "core"
	ProfileFull = "full"
	ProfileWeb  = "web"
)

// DockerDriver is the per-project container execution plane over the Docker
// Engine API.
type DockerDriver struct {
	client     *dockerClient
	socket     string
	instanceID string
	image      string
	profile    string
	// projectLimit caps concurrently existing project containers. It is always
	// >= 1: a zero/negative configuration is resolved against the host resource
	// budget in NewDockerDriver.
	projectLimit int
	// execLimit bounds simultaneous execs inside one container.
	execLimit int
	// diskEnforce applies DiskMB as the writable-layer StorageOpt.
	diskEnforce bool

	mu       sync.Mutex
	projects map[string]*dockerProject
	// slots is the project ceiling; acquired on create, released on remove.
	slots chan struct{}
	// slotsAligned records that the in-memory project map and the slot tokens
	// have been seeded from the containers that outlived a platform restart.
	// Without this, adopted containers would not count against the cap and a
	// later release could hand their token to a different project.
	slotsAligned bool
	// execSlots is per-driver (not per-project): the container runtime, not the
	// platform, is the real CPU arbiter, and an over-eager fan-out hurts every
	// project on the host.
	execSlots chan struct{}
}

type dockerProject struct {
	id          string
	containerID string
	createdAt   time.Time
	lastExecAt  time.Time
	state       string
	// slotHeld records that this project owns exactly one token in slots, so a
	// removal only ever releases its own.
	slotHeld bool
}

// DockerConfig configures the Docker driver.
type DockerConfig struct {
	SocketPath   string
	InstanceID   string
	Image        string
	Profile      string
	ProjectLimit int
	ExecLimit    int
	DiskEnforce  bool
}

// NewDockerDriver builds the Docker driver and verifies the socket is present.
func NewDockerDriver(cfg DockerConfig) (*DockerDriver, error) {
	image := strings.TrimSpace(cfg.Image)
	if image == "" {
		return nil, errors.New("runtime: toolbox image reference is required")
	}
	if err := validateToolboxImage(image, allowUnpinnedToolbox()); err != nil {
		return nil, err
	}
	socket := strings.TrimSpace(cfg.SocketPath)
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	if !socketExists(socket) {
		return nil, fmt.Errorf("runtime: docker socket %q is not available", socket)
	}
	execLimit := cfg.ExecLimit
	if execLimit <= 0 {
		execLimit = 4
	}
	projectLimit := cfg.ProjectLimit
	if projectLimit <= 0 {
		projectLimit = resolveProjectLimit(projectLimit, resources.CurrentProjectBudget().MaxProjects)
	}
	d := &DockerDriver{
		client:       newDockerClient(socket),
		socket:       socket,
		instanceID:   cfg.InstanceID,
		image:        image,
		profile:      orDefault(cfg.Profile, ProfileCore),
		projectLimit: projectLimit,
		execLimit:    execLimit,
		diskEnforce:  cfg.DiskEnforce,
		projects:     make(map[string]*dockerProject),
		slots:        make(chan struct{}, projectLimit),
		execSlots:    make(chan struct{}, execLimit),
	}
	return d, nil
}

// resolveProjectLimit turns the operator's cap (or its absence) into a concrete
// ceiling. Zero/negative means "derive from the host resource budget", clamped
// to at least one so a project can always run.
func resolveProjectLimit(configured, derived int) int {
	if configured > 0 {
		return configured
	}
	if derived < 1 {
		return 1
	}
	return derived
}

// validateToolboxImage enforces the P0 supply-chain rule: the toolbox image must
// be digest-pinned (repo:tag@sha256:<64 hex>). XALGORIX_ALLOW_UNPINNED_TOOLBOX is
// a local-development escape hatch, not a production posture.
func validateToolboxImage(ref string, allowUnpinned bool) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("runtime: toolbox image reference is required")
	}
	if digest, ok := pinnedDigest(ref); ok {
		if !isHexDigest(digest) {
			return fmt.Errorf("runtime: toolbox image %q has a malformed sha256 digest (want 64 hex chars); pin it as repo:tag@sha256:<64-hex>", ref)
		}
		return nil
	}
	if allowUnpinned {
		return nil
	}
	return fmt.Errorf("runtime: toolbox image %q is not digest-pinned; set XALGORIX_TOOLBOX_IMAGE=repo:tag@sha256:<64-hex> (XALGORIX_ALLOW_UNPINNED_TOOLBOX=1 is for local development only)", ref)
}

// allowUnpinnedToolbox reports whether the development override is enabled.
// Only explicit truthy spellings count; anything else keeps the fail-closed
// digest requirement.
func allowUnpinnedToolbox() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

const digestSeparator = "@sha256:"

func pinnedDigest(ref string) (string, bool) {
	at := strings.Index(ref, digestSeparator)
	if at < 0 {
		return "", false
	}
	return ref[at+len(digestSeparator):], true
}

func isHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// Backend implements Driver.
func (d *DockerDriver) Backend() Backend { return BackendDocker }

// Close implements Driver. The HTTP transport is dropped with the process; no
// containers are touched.
func (d *DockerDriver) Close() error {
	closeIdleConnections(d)
	return nil
}

func closeIdleConnections(d *DockerDriver) {
	if d == nil || d.client == nil {
		return
	}
	if tr, ok := d.client.http.Transport.(interface{ CloseIdleConnections() }); ok {
		tr.CloseIdleConnections()
	}
}

// containerName is the deterministic, idempotent container name for a project.
func containerName(projectID string) string {
	var b strings.Builder
	b.WriteString("xalg-toolbox-")
	for _, r := range projectID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := b.String()
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

func networkName(projectID string) string {
	return "xalg-proj-" + strings.TrimPrefix(containerName(projectID), "xalg-toolbox-")
}

// EnsureProject implements Driver.
func (d *DockerDriver) EnsureProject(ctx context.Context, spec ProjectSpec) (ProjectHandle, error) {
	if spec.ProjectID == "" {
		return ProjectHandle{}, NewExecError(CodeContainerStart, PhaseEnsure, "project id is required")
	}
	// Re-validate at the driver boundary. Under the runnerd transport the
	// platform hands the spec to this driver across an HTTP hop, so this is the
	// last place that can refuse a tag-only image before the daemon runs it.
	if err := validateToolboxImage(spec.Image, allowUnpinnedToolbox()); err != nil {
		return ProjectHandle{}, NewExecError(CodeImageMissing, PhaseEnsure, "%v", err)
	}
	name := containerName(spec.ProjectID)

	// Seed the slot ledger from containers that outlived a platform restart
	// before deciding whether this project fits. Best-effort: when the daemon is
	// unreachable the inspect below reports it.
	d.ensureSlotsAligned(ctx)

	existing, err := d.client.inspectContainer(ctx, name)
	if err == nil {
		if imageMatches(existing.Config.Image, spec.Image) {
			if !existing.State.Running {
				if startErr := d.client.startContainer(ctx, existing.ID); startErr != nil {
					return ProjectHandle{}, NewExecError(CodeContainerStart, PhaseEnsure, "start existing container: %v", startErr)
				}
			}
			// The container already exists and consumes host resources whether
			// or not we count it, so adopt it without failing at the cap.
			d.adoptSlot(spec.ProjectID)
			d.remember(spec.ProjectID, existing.ID, StateReady)
			return ProjectHandle{ProjectID: spec.ProjectID, ContainerID: existing.ID, State: StateReady, Image: spec.Image, CreatedAt: time.Now().UTC()}, nil
		}
		// Image digest changed: replace rather than mutate in place.
		if rmErr := d.removeContainerAndNetwork(ctx, spec.ProjectID, existing.ID); rmErr != nil && !IsCode(rmErr, CodeNotFound) {
			return ProjectHandle{}, rmErr
		}
	} else if !IsCode(err, CodeNotFound) {
		return ProjectHandle{}, NewExecError(CodeRuntimeUnavailable, PhaseEnsure, "inspect %s: %v", name, err)
	}

	if err := d.acquireSlot(spec.ProjectID); err != nil {
		return ProjectHandle{}, err
	}

	if spec.Network.Name != "" {
		if netErr := d.client.createNetwork(ctx, dockerNetworkCreate{
			Name:           spec.Network.Name,
			Driver:         "bridge",
			Internal:       spec.Network.Internal,
			CheckDuplicate: true,
			Labels: map[string]string{
				LabelRole:     RoleToolbox,
				LabelProject:  spec.ProjectID,
				LabelInstance: d.instanceID,
			},
		}); netErr != nil {
			d.releaseSlot(spec.ProjectID)
			return ProjectHandle{}, NewExecError(CodeContainerStart, PhaseEnsure, "create network: %v", netErr)
		}
	}

	labels := map[string]string{
		LabelRole:    RoleToolbox,
		LabelProject: spec.ProjectID,
		LabelScan:    spec.ScanID,
		LabelCreated: time.Now().UTC().Format(time.RFC3339),
	}
	if d.instanceID != "" {
		labels[LabelInstance] = d.instanceID
	}
	labels[LabelProfile] = orDefault(spec.Profile, d.profile)
	for k, v := range spec.Labels {
		labels[k] = v
	}

	binds := make([]string, 0, len(spec.Mounts))
	for _, m := range spec.Mounts {
		mode := m.Mode
		if mode == "" {
			mode = MountReadOnly
		}
		binds = append(binds, m.Host+":"+m.Container+":"+mode)
	}

	exposed := map[string]struct{}{}
	var bindings map[string][]dockerPortBinding
	if spec.PublishCDP {
		// Bind Chromium's DevTools port to loopback only, with an ephemeral host
		// port. Traffic never leaves the host and does not join the project
		// network, so the container keeps its no-route posture.
		exposed[fmt.Sprintf("%d/tcp", cdpContainerPort)] = struct{}{}
		bindings = map[string][]dockerPortBinding{
			fmt.Sprintf("%d/tcp", cdpContainerPort): {{HostIP: "127.0.0.1", HostPort: "0"}},
		}
	}

	hostCfg := dockerHostConfig{
		Binds:          binds,
		NetworkMode:    orDefault(spec.Network.Name, "none"),
		ReadonlyRootfs: true,
		Privileged:     false,
		CapDrop:        []string{"ALL"},
		CapAdd:         capabilitiesFor(spec),
		SecurityOpt:    []string{"no-new-privileges"},
		Tmpfs:          map[string]string{"/tmp": fmt.Sprintf("rw,size=%dm,exec", tmpfsMB(spec.Limits))},
		StorageOpt:     storageOpt(spec.Limits, d.diskEnforce),
		PortBindings:   bindings,
		Resources: dockerResources{
			Memory:    spec.Limits.MemoryMB * 1024 * 1024,
			NanoCPUs:  nanoCPUs(spec.Limits),
			PidsLimit: spec.Limits.Pids,
		},
	}
	if hostCfg.Resources.Memory > 0 {
		// MemorySwap == Memory disables swap for the cgroup, so a runaway tool is
		// OOM-killed inside its own budget instead of pressing the host.
		hostCfg.Resources.MemorySwap = hostCfg.Resources.Memory
	}

	createReq := dockerCreateRequest{
		dockerContainerConfig: dockerContainerConfig{
			Image:        spec.Image,
			Cmd:          []string{"sleep", "infinity"},
			Env:          spec.Env,
			WorkingDir:   orDefault(spec.WorkDir, "/"),
			Labels:       labels,
			Hostname:     "toolbox",
			ExposedPorts: exposed,
		},
		HostConfig: hostCfg,
	}
	containerID, createErr := d.client.createContainer(ctx, name, createReq)
	if createErr != nil {
		d.releaseSlot(spec.ProjectID)
		return ProjectHandle{}, NewExecError(CodeContainerStart, PhaseEnsure, "create container: %v", createErr)
	}
	if startErr := d.client.startContainer(ctx, containerID); startErr != nil {
		_ = d.client.removeContainer(ctx, containerID, true)
		d.releaseSlot(spec.ProjectID)
		return ProjectHandle{}, NewExecError(CodeContainerStart, PhaseEnsure, "start container: %v", startErr)
	}
	d.remember(spec.ProjectID, containerID, StateReady)
	return ProjectHandle{
		ProjectID:   spec.ProjectID,
		ContainerID: containerID,
		State:       StateReady,
		Image:       spec.Image,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

func imageMatches(current, want string) bool {
	if current == "" || want == "" {
		return false
	}
	if current == want {
		return true
	}
	// The daemon reports the local reference; an ID-prefixed match is enough to
	// avoid churn on every EnsureProject call.
	if strings.HasPrefix(current, want) || strings.HasPrefix(want, current) {
		return true
	}
	if at := strings.Index(want, "@sha256:"); at >= 0 {
		digest := want[at+len("@sha256:"):]
		return len(digest) >= 12 && strings.Contains(current, digest[:12])
	}
	return false
}

func capabilitiesFor(spec ProjectSpec) []string {
	if len(spec.Capabilities) > 0 {
		return spec.Capabilities
	}
	switch spec.Profile {
	case ProfileFull:
		return []string{"NET_RAW", "NET_ADMIN"}
	default:
		return nil
	}
}

func nanoCPUs(l ResourceLimits) int64 {
	if l.CPUQuota > 0 && l.CPUPeriod > 0 {
		return int64(math.Round(float64(l.CPUQuota) / float64(l.CPUPeriod) * 1e9))
	}
	return 0
}

func tmpfsMB(l ResourceLimits) int64 {
	if l.TmpfsMB > 0 {
		return l.TmpfsMB
	}
	return 512
}

// storageOpt applies the project's disk budget to the container writable
// layer. It is opt-in because only overlay2 with project quotas honours it;
// other storage drivers reject the container at create time. Bind-mounted scan
// directories are outside the writable layer, so this is not a quota for the
// whole scan directory.
func storageOpt(l ResourceLimits, enforce bool) map[string]string {
	if !enforce || l.DiskMB <= 0 {
		return nil
	}
	return map[string]string{"size": fmt.Sprintf("%dm", l.DiskMB)}
}

func (d *DockerDriver) remember(projectID, containerID, state string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.projectLocked(projectID)
	p.containerID = containerID
	p.state = state
}

// projectLocked returns the bookkeeping entry for projectID, creating it when
// absent. The caller must hold d.mu.
func (d *DockerDriver) projectLocked(projectID string) *dockerProject {
	if p, ok := d.projects[projectID]; ok {
		return p
	}
	p := &dockerProject{id: projectID, createdAt: time.Now().UTC()}
	d.projects[projectID] = p
	return p
}

// acquireSlot reserves a project slot for a container that is about to be
// created. It is idempotent for a project that already owns a token, and fails
// with E_PROJECT_LIMIT when the ceiling is reached.
func (d *DockerDriver) acquireSlot(projectID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := d.projects[projectID]; ok && p.slotHeld {
		return nil
	}
	if d.slots == nil {
		return nil
	}
	select {
	case d.slots <- struct{}{}:
		d.projectLocked(projectID).slotHeld = true
		return nil
	default:
		return NewExecError(CodeProjectLimit, PhaseEnsure,
			"project container limit reached (%d); wait for a scan to finish", cap(d.slots))
	}
}

// adoptSlot accounts for a container that already exists (a restart-adopted
// project or an idempotent EnsureProject hit). It never fails: the container is
// already consuming host resources, so refusing to record it would not free
// anything. When the cap is already full the project simply holds no token.
func (d *DockerDriver) adoptSlot(projectID string) {
	if d.slots == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.projectLocked(projectID)
	if p.slotHeld {
		return
	}
	select {
	case d.slots <- struct{}{}:
		p.slotHeld = true
	default:
	}
}

// releaseSlot returns the token owned by projectID, if any. It must never be
// called for a project that does not hold one: draining the channel blindly
// would hand another project's token back and let the host exceed the cap.
func (d *DockerDriver) releaseSlot(projectID string) {
	if d.slots == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.projects[projectID]
	if !ok || !p.slotHeld {
		return
	}
	select {
	case <-d.slots:
	default:
	}
	p.slotHeld = false
}

// ensureSlotsAligned seeds the slot ledger from the containers this instance
// already owns. Without it, containers adopted after a platform restart are
// invisible to the cap and a later removal could release a token another
// project is using. The work is done once per driver; a list failure leaves the
// flag clear so the next EnsureProject retries.
func (d *DockerDriver) ensureSlotsAligned(ctx context.Context) {
	if d.slots == nil {
		return
	}
	d.mu.Lock()
	if d.slotsAligned {
		d.mu.Unlock()
		return
	}
	d.slotsAligned = true
	d.mu.Unlock()

	projects, err := d.ListProjects(ctx)
	if err != nil {
		d.mu.Lock()
		d.slotsAligned = false
		d.mu.Unlock()
		return
	}
	d.alignSlots(projects)
}

// alignSlots records already-existing containers and grants each as many slot
// tokens as the cap allows, oldest rows first in the order supplied. Projects
// beyond the cap stay unrecorded as token owners so that removing them does not
// release a borrowed token.
func (d *DockerDriver) alignSlots(projects []ProjectState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ps := range projects {
		if ps.ProjectID == "" {
			continue
		}
		p := d.projectLocked(ps.ProjectID)
		if ps.ContainerID != "" {
			p.containerID = ps.ContainerID
		}
		if ps.State != "" {
			p.state = ps.State
		}
		if p.createdAt.IsZero() && !ps.CreatedAt.IsZero() {
			p.createdAt = ps.CreatedAt
		}
		if d.slots == nil || p.slotHeld {
			continue
		}
		select {
		case d.slots <- struct{}{}:
			p.slotHeld = true
		default:
		}
	}
}

func (d *DockerDriver) containerIDFor(ctx context.Context, projectID string) (string, error) {
	d.mu.Lock()
	p, ok := d.projects[projectID]
	d.mu.Unlock()
	if ok && p.containerID != "" {
		return p.containerID, nil
	}
	inspected, err := d.client.inspectContainer(ctx, containerName(projectID))
	if err != nil {
		if IsCode(err, CodeNotFound) {
			return "", NewExecError(CodeNotFound, PhaseExec, "project %q has no container (call EnsureProject first)", projectID)
		}
		return "", NewExecError(CodeRuntimeUnavailable, PhaseExec, "inspect %s: %v", projectID, err)
	}
	d.remember(projectID, inspected.ID, StateReady)
	return inspected.ID, nil
}

// Exec implements Driver.
func (d *DockerDriver) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	return d.ExecStream(ctx, spec, nil)
}

// ExecStream implements Driver.
func (d *DockerDriver) ExecStream(ctx context.Context, spec ExecSpec, onFrame func(StreamFrame)) (*ExecResult, error) {
	if len(spec.Argv) == 0 {
		return nil, NewExecError(CodeExecSpawn, PhaseExec, "empty argv")
	}
	containerID, err := d.containerIDFor(ctx, spec.ProjectID)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Acquire the in-container exec slot. This is the second backpressure layer;
	// it is bounded so a single scan cannot fork-bomb the host through fan-out.
	select {
	case d.execSlots <- struct{}{}:
		defer func() { <-d.execSlots }()
	case <-ctx.Done():
		return nil, NewExecError(CodeCanceled, PhaseExec, "canceled while waiting for an exec slot: %v", ctx.Err())
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	argv := spec.Argv
	// The exec itself cannot be killed through the Docker API, so the deadline is
	// enforced inside the container. `timeout` is present in every toolbox profile.
	argv = withInContainerTimeout(argv, spec.Timeout)

	execID, err := d.client.createExec(runCtx, containerID, dockerExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Env:          spec.Env,
		WorkingDir:   spec.Cwd,
		Cmd:          argv,
	})
	if err != nil {
		if runCtx.Err() != nil {
			return canceledResult(spec, runCtx), nil
		}
		return nil, NewExecError(CodeExecSpawn, PhaseExec, "create exec: %v", err)
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

	started := time.Now()
	streamErr := d.client.startExec(runCtx, execID,
		func(p []byte) {
			_, _ = stdout.Write(p)
			spool.writeStdout(p)
			emit(StreamStdout, p)
		},
		func(p []byte) {
			_, _ = stderr.Write(p)
			spool.writeStderr(p)
			emit(StreamStderr, p)
		},
	)
	d.touchExec(spec.ProjectID)

	result := &ExecResult{
		CallSeq:         spec.CallSeq,
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
		ArchivePath:     spool.finish(),
		Duration:        time.Since(started),
		ContainerID:     containerID,
	}

	if runCtx.Err() != nil {
		result.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		result.Canceled = errors.Is(runCtx.Err(), context.Canceled)
		result.ExitCode = -1
		// The in-container `timeout` wrapper already SIGKILLed the tree; the
		// exec may still report a running state, so do not block on inspect.
		return result, nil
	}
	if streamErr != nil && !errors.Is(streamErr, context.Canceled) {
		return nil, NewExecError(CodeExecSpawn, PhaseStream, "read exec stream: %v", streamErr)
	}

	inspect, inspectErr := d.waitExec(runCtx, execID)
	if inspectErr != nil {
		// The command ran but we could not learn its exit code. Reporting 0 would
		// lie; reporting a hard error would trip the circuit breaker. -1 keeps the
		// "command executed, outcome unknown" distinction the agent can act on.
		result.ExitCode = -1
		if result.Stderr == "" {
			result.Stderr = fmt.Sprintf("[runtime] could not read exec exit status: %v", inspectErr)
		}
		return result, nil
	}
	result.ExitCode = inspect.ExitCode
	if inspect.ExitCode == 127 || inspect.ExitCode == 126 {
		// Docker reports "not found" through the shell/exec exit code; classify it
		// so the tool layer can trigger auto-install instead of a bare failure.
		if strings.Contains(strings.ToLower(result.Stderr), "not found") ||
			strings.Contains(strings.ToLower(result.Stderr), "executable file not found") {
			result.ExitCode = 127
		}
	}
	return result, nil
}

// withInContainerTimeout wraps argv with the in-container deadline. GNU timeout
// parses COMMAND after DURATION, so a `--` separator in that position is taken
// as the command itself ("failed to run command '--'", exit 127). Do not add
// one; far-future deadlines are still bounded by the platform's context.
func withInContainerTimeout(argv []string, timeout time.Duration) []string {
	if timeout <= 0 || len(argv) == 0 {
		return argv
	}
	secs := int(math.Ceil(timeout.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return append([]string{"timeout", "-k", "5", strconv.Itoa(secs)}, argv...)
}

// waitExec polls the exec state until it stops running.
func (d *DockerDriver) waitExec(ctx context.Context, execID string) (dockerExecInspect, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		inspect, err := d.client.inspectExec(ctx, execID)
		if err != nil {
			return inspect, err
		}
		if !inspect.Running {
			return inspect, nil
		}
		if time.Now().After(deadline) {
			return inspect, fmt.Errorf("exec %s still running after 30s", execID)
		}
		select {
		case <-ctx.Done():
			return inspect, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (d *DockerDriver) touchExec(projectID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := d.projects[projectID]; ok {
		p.lastExecAt = time.Now().UTC()
		p.state = StateBusy
	}
}

func canceledResult(spec ExecSpec, ctx context.Context) *ExecResult {
	return &ExecResult{
		CallSeq:  spec.CallSeq,
		ExitCode: -1,
		TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded),
		Canceled: errors.Is(ctx.Err(), context.Canceled),
	}
}

// StopProject implements Driver.
func (d *DockerDriver) StopProject(ctx context.Context, projectID string, grace time.Duration) error {
	containerID, err := d.containerIDFor(ctx, projectID)
	if err != nil {
		return err
	}
	if err := d.client.stopContainer(ctx, containerID, grace); err != nil {
		return NewExecError(CodeContainerStart, PhaseStop, "stop %s: %v", projectID, err)
	}
	d.mu.Lock()
	if p, ok := d.projects[projectID]; ok {
		p.state = StateStopped
	}
	d.mu.Unlock()
	return nil
}

// RemoveProject implements Driver.
func (d *DockerDriver) RemoveProject(ctx context.Context, projectID string) error {
	d.mu.Lock()
	p, ok := d.projects[projectID]
	containerID := ""
	if ok {
		containerID = p.containerID
	}
	d.mu.Unlock()
	if containerID == "" {
		if inspected, err := d.client.inspectContainer(ctx, containerName(projectID)); err == nil {
			containerID = inspected.ID
		} else if IsCode(err, CodeNotFound) {
			d.forget(projectID)
			_ = d.client.removeNetwork(ctx, networkName(projectID))
			return nil
		}
	}
	if err := d.removeContainerAndNetwork(ctx, projectID, containerID); err != nil {
		return err
	}
	return nil
}

func (d *DockerDriver) removeContainerAndNetwork(ctx context.Context, projectID, containerID string) error {
	if containerID != "" {
		if err := d.client.removeContainer(ctx, containerID, true); err != nil && !IsCode(err, CodeNotFound) {
			return NewExecError(CodeContainerStart, PhaseRemove, "remove %s: %v", projectID, err)
		}
	}
	if err := d.client.removeNetwork(ctx, networkName(projectID)); err != nil {
		return NewExecError(CodeContainerStart, PhaseRemove, "remove network: %v", err)
	}
	d.forget(projectID)
	return nil
}

func (d *DockerDriver) forget(projectID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.projects[projectID]
	if !ok {
		return
	}
	// Release only the token this project actually owns. Adopted-over-cap
	// projects hold none, and draining blindly would steal another project's.
	if p.slotHeld && d.slots != nil {
		select {
		case <-d.slots:
		default:
		}
		p.slotHeld = false
	}
	delete(d.projects, projectID)
}

// Inspect implements Driver.
func (d *DockerDriver) Inspect(ctx context.Context, projectID string) (ProjectState, error) {
	inspected, err := d.client.inspectContainer(ctx, containerName(projectID))
	if err != nil {
		return ProjectState{}, err
	}
	state := StateReady
	if !inspected.State.Running {
		state = StateStopped
	}
	d.mu.Lock()
	if p, ok := d.projects[projectID]; ok {
		state = p.state
		p.containerID = inspected.ID
	}
	d.mu.Unlock()
	return ProjectState{
		ProjectID:   projectID,
		ContainerID: inspected.ID,
		State:       state,
		Image:       inspected.Config.Image,
	}, nil
}

// ListProjects implements ProjectLister. It only returns containers belonging to
// this instance: adopting another instance's container would break isolation.
func (d *DockerDriver) ListProjects(ctx context.Context) ([]ProjectState, error) {
	filters := map[string][]string{
		"label": {LabelRole + "=" + RoleToolbox},
	}
	if d.instanceID != "" {
		filters["label"] = append(filters["label"], LabelInstance+"="+d.instanceID)
	}
	containers, err := d.client.listContainers(ctx, filters)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectState, 0, len(containers))
	for _, c := range containers {
		state := StateReady
		switch c.State {
		case "running":
			state = StateReady
		case "exited", "dead", "created":
			state = StateStopped
		}
		out = append(out, ProjectState{
			ProjectID:   c.Labels[LabelProject],
			ContainerID: c.ID,
			State:       state,
			Image:       c.Image,
			CreatedAt:   time.Unix(c.Created, 0).UTC(),
		})
	}
	return out, nil
}

// Capacity implements Driver.
func (d *DockerDriver) Capacity(ctx context.Context) (CapacityReport, error) {
	containers, err := d.ListProjects(ctx)
	if err != nil {
		return CapacityReport{}, err
	}
	active := 0
	for _, c := range containers {
		if c.State == StateReady || c.State == StateBusy || c.State == StateIdle {
			active++
		}
	}
	limit := d.projectLimit
	if limit < 1 {
		limit = 1
	}
	return CapacityReport{
		Backend:        BackendDocker,
		Transport:      string(TransportDockerSocket),
		ProjectLimit:   limit,
		ProjectsActive: active,
		ExecQueueDepth: len(d.execSlots),
		Image:          d.image,
		ImageDigest:    digestOf(d.image),
		Profile:        d.profile,
	}, nil
}

func digestOf(ref string) string {
	if at := strings.Index(ref, "@sha256:"); at >= 0 {
		return "sha256:" + ref[at+len("@sha256:"):]
	}
	return filepath.Base(ref)
}

// PullImage ensures the configured image exists locally.
func (d *DockerDriver) PullImage(ctx context.Context) error {
	ref := d.image
	if err := d.client.pullImage(ctx, ref); err != nil {
		return NewExecError(CodeImageMissing, PhaseEnsure, "pull %s: %v", ref, err)
	}
	return nil
}

// defaultKeepToolboxImages is the retention window used when the caller passes
// a non-positive keep value.
const defaultKeepToolboxImages = 3

// toolboxRepo is the repository basename of the toolbox image. Matching is on
// the whole repository (not a substring), so a look-alike repository such as
// "myorg/xalgorix-toolbox-mirror" can never be collected.
const toolboxRepo = "xalgorix-toolbox"

// GarbageCollectImages removes stale toolbox images, keeping the newest `keep`
// by creation time. Only images whose repository basename is exactly
// "xalgorix-toolbox" are candidates, and an image is never removed while any
// toolbox container on the host still references it (or it is the configured
// image).
func (d *DockerDriver) GarbageCollectImages(ctx context.Context, keep int) error {
	// List everything: the daemon's reference filter would hide images we still
	// need to reason about (and "xalgorix/*" does not match the real GHCR repo).
	images, err := d.client.listImages(ctx, "")
	if err != nil {
		return NewExecError(CodeImageMissing, PhaseEnsure, "list images: %v", err)
	}
	protected := make(map[string]struct{})
	protectImageRef(protected, d.image)
	// Every toolbox container on the host counts, not just this instance's:
	// removing an image another instance is running would break its projects.
	containers, err := d.client.listContainers(ctx, map[string][]string{
		"label": {LabelRole + "=" + RoleToolbox},
	})
	if err != nil {
		// Fail closed: without the in-use set there is no safe deletion.
		return NewExecError(CodeRuntimeUnavailable, PhaseRemove, "gc list containers: %v", err)
	}
	for _, c := range containers {
		protectImageRef(protected, c.Image)
	}
	for _, img := range planImageGarbage(images, keep, protected) {
		if err := d.client.removeImage(ctx, img.ID); err != nil {
			if isImageInUseError(err) {
				// A child image or a container outside our label set still
				// references it; skip rather than fail the whole GC job.
				continue
			}
			return NewExecError(CodeImageMissing, PhaseRemove, "remove image %s: %v", img.ID, err)
		}
	}
	return nil
}

// planImageGarbage selects the images to delete: toolbox images outside the
// newest-N window that are not protected (referenced by a live container or the
// configured image). It is pure so the selection can be tested without a daemon.
func planImageGarbage(images []dockerImageSummary, keep int, protected map[string]struct{}) []dockerImageSummary {
	if keep < 1 {
		keep = defaultKeepToolboxImages
	}
	toolbox := make([]dockerImageSummary, 0, len(images))
	for _, img := range images {
		if isToolboxImage(img) {
			toolbox = append(toolbox, img)
		}
	}
	sort.SliceStable(toolbox, func(i, j int) bool { return toolbox[i].Created > toolbox[j].Created })
	out := make([]dockerImageSummary, 0)
	for i := keep; i < len(toolbox); i++ {
		if imageProtected(toolbox[i], protected) {
			continue
		}
		out = append(out, toolbox[i])
	}
	return out
}

func isToolboxImage(img dockerImageSummary) bool {
	for _, tag := range img.RepoTags {
		if isToolboxRepoTags(tag) {
			return true
		}
	}
	return false
}

// isToolboxRepoTags reports whether a repo tag belongs to the toolbox
// repository. The repository basename must be exactly "xalgorix-toolbox"; a
// tag, digest, or registry port is stripped first.
func isToolboxRepoTags(tag string) bool {
	repo, ok := repoOfRef(tag)
	if !ok {
		return false
	}
	if slash := strings.LastIndex(repo, "/"); slash >= 0 {
		repo = repo[slash+1:]
	}
	return repo == toolboxRepo
}

// repoOfRef strips the tag and digest from an image reference.
func repoOfRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref, ref != ""
}

func protectImageRef(protected map[string]struct{}, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	protected[ref] = struct{}{}
	if digest, ok := pinnedDigest(ref); ok {
		protected["sha256:"+digest] = struct{}{}
	}
	if strings.HasPrefix(ref, "sha256:") {
		protected[ref] = struct{}{}
	}
}

func imageProtected(img dockerImageSummary, protected map[string]struct{}) bool {
	if len(protected) == 0 {
		return false
	}
	if _, ok := protected[img.ID]; ok {
		return true
	}
	if _, ok := protected[strings.TrimPrefix(img.ID, "sha256:")]; ok {
		return true
	}
	for _, tag := range img.RepoTags {
		if _, ok := protected[tag]; ok {
			return true
		}
	}
	return false
}

func isImageInUseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "conflict") || strings.Contains(msg, "being used")
}

// cdpContainerPort is the forwarded, published DevTools port. Chromium itself
// listens on cdpChromiumPort on loopback: modern Chromium ignores
// --remote-debugging-address and binds 127.0.0.1 only, so a published Docker
// port cannot reach it directly. A tiny in-container TCP forwarder bridges
// 0.0.0.0:cdpContainerPort to 127.0.0.1:cdpChromiumPort.
const (
	cdpContainerPort = 9222
	cdpChromiumPort  = 9223
)

// CDPEndpoint ensures Chromium is running inside the project container and
// returns a DevTools websocket URL reachable from the platform.
//
// Chromium is launched detached so it outlives individual browser_action calls
// (the whole point of a per-project container). Launch is idempotent: an already
// listening endpoint is reused.
func (d *DockerDriver) CDPEndpoint(ctx context.Context, projectID string, proxyURL string) (string, error) {
	containerID, err := d.containerIDFor(ctx, projectID)
	if err != nil {
		return "", err
	}
	inspected, err := d.client.inspectContainer(ctx, containerID)
	if err != nil {
		return "", err
	}
	hostPort := cdpHostPort(inspected)
	if hostPort == 0 {
		return "", NewExecError(CodeContainerStart, PhaseExec,
			"project %q was created without a published DevTools port (profile must be %q)", projectID, ProfileWeb)
	}
	if ws, ok := probeCDP(ctx, hostPort); ok {
		return ws, nil
	}

	argv := []string{
		"chromium",
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		// Crashpad needs a writable database; a rootfs-readable project HOME
		// (or a rootless bind mount) otherwise aborts the browser before the
		// DevTools port opens.
		"--disable-crash-reporter",
		"--disable-breakpad",
		"--no-first-run",
		"--remote-debugging-address=127.0.0.1",
		fmt.Sprintf("--remote-debugging-port=%d", cdpChromiumPort),
	}
	if proxyURL != "" {
		// Chromium does not honour HTTP_PROXY: the proxy must be passed as a
		// launch argument, and it must be addressed by a name the project network
		// can resolve (loopback inside the container is the container itself).
		argv = append(argv, "--proxy-server="+proxyURL, "--ignore-certificate-errors")
	}
	// A crashed Chromium leaves SingletonLock in its profile directory and the
	// next launch aborts ("Failed to create a ProcessSingleton"). The container
	// is per-project and /tmp is tmpfs, so a unique profile per launch is
	// cheaper than reaching into the container filesystem to clear the lock.
	argv = append(argv,
		fmt.Sprintf("--user-data-dir=/tmp/chromium-profile-%d", time.Now().UnixNano()),
		"about:blank")

	// /tmp is a writable tmpfs in every profile; the project HOME can be a
	// rootless bind mount that the browser cannot use for its profile.
	env := []string{
		"HOME=/tmp",
		"XDG_CONFIG_HOME=/tmp/.config",
		"XDG_CACHE_HOME=/tmp/.cache",
		"TMPDIR=/tmp",
	}
	execID, err := d.client.createExec(ctx, containerID, dockerExecConfig{Cmd: argv, Env: env})
	if err != nil {
		return "", NewExecError(CodeExecSpawn, PhaseExec, "start chromium: %v", err)
	}
	if err := d.client.startExecDetached(ctx, execID); err != nil {
		return "", NewExecError(CodeExecSpawn, PhaseExec, "start chromium: %v", err)
	}

	// Bridge the published port to Chromium's loopback listener. A stale
	// forwarder from a previous launch may already hold the port; that is fine
	// because it forwards to the same Chromium port, so a bind failure here is
	// not fatal.
	forwarder := fmt.Sprintf(`import socket, threading
def pipe(a, b):
    try:
        while True:
            d = a.recv(65536)
            if not d:
                break
            b.sendall(d)
    except Exception:
        pass
    try:
        b.shutdown(socket.SHUT_WR)
    except Exception:
        pass
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("0.0.0.0", %d))
s.listen(32)
while True:
    c, _ = s.accept()
    try:
        u = socket.create_connection(("127.0.0.1", %d), timeout=5)
    except Exception:
        c.close()
        continue
    threading.Thread(target=pipe, args=(c, u), daemon=True).start()
    threading.Thread(target=pipe, args=(u, c), daemon=True).start()
`, cdpContainerPort, cdpChromiumPort)
	if relayExec, relayErr := d.client.createExec(ctx, containerID, dockerExecConfig{
		Cmd: []string{"python3", "-c", forwarder},
	}); relayErr == nil {
		_ = d.client.startExecDetached(ctx, relayExec)
	}

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if ws, ok := probeCDP(ctx, hostPort); ok {
			return ws, nil
		}
		select {
		case <-ctx.Done():
			return "", NewExecError(CodeCanceled, PhaseExec, "canceled while starting chromium: %v", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	return "", NewExecError(CodeContainerStart, PhaseExec,
		"chromium DevTools endpoint did not become ready on 127.0.0.1:%d within 25s", hostPort)
}

func cdpHostPort(inspected dockerContainerInspect) int {
	key := fmt.Sprintf("%d/tcp", cdpContainerPort)
	for _, binding := range inspected.NetworkSettings.Ports[key] {
		if port, err := strconv.Atoi(binding.HostPort); err == nil && port > 0 {
			return port
		}
	}
	return 0
}

// probeCDP asks the DevTools HTTP endpoint for the browser websocket URL. The
// returned URL carries the container-side port, so it is rewritten to the host
// port the platform can actually reach.
func probeCDP(ctx context.Context, hostPort int) (string, bool) {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", hostPort), nil)
	if err != nil {
		return "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", false
	}
	var version struct {
		WSURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(payload, &version); err != nil || version.WSURL == "" {
		return "", false
	}
	parsed, err := neturl.Parse(version.WSURL)
	if err != nil {
		return "", false
	}
	parsed.Host = fmt.Sprintf("127.0.0.1:%d", hostPort)
	return parsed.String(), true
}

// Stats returns a one-shot memory/CPU sample for a project container.
func (d *DockerDriver) Stats(ctx context.Context, projectID string) (dockerStats, error) {
	containerID, err := d.containerIDFor(ctx, projectID)
	if err != nil {
		return dockerStats{}, err
	}
	return d.client.stats(ctx, containerID)
}
