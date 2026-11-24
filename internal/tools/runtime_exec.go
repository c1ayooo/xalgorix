package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/resources"
	toolrt "github.com/c1ayooo/xalgorix/v4/internal/runtime"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/storage"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// Container execution routing.
//
// Tools call EnsureScanProject/RunInProject; when no container driver is active
// these return (nil, nil) and the caller keeps its historical local path. That
// split is deliberate: the native implementations carry behaviour (shell prelude,
// apt auto-install, credential files, RLIMIT guards) whose byte-level parity is
// an acceptance criterion, so they are preserved rather than re-implemented.

// ContainerActive reports whether tools should execute in a project container.
func ContainerActive() bool { return toolrt.ActiveContainer() != nil }

type ensuredProject struct {
	at time.Time
	// proxyURL is the container-reachable per-run proxy URL reported by the
	// runtime (project-network gateway + run-scoped listener port). The
	// containerised browser needs it for --proxy-server; empty when the
	// runtime did not report one.
	proxyURL string
}

var (
	ensuredMu sync.Mutex
	ensured   = make(map[string]ensuredProject)
)

// ensureCacheTTL bounds how often EnsureProject is re-validated on the hot path.
// The driver's EnsureProject is idempotent (it inspects by name), but doing that
// on every single tool call would add a daemon round-trip to each command.
const ensureCacheTTL = 2 * time.Minute

// EnsureScanProject makes the scan's project container exist and returns its
// driver. It returns (nil, nil) when the platform runs in native mode.
func EnsureScanProject(ctx context.Context, sc *scanctx.ScanContext) (toolrt.Driver, error) {
	drv := toolrt.ActiveContainer()
	if drv == nil {
		return nil, nil
	}
	if sc == nil || strings.TrimSpace(sc.ID) == "" {
		return nil, fmt.Errorf("tools: scan context is required for container execution")
	}
	ensuredMu.Lock()
	cached, ok := ensured[sc.ID]
	ensuredMu.Unlock()
	if ok && time.Since(cached.at) < ensureCacheTTL {
		return drv, nil
	}

	cfg := config.Get()
	spec, err := buildProjectSpec(sc, cfg)
	if err != nil {
		return nil, err
	}

	// Persist intent before creating the container: a crash between the two must
	// leave a durable desired state (the reconciler finishes the job).
	if store := toolrt.StateStore(); store != nil {
		encoded, encErr := json.Marshal(spec)
		if encErr == nil {
			if _, recErr := store.RecordProject(ctx, toolrt.DesiredProject{
				ProjectID:    spec.ProjectID,
				ScanID:       spec.ScanID,
				InstanceID:   spec.InstanceID,
				ImageDigest:  spec.Image,
				Profile:      spec.Profile,
				NetProfile:   spec.NetProfile,
				DesiredState: toolrt.DesiredReady,
				Spec:         string(encoded),
			}); recErr != nil {
				log.Printf("[runtime] record desired state for %s: %v", sc.ID, recErr)
			}
		}
	}

	handle, err := drv.EnsureProject(ctx, spec)
	if err != nil {
		return nil, err
	}
	if store := toolrt.StateStore(); store != nil {
		row, getErr := store.Get(ctx, spec.ProjectID)
		if getErr == nil {
			if err := store.MarkApplied(ctx, spec.ProjectID, row.DesiredGen, handle.ContainerID, handle.State); err != nil {
				log.Printf("[runtime] mark applied for %s: %v", sc.ID, err)
			}
		}
	}
	ensuredMu.Lock()
	ensured[sc.ID] = ensuredProject{at: time.Now(), proxyURL: handle.ProxyURL}
	ensuredMu.Unlock()
	return drv, nil
}

// ScanProjectProxyURL reports the container-reachable per-run proxy URL the
// runtime resolved for a scan ("" when none). The containerised browser uses it
// as --proxy-server so its traffic is attributed by the run-scoped listener.
func ScanProjectProxyURL(scanID string) string {
	ensuredMu.Lock()
	defer ensuredMu.Unlock()
	return ensured[scanID].proxyURL
}

// invalidateProject drops the ensure cache so the next call re-creates.
func invalidateProject(scanID string) {
	ensuredMu.Lock()
	delete(ensured, scanID)
	ensuredMu.Unlock()
}

// cdpProvider is implemented by drivers that can host a Chromium DevTools
// endpoint inside the project container.
type cdpProvider interface {
	CDPEndpoint(ctx context.Context, projectID, proxyURL string) (string, error)
}

