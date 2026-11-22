// Package runtime is the execution plane abstraction for xalgorix tools.
//
// The control plane (agent loop, policy, state authority) stays in the platform
// process; the execution plane is either the platform process itself (native,
// the historical behaviour) or one container per scan ("project"), driven over
// the Docker Engine API or through the runnerd service.
//
// Design rules this package must keep (see AGENTS.md and docs/ARCHITECTURE.md):
//
//   - The security chain is decided by the caller BEFORE an ExecSpec reaches a
//     driver. A driver is not a security boundary and never re-evaluates scope,
//     path policy, credentials, or proxy posture.
//   - Exec is not idempotent. Nothing in this package may retry an Exec; only
//     state transitions (ensure/stop/remove) are safe to repeat.
//   - Container lifecycle is synchronous and cancelable. River only drives
//     cold-path reconciliation (see internal/jobs and reconcile.go); it never
//     owns the scan main loop, dispatch, or approval flow.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Backend identifies which execution plane a Driver implements.
type Backend string

const (
	// BackendNative executes in the platform process (historical behaviour).
	BackendNative Backend = "native"
	// BackendDocker executes in a per-project container.
	BackendDocker Backend = "docker"
)

// Transport selects how the platform reaches the container runtime.
type Transport string

const (
	// TransportDockerSocket talks to /var/run/docker.sock directly (MVP).
	TransportDockerSocket Transport = "socket"
	// TransportRunnerd talks to a host-side runnerd over its own unix socket.
	TransportRunnerd Transport = "runnerd"
)

// ErrorCode is the stable, machine-readable classification of an ExecError.
//
// Only infrastructure failures become Go errors at the tool layer. A command
// that merely exited non-zero, timed out, or hit a missing binary is reported
// as an ExecResult so the registry's circuit breaker is not tripped by
// expected, agent-correctable failures (registry_test.go pins that contract).
type ErrorCode string

const (
	// CodeRuntimeUnavailable: the runtime/transport is unreachable.
	CodeRuntimeUnavailable ErrorCode = "E_RUNTIME_UNAVAILABLE"
	// CodeImageMissing: the requested image digest is absent or untrusted.
	CodeImageMissing ErrorCode = "E_IMAGE_MISSING"
	// CodeProjectLimit: the project container ceiling is reached.
	CodeProjectLimit ErrorCode = "E_PROJECT_LIMIT"
	// CodeLeaseTimeout: a launch waited past the bounded lease window.
	CodeLeaseTimeout ErrorCode = "E_LEASE_TIMEOUT"
	// CodeContainerStart: the container failed to create/start.
	CodeContainerStart ErrorCode = "E_CONTAINER_START"
	// CodeExecSpawn: the runtime could not fork the process.
	CodeExecSpawn ErrorCode = "E_EXEC_SPAWN"
	// CodeNotFound: the binary does not exist inside the execution plane.
	CodeNotFound ErrorCode = "E_NOT_FOUND"
	// CodeTimeout: the command exceeded its deadline.
	CodeTimeout ErrorCode = "E_TIMEOUT"
	// CodeCanceled: the scan or project was canceled.
	CodeCanceled ErrorCode = "E_CANCELED"
	// CodeOutputLimit: output exceeded the configured cap.
	CodeOutputLimit ErrorCode = "E_OUTPUT_LIMIT"
	// CodeSandboxDenied: a path policy denial inside the execution plane.
	CodeSandboxDenied ErrorCode = "E_SANDBOX_DENIED"
	// CodeScopeDenied: the target resolved to the operator machine.
	CodeScopeDenied ErrorCode = "E_SCOPE_DENIED"
	// CodeUnsupported: the backend does not implement the operation.
	CodeUnsupported ErrorCode = "E_UNSUPPORTED"
)

// Phase names the lifecycle stage that produced an error, so operators can tell
// "we never got a container" from "the command ran and failed".
const (
	PhaseEnsure = "ensure"
	PhaseExec   = "exec"
	PhaseStop    = "stop"
	PhaseRemove  = "remove"
	PhaseInspect = "inspect"
	PhaseStream  = "stream"
)

// ExecError is the structured infrastructure failure returned by a Driver.
type ExecError struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
	Phase     string    `json:"phase"`
}

func (e *ExecError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s (%s): %s", e.Code, e.Phase, e.Message)
}

// NewExecError builds an ExecError with a code-derived retryability default.
func NewExecError(code ErrorCode, phase, format string, args ...any) *ExecError {
	return &ExecError{
		Code:      code,
		Phase:     phase,
		Message:   fmt.Sprintf(format, args...),
		Retryable: RetryableCode(code),
	}
}