// ProjectCDPURL ensures Chromium is running in the scan's project container and
// returns the websocket URL the platform attaches to.
func ProjectCDPURL(ctx context.Context, sc *scanctx.ScanContext) (string, error) {
	drv, err := EnsureScanProject(ctx, sc)
	if err != nil {
		return "", err
	}
	if drv == nil {
		return "", toolrt.ErrNoDriver
	}
	provider, ok := drv.(cdpProvider)
	if !ok {
		return "", fmt.Errorf("tools: the active runtime cannot host a CDP endpoint")
	}
	proxyURL := strings.TrimSpace(config.Get().ToolboxBrowserProxy)
	if proxyURL == "" {
		// Prefer the container-reachable URL of this run's dedicated proxy
		// listener: it attributes browser traffic without putting a token in
		// Chromium's process arguments or the persisted project spec.
		proxyURL = ScanProjectProxyURL(sc.ID)
	}
	if proxyURL == "" {
		if config.Get().ProxyRequired || trafficproxy.Enabled() {
			// Fail closed: a containerised browser that cannot be told about the
			// proxy would dial the target directly, bypassing observation and
			// attribution.
			return "", fmt.Errorf("tools: XALGORIX_TOOLBOX_BROWSER_PROXY must be set to a container-reachable proxy URL when browser traffic is proxied")
		}
		log.Printf("[runtime] scan %s: browser project has no proxy configured; Chromium will dial directly", sc.ID)
	}
	if tb := toolrt.ActiveToolbox(); tb != nil {
		tb.Touch(sc.ID)
	}
	return provider.CDPEndpoint(ctx, sc.ID, proxyURL)
}

// egressPlan is the resolved network egress for a project container.
type egressPlan struct {
	NetProfile string
	ProxyURL   string
	// ProxyGatewayPort is set when the proxy is addressed through the project
	// network's gateway (the platform proxy listens on 0.0.0.0).
	ProxyGatewayPort int
	CACertPath       string
}

// resolveEgress decides how a project container reaches the network.
//
// The traffic proxy binds a dedicated per-run listener on the configured bind
// host (ephemeral port), so container traffic arrives on a port that already
// implies the run: no bearer token needs to ride the container environment or
// the persisted project spec, and browser/terminal/Python traffic is still
// attributed and archived. A loopback bind is unreachable from an --internal
// project network, so the bind host must be unspecified (0.0.0.0/::). An
// explicitly configured XALGORIX_TOOLBOX_PROXY_URL still wins and selects the
// proxy-only network even when nothing else demands a proxy.
func resolveEgress(scanCtxID string, proxyRequired, trafficProxyEnabled bool, toolboxProxyURL, trafficProxyBind, caPath string) (egressPlan, error) {
	proxyURL := strings.TrimSpace(toolboxProxyURL)
	if proxyURL == "" && (proxyRequired || trafficProxyEnabled) {
		// The platform proxy serves the project network through its gateway:
		// bind the run's listener on 0.0.0.0 and address it at the project
		// network's gateway.
		if !bindReachesProjectGateway(trafficProxyBind) {
			return egressPlan{}, fmt.Errorf("tools: the traffic proxy is required but not container-reachable: set XALGORIX_TRAFFICPROXY_BIND=0.0.0.0:<port>, or set XALGORIX_TOOLBOX_PROXY_URL to a project-network-reachable proxy; refusing to fall back to direct egress")
		}
		if _, err := trafficproxy.ListenForScan(scanCtxID); err != nil {
			return egressPlan{}, fmt.Errorf("tools: bind the run-scoped traffic proxy listener: %w", err)
		}
		port, ok := trafficproxy.ScanListenerPort(scanCtxID)
		if !ok || port <= 0 {
			return egressPlan{}, fmt.Errorf("tools: the run-scoped traffic proxy listener has no port")
		}
		return egressPlan{
			NetProfile:       toolrt.NetProfileProxyOnly,
			ProxyGatewayPort: port,
			CACertPath:       caPath,
		}, nil
	}
	if proxyURL != "" {
		return egressPlan{NetProfile: toolrt.NetProfileProxyOnly, ProxyURL: proxyURL, CACertPath: caPath}, nil
	}
	return egressPlan{NetProfile: toolrt.NetProfileDirect}, nil
}

// bindReachesProjectGateway reports whether the configured traffic-proxy bind
// host is reachable from an --internal project network (only an unspecified
// host is; a loopback or specific-interface bind is not).
func bindReachesProjectGateway(bind string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(bind))
	if err != nil {
		return false
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return true
	default:
		return false
	}
}