// RetryableCode reports whether the platform may sensibly retry the operation.
// Exec is never retried by this package; the flag is for the caller's queueing
// decision (e.g. re-ensure after E_CONTAINER_START).
func RetryableCode(code ErrorCode) bool {
	switch code {
	case CodeRuntimeUnavailable, CodeProjectLimit, CodeLeaseTimeout, CodeContainerStart:
		return true
	default:
		return false
	}
}

// CodeOf extracts the ErrorCode from an error chain, or "" when the error is
// not an ExecError.
func CodeOf(err error) ErrorCode {
	var execErr *ExecError
	if errors.As(err, &execErr) {
		return execErr.Code
	}
	return ""
}

// IsCode reports whether err carries the given code.
func IsCode(err error, code ErrorCode) bool { return CodeOf(err) == code }

// CircuitBreakerRelevant reports whether the error should count as a tool
// failure for the registry's circuit breaker. Cancellation and scope denials are
// deliberate operator/engine outcomes, not tool defects, so they are excluded.
func CircuitBreakerRelevant(err error) bool {
	switch CodeOf(err) {
	case CodeCanceled, CodeScopeDenied, CodeSandboxDenied:
		return false
	}
	return err != nil
}

// ResourceLimits is the per-project container budget. Zero values mean
// "use the platform default" (see resources.ProjectBudget).
type ResourceLimits struct {
	MemoryMB  int64 `json:"memory_mb,omitempty"`
	CPUQuota  int64 `json:"cpu_quota,omitempty"`
	CPUPeriod int64 `json:"cpu_period,omitempty"`
	Pids      int64 `json:"pids,omitempty"`
	TmpfsMB   int64 `json:"tmpfs_mb,omitempty"`
	DiskMB    int64 `json:"disk_mb,omitempty"`
}

// Mount mode values accepted by the drivers.
const (
	MountReadOnly  = "ro"
	MountReadWrite = "rw"
)

// Mount is a host→container bind mount. Paths are mounted at the SAME absolute
// path on both sides so sandbox.Policy, notes.json, ledger.json, tool-outputs/
// and traffic/ keep working with zero rewrites.
type Mount struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	Mode      string `json:"mode"`
}

// NetworkSpec describes the per-project network and its only egress path.
type NetworkSpec struct {
	Name       string   `json:"name"`
	Internal   bool     `json:"internal"`
	ProxyURL   string   `json:"proxy_url,omitempty"`
	CACertPath string   `json:"ca_cert,omitempty"`
	EgressCIDRs []string `json:"egress_cidrs,omitempty"`
}

// ProjectSpec is the desired state of one project container.
type ProjectSpec struct {
	ProjectID    string            `json:"project_id"`
	ScanID       string            `json:"scan_id"`
	InstanceID   string            `json:"instance_id"`
	Image        string            `json:"image"`
	Profile      string            `json:"profile"`
	NetProfile   string            `json:"net_profile"`
	Limits       ResourceLimits    `json:"limits"`
	Mounts       []Mount           `json:"mounts"`
	Network      NetworkSpec       `json:"network"`
	WorkDir      string            `json:"work_dir"`
	Env          []string          `json:"env,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	// PublishCDP exposes Chromium's DevTools port on 127.0.0.1 with an ephemeral
	// host port, so the platform can attach over CDP without joining the project
	// network.
	PublishCDP bool `json:"publish_cdp,omitempty"`
	// BrowserProxy is the proxy URL handed to Chromium inside the container.
	// Chromium ignores HTTP_PROXY, so a containerised browser needs the address
	// explicitly; it must be reachable from the project network (not loopback).
	BrowserProxy string `json:"browser_proxy,omitempty"`
}

// ProjectHandle is the result of a successful EnsureProject.
type ProjectHandle struct {
	ProjectID   string    `json:"project_id"`
	ContainerID string    `json:"container_id"`
	State       string    `json:"state"`
	Image       string    `json:"image"`
	CreatedAt   time.Time `json:"created_at"`
}

// ProjectState is the actual state read back from the runtime.
type ProjectState struct {
	ProjectID   string    `json:"project_id"`
	ContainerID string    `json:"container_id"`
	State       string    `json:"state"`
	Image       string    `json:"image"`
	CreatedAt   time.Time `json:"created_at"`
	LastExecAt  time.Time `json:"last_exec_at"`
	Error       string    `json:"error,omitempty"`
}

// Container states surfaced by Inspect (normalised across transports).
const (
	StatePending  = "pending"
	StateStarting = "starting"
	StateReady    = "ready"
	StateBusy     = "busy"
	StateIdle     = "idle"
	StateStopping = "stopping"
	StateStopped  = "stopped"
	StateRemoved  = "removed"
	StateFailed   = "failed"
	StateOrphaned = "orphaned"
)

// Process describes a started command for callers that must register it with
// scan-scoped tracking (OOM score, cancellation, tree kill).
type Process struct {
	PID      int
	KillTree func() error
}

// ExecSpec is one tool invocation. It is fully resolved by the caller: argv is
// final, env is already filtered (SanitEnv) and proxy-injected, cwd is a path
// that exists inside the execution plane.
type ExecSpec struct {
	ProjectID string `json:"project_id"`
	ScanID    string `json:"scan_id"`
	CallSeq   uint64 `json:"call_seq"`

	Tool  string   `json:"tool"`
	Argv  []string `json:"argv"`
	Cwd   string   `json:"cwd"`
	Env   []string `json:"env,omitempty"`
	Stdin []byte   `json:"stdin,omitempty"`

	Timeout     time.Duration `json:"timeout"`
	StdoutLimit int64         `json:"stdout_limit,omitempty"`
	StderrLimit int64         `json:"stderr_limit,omitempty"`
	ArtifactDir string        `json:"artifact_dir,omitempty"`

	Heavy      bool   `json:"heavy"`
	NetProfile string `json:"net_profile,omitempty"`

	// OnStart is invoked once the process is running. Backends that cannot
	// expose a host PID simply do not call it. It must not block.
	OnStart func(Process) `json:"-"`
}

// ExecResult is the outcome of a command. It never carries an infrastructure
// failure: those are returned as *ExecError.
type ExecResult struct {
	CallSeq   uint64 `json:"call_seq"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`

	StdoutTruncated bool `json:"stdout_truncated,omitempty"`
	StderrTruncated bool `json:"stderr_truncated,omitempty"`

	// ArchivePath is the host-side path of the full (untruncated) output when
	// the caller asked for one and the output exceeded the inline cap.
	ArchivePath string `json:"archive_path,omitempty"`

	TimedOut    bool          `json:"timed_out,omitempty"`
	Canceled    bool          `json:"canceled,omitempty"`
	Duration    time.Duration `json:"duration"`
	ContainerID string        `json:"container_id,omitempty"`
}

// Success reports a zero exit with no timeout/cancellation.
func (r *ExecResult) Success() bool {
	return r != nil && r.ExitCode == 0 && !r.TimedOut && !r.Canceled
}

// StreamKind labels a streamed frame.
type StreamKind string

const (
	StreamStdout StreamKind = "stdout"
	StreamStderr StreamKind = "stderr"
)

// StreamFrame is one chunk of live output. Seq is monotonic per Exec and lets a
// caller resynchronise after a dropped transport.
type StreamFrame struct {
	Kind StreamKind `json:"kind"`
	Seq  uint64     `json:"seq"`
	Data string     `json:"data"`
}

// CapacityReport is the read-only capacity projection for the dashboard.
type CapacityReport struct {
	Backend          Backend `json:"backend"`
	Transport        string  `json:"transport,omitempty"`
	ProjectLimit     int     `json:"project_limit"`
	ProjectsActive   int     `json:"projects_active"`
	ProjectsPending  int     `json:"projects_pending"`
	ExecQueueDepth   int     `json:"exec_queue_depth"`
	Image            string  `json:"image,omitempty"`
	ImageDigest      string  `json:"image_digest,omitempty"`
	Profile          string  `json:"profile,omitempty"`
	Reason           string  `json:"reason,omitempty"`
}

// Driver is the execution-plane contract. Implementations must be safe for
// concurrent use across projects.
type Driver interface {
	// Backend identifies the implementation.
	Backend() Backend

	// EnsureProject makes the project container exist and be running. It is
	// idempotent: an existing container for ProjectID is reused, never
	// recreated, unless its image digest differs (then it is replaced).
	EnsureProject(ctx context.Context, spec ProjectSpec) (ProjectHandle, error)

	// Exec runs one command. It never retries.
	Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error)

	// ExecStream is Exec with live frames.
	ExecStream(ctx context.Context, spec ExecSpec, onFrame func(StreamFrame)) (*ExecResult, error)

	// StopProject stops the container (grace period, then kill).
	StopProject(ctx context.Context, projectID string, grace time.Duration) error

	// RemoveProject stops and removes the container plus its network.
	RemoveProject(ctx context.Context, projectID string) error

	// Inspect returns the actual state, or an ExecError with CodeNotFound.
	Inspect(ctx context.Context, projectID string) (ProjectState, error)

	// Capacity reports the current ceiling and usage.
	Capacity(ctx context.Context) (CapacityReport, error)

	// Close releases transport resources. It must not stop projects.
	Close() error
}

// ErrNoDriver is returned when the platform asks for a runtime operation but no
// container backend is configured (native mode).
var ErrNoDriver = errors.New("runtime: no container driver configured")