// prepareSharedAssets materialises the shared mount root and makes the
// traffic-proxy CA available at the exact path the container mounts.
//
// The mount plan publishes the CA at <DataDir>/shared/ca/ca.pem, but the
// traffic proxy keeps its authoritative copy under ~/.xalgorix/ca. Without this
// copy the mount source is missing (Docker would create a directory at the
// file path) and the SSL_CERT_FILE handed to the container would point at a
// path that is not mounted. The cache directory is created because the image
// rootfs is read-only and the toolchain cache must be writable.
func prepareSharedAssets(dataDir, caPath string) (sharedRoot, mountedCA string, err error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", "", nil
	}
	sharedRoot = filepath.Join(dataDir, "shared")
	if err := os.MkdirAll(filepath.Join(sharedRoot, "cache"), 0o750); err != nil {
		return "", "", fmt.Errorf("tools: create shared cache dir: %w", err)
	}
	if strings.TrimSpace(caPath) == "" {
		return sharedRoot, "", nil
	}
	pem, readErr := os.ReadFile(caPath)
	if readErr != nil {
		log.Printf("[runtime] shared CA unavailable at %s: %v", caPath, readErr)
		return sharedRoot, "", nil
	}
	dest := filepath.Join(sharedRoot, "ca", "ca.pem")
	if existing, err := os.ReadFile(dest); err == nil && bytes.Equal(existing, pem) {
		return sharedRoot, dest, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", "", fmt.Errorf("tools: create shared CA dir: %w", err)
	}
	if err := storage.WriteAtomic(dest, pem); err != nil {
		return "", "", fmt.Errorf("tools: publish shared CA: %w", err)
	}
	return sharedRoot, dest, nil
}

// buildProjectSpec resolves the container spec for a scan.
func buildProjectSpec(sc *scanctx.ScanContext, cfg *config.Config) (toolrt.ProjectSpec, error) {
	budget := resources.CurrentProjectBudget()

	// Egress selection for the container is the task's pinned proxy (or the
	// global pool) when the MITM recorder is off. With interception on, the
	// container talks to the run-scoped loopback listener, which forwards
	// through the same pinned egress.
	containerProxyURL := strings.TrimSpace(cfg.ToolboxProxyURL)
	hostNetwork := strings.EqualFold(strings.TrimSpace(cfg.ToolboxNetworkMode), "host")
	if !trafficproxy.Enabled() {
		if pin, has := proxy.ScanUpstreamFor(sc.ID); has && pin.Direct && cfg.ProxyRequired {
			return toolrt.ProjectSpec{}, fmt.Errorf("tools: this task pins direct egress but the process requires a proxy")
		}
		if containerProxyURL == "" && hostNetwork {
			if p, direct, ok := proxy.UpstreamFor(sc.ID); ok && !direct && p != nil {
				if u, err := p.URL(); err == nil {
					containerProxyURL = u.String()
				}
			}
		}
	}

	egress, err := resolveEgress(sc.ID, cfg.ProxyRequired, trafficproxy.Enabled(), containerProxyURL, cfg.TrafficProxyBind, trafficproxy.CAPath())
	if err != nil {
		return toolrt.ProjectSpec{}, err
	}
	netProfile := egress.NetProfile
	proxyURL := egress.ProxyURL
	caPath := egress.CACertPath
	if netProfile == toolrt.NetProfileDirect {
		log.Printf("[runtime] scan %s: project container uses direct egress (traffic proxy disabled)", sc.ID)
	}

	sharedRoot, mountedCA, prepErr := prepareSharedAssets(cfg.DataDir, caPath)
	if prepErr != nil {
		return toolrt.ProjectSpec{}, prepErr
	}
	workerSocket := HTTPWorkerSocketPath(sc.ScanDir)
	if mkErr := os.MkdirAll(filepath.Dir(workerSocket), 0o750); mkErr != nil {
		return toolrt.ProjectSpec{}, fmt.Errorf("tools: prepare http worker socket dir: %w", mkErr)
	}

	// A scan whose source tree is available needs no extra mounts; the same-path
	// scan directory mount already covers the clone and the artefacts.
	return toolrt.BuildProject(toolrt.ProjectOptions{
		InstanceID:       cfg.RuntimeInstanceID(),
		ScanID:           sc.ID,
		ScanDir:          sc.ScanDir,
		SharedDir:        sharedRoot,
		Image:            cfg.ToolboxImage,
		Profile:          cfg.ToolboxProfile,
		NetProfile:       netProfile,
		NetworkMode:      cfg.ToolboxNetworkMode,
		ProxyURL:         proxyURL,
		ProxyGatewayPort: egress.ProxyGatewayPort,
		CACertPath:       mountedCA,
		HTTPWorkerSocket: workerSocket,
		BrowserProxy:     cfg.ToolboxBrowserProxy,
		Limits: toolrt.ResourceLimits{
			MemoryMB:  budget.MemoryMB,
			CPUQuota:  budget.CPUQuota,
			CPUPeriod: budget.CPUPeriod,
			Pids:      budget.Pids,
			TmpfsMB:   budget.TmpfsMB,
			DiskMB:    budget.DiskMB,
		},
	})
}

// RunInProject executes argv inside the scan's project container.
//
// argv is passed through unchanged: the caller has already applied the security
// chain (scope guard, path policy, credential scrubbing, proxy injection).
func RunInProject(ctx context.Context, sc *scanctx.ScanContext, spec toolrt.ExecSpec) (*toolrt.ExecResult, error) {
	drv, err := EnsureScanProject(ctx, sc)
	if err != nil {
		return nil, err
	}
	if drv == nil {
		return nil, toolrt.ErrNoDriver
	}
	spec.ProjectID = sc.ID
	spec.ScanID = sc.ID
	if tb := toolrt.ActiveToolbox(); tb != nil {
		// BeginExec keeps the project marked in-flight for the whole call —
		// including the container-vanished retry below — so idle reaping cannot
		// stop the container while a long command is still running.
		release := tb.BeginExec(sc.ID)
		defer release()
	}
	res, execErr := drv.Exec(ctx, spec)
	if execErr != nil && toolrt.IsCode(execErr, toolrt.CodeNotFound) {
		// The container vanished (host restart, manual cleanup). Re-create once
		// and retry — this is the only retry in the path because EnsureProject is
		// idempotent while Exec is not.
		invalidateProject(sc.ID)
		drv, ensureErr := EnsureScanProject(ctx, sc)
		if ensureErr != nil {
			return nil, ensureErr
		}
		return drv.Exec(ctx, spec)
	}
	return res, execErr
}

// ToolshimPath is where the toolbox image publishes the filesystem/code-search
// shim.
const ToolshimPath = "/opt/xalgorix/bin/toolshim"

// RunToolshim executes one shim request inside the scan's project container.
//
// The request travels as a JSON file under the scan directory (mounted at the
// same absolute path on both sides) rather than on stdin: Docker exec cannot
// attach a stdin stream without hijacking the connection, and the request can be
// large (file contents for `create`).
func RunToolshim(ctx context.Context, sc *scanctx.ScanContext, root string, req any) (string, error) {
	if sc == nil {
		return "", fmt.Errorf("tools: scan context is required")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("tools: encode shim request: %w", err)
	}
	dir := filepath.Join(sc.ScanDir, ".toolshim")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("tools: prepare shim dir: %w", err)
	}
	name := fmt.Sprintf("req-%d.json", time.Now().UnixNano())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return "", fmt.Errorf("tools: write shim request: %w", err)
	}
	defer func() { _ = os.Remove(path) }()

	res, err := RunInProject(ctx, sc, toolrt.ExecSpec{
		CallSeq:     uint64(time.Now().UnixNano()),
		Tool:        "toolshim",
		Argv:        []string{ToolshimPath, "--request", path},
		Cwd:         root,
		Timeout:     2 * time.Minute,
		StdoutLimit: 8 << 20,
		StderrLimit: 1 << 20,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if unmarshalErr := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &out); unmarshalErr != nil {
		return "", fmt.Errorf("tools: decode shim response: %v (stderr: %s)", unmarshalErr, truncateShim(res.Stderr))
	}
	if out.Error != "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.Output, nil
}

func truncateShim(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

// IsInfrastructureError reports whether err must surface as a tool error (and
// count towards the circuit breaker) rather than as an ordinary failed command.
func IsInfrastructureError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, toolrt.ErrNoDriver) {
		return false
	}
	return toolrt.CircuitBreakerRelevant(err)
}

// ContainerEnvFor filters a host environment down to entries that are valid
// inside the project container. PATH/HOME/proxy come from the container's own
// environment; only explicitly scan-scoped values are forwarded.
func ContainerEnvFor(hostEnv []string, workDir string) []string {
	allowed := map[string]bool{
		"XALGORIX_RATE_LOCK_DIR": true,
		"XALGORIX_RATE_DELAY_MS": true,
		"PYTHONPATH":             true,
		"PYTHONUNBUFFERED":       true,
		"GOPATH":                 false, // host path, invalid in the container
		"TMPDIR":                 false,
		"HOME":                   false,
		"PATH":                   false,
	}
	out := make([]string, 0, 8)
	for _, kv := range hostEnv {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		keep, known := allowed[key]
		if !known || !keep {
			continue
		}
		// Only forward paths that remain inside the mounted scan directory.
		if key == "PYTHONPATH" && workDir != "" {
			parts := strings.Split(value, ":")
			kept := parts[:0]
			for _, p := range parts {
				if p == "" || strings.HasPrefix(p, workDir) {
					kept = append(kept, p)
				}
			}
			value = strings.Join(kept, ":")
			if value == "" {
				continue
			}
		}
		out = append(out, key+"="+value)
	}
	return out
}
