package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Network profiles a project can request.
const (
	// NetProfileNone: no egress at all (analysis-only project).
	NetProfileNone = "none"
	// NetProfileProxyOnly: the container has no default route; the only way out
	// is the platform traffic proxy. This is the default and the fail-closed
	// posture.
	NetProfileProxyOnly = "proxy-only"
	// NetProfileRaw: the project needs raw sockets (SYN scans, ARP, MITM). It
	// gets NET_RAW/NET_ADMIN and a destination allow-list computed by the scope
	// guard; it never gets a general-purpose route.
	NetProfileRaw = "raw"
	// NetProfileDirect uses the default bridge with no proxy enforcement. It is
	// only correct when the operator has NOT enabled the traffic proxy and has
	// not required one; selecting it otherwise would silently bypass the proxy
	// observation and attribution channel.
	NetProfileDirect = "direct"
)

// Config is the process-wide runtime configuration.
type Config struct {
	Backend      Backend
	Transport    Transport
	SocketPath   string
	RunnerSocket string
	InstanceID   string
	Image        string
	Profile      string
	ProjectLimit int
	ExecLimit    int
	// DiskEnforce applies each project's DiskMB budget as the Docker
	// writable-layer StorageOpt. Off by default: unsupported daemons reject
	// container creation.
	DiskEnforce bool
}

// Open constructs the configured driver.
//
// BackendNative returns a NativeDriver and never fails. BackendDocker requires a
// reachable transport; the caller decides whether that is fatal (it is, once the
// operator has explicitly selected the container backend).
func Open(ctx context.Context, cfg Config) (Driver, error) {
	switch cfg.Backend {
	case "", BackendNative:
		return NewNative(), nil
	case BackendDocker:
		switch cfg.Transport {
		case TransportRunnerd:
			return NewRunnerdDriver(RunnerdConfig{SocketPath: cfg.RunnerSocket})
		case "", TransportDockerSocket:
			driver, err := NewDockerDriver(DockerConfig{
				SocketPath:   cfg.SocketPath,
				InstanceID:   cfg.InstanceID,
				Image:        cfg.Image,
				Profile:      cfg.Profile,
				ProjectLimit: cfg.ProjectLimit,
				ExecLimit:    cfg.ExecLimit,
				DiskEnforce:  cfg.DiskEnforce,
			})
			if err != nil {
				return nil, err
			}
			// Verify connectivity eagerly so a misconfigured socket fails at
			// startup instead of on the first tool call.
			if err := driver.client.ping(ctx); err != nil {
				return nil, fmt.Errorf("runtime: docker daemon unreachable: %w", err)
			}
			return driver, nil
		default:
			return nil, fmt.Errorf("runtime: unknown transport %q", cfg.Transport)
		}
	default:
		return nil, fmt.Errorf("runtime: unknown backend %q", cfg.Backend)
	}
}

var (
	activeMu sync.RWMutex
	activeDr Driver
)

// SetActive installs the process-wide driver. Passing nil restores native mode.
func SetActive(d Driver) {
	activeMu.Lock()
	defer activeMu.Unlock()
	activeDr = d
}

// Active returns the process-wide driver, or nil when none is installed.
func Active() Driver {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return activeDr
}

// ActiveContainer returns the driver only when it is a container backend.
// Tools use this as the single switch between "execute locally" and "execute in
// the project container".
func ActiveContainer() Driver {
	d := Active()
	if d == nil || d.Backend() == BackendNative {
		return nil
	}
	return d
}

var (
	stateMu       sync.RWMutex
	activeStore   DesiredStateStore
	activeToolbox *Toolbox
)

// SetStateStore installs the process-wide desired-state store.
func SetStateStore(s DesiredStateStore) {
	stateMu.Lock()
	defer stateMu.Unlock()
	activeStore = s
}

// StateStore returns the process-wide desired-state store, or nil.
func StateStore() DesiredStateStore {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return activeStore
}

// SetToolbox installs the process-wide toolbox handler (nil disables it).
func SetToolbox(t *Toolbox) {
	stateMu.Lock()
	defer stateMu.Unlock()
	activeToolbox = t
}

// ActiveToolbox returns the process-wide toolbox handler, or nil.
func ActiveToolbox() *Toolbox {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return activeToolbox
}

// ProjectOptions is the input for BuildProject.
type ProjectOptions struct {
	InstanceID string
	ScanID     string
	ScanDir    string
	SharedDir  string
	Image      string
	Profile    string
	NetProfile string
	ProxyURL   string
	// ProxyGatewayPort selects the per-project network gateway as the proxy
	// address (http://<gateway>:<port>). It is used when the platform's proxy
	// listens on 0.0.0.0 and an --internal project network can reach only its
	// own gateway, not the host loopback.
	ProxyGatewayPort int
	CACertPath       string
	EgressCIDRs      []string
	Limits           ResourceLimits
	Env              []string
	// BrowserProxy is only used by web-profile projects (Chromium argument).
	BrowserProxy string
	// HTTPWorkerSocket enables the in-container HTTP worker on this path.
	HTTPWorkerSocket string
}

// BuildProject assembles a ProjectSpec with the same-path mount plan and the
// default container environment.
func BuildProject(opt ProjectOptions) (ProjectSpec, error) {
	if strings.TrimSpace(opt.ScanID) == "" {
		return ProjectSpec{}, fmt.Errorf("runtime: scan id is required")
	}
	if strings.TrimSpace(opt.ScanDir) == "" {
		return ProjectSpec{}, fmt.Errorf("runtime: scan dir is required")
	}
	// Enforce the digest on the platform side too: under the runnerd transport
	// NewDockerDriver runs on the host, so a tag-only image would otherwise pass
	// the platform and be executed as a tag on the runner.
	if err := validateToolboxImage(opt.Image, allowUnpinnedToolbox()); err != nil {
		return ProjectSpec{}, err
	}
	plan, err := BuildMounts(opt.ScanDir, opt.SharedDir)
	if err != nil {
		return ProjectSpec{}, err
	}
	absScan, err := filepath.Abs(opt.ScanDir)
	if err != nil {
		return ProjectSpec{}, fmt.Errorf("runtime: resolve scan dir: %w", err)
	}
	netProfile := orDefault(opt.NetProfile, NetProfileProxyOnly)
	switch netProfile {
	case NetProfileNone, NetProfileProxyOnly, NetProfileRaw, NetProfileDirect:
	default:
		return ProjectSpec{}, fmt.Errorf("runtime: unknown net profile %q", netProfile)
	}
	if netProfile == NetProfileProxyOnly && strings.TrimSpace(opt.ProxyURL) == "" && opt.ProxyGatewayPort <= 0 {
		// Fail closed at configuration time rather than silently letting a
		// proxy-only project fall back to direct egress. A gateway port is a
		// valid proxy address too: the driver resolves the project network's
		// gateway after the network exists and injects the URL itself.
		return ProjectSpec{}, fmt.Errorf("runtime: proxy-only projects require a proxy URL")
	}

	env := []string{
		"HOME=" + filepath.Join(absScan, "home"),
		"TMPDIR=/tmp",
		"PATH=" + defaultContainerPath(),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"DEBIAN_FRONTEND=noninteractive",
		"XALGORIX_SCAN_ID=" + opt.ScanID,
		"XALGORIX_PROJECT_CONTAINER=1",
	}
	if strings.TrimSpace(opt.ProxyURL) != "" {
		env = append(env,
			"HTTP_PROXY="+opt.ProxyURL,
			"HTTPS_PROXY="+opt.ProxyURL,
			"ALL_PROXY="+opt.ProxyURL,
			"http_proxy="+opt.ProxyURL,
			"https_proxy="+opt.ProxyURL,
			"all_proxy="+opt.ProxyURL,
			"NO_PROXY=127.0.0.1,localhost,::1",
		)
	}
	if strings.TrimSpace(opt.CACertPath) != "" {
		env = append(env,
			"SSL_CERT_FILE="+opt.CACertPath,
			"CURL_CA_BUNDLE="+opt.CACertPath,
			"REQUESTS_CA_BUNDLE="+opt.CACertPath,
			"GIT_SSL_CAINFO="+opt.CACertPath,
		)
	}
	env = append(env, opt.Env...)

	spec := ProjectSpec{
		ProjectID:  opt.ScanID,
		ScanID:     opt.ScanID,
		InstanceID: opt.InstanceID,
		Image:      opt.Image,
		Profile:    orDefault(opt.Profile, ProfileCore),
		NetProfile: netProfile,
		Limits:     opt.Limits,
		Mounts:     plan.Mounts,
		WorkDir:    absScan,
		Env:        env,
		Network: NetworkSpec{
			Name:             "xalg-proj-" + sanitizeNetName(opt.ScanID),
			Internal:         netProfile == NetProfileProxyOnly,
			ProxyURL:         opt.ProxyURL,
			ProxyGatewayPort: opt.ProxyGatewayPort,
			CACertPath:       opt.CACertPath,
			EgressCIDRs:      opt.EgressCIDRs,
		},
		Capabilities:     capabilitiesForProfile(opt.Profile, netProfile),
		PublishCDP:       orDefault(opt.Profile, ProfileCore) == ProfileWeb,
		BrowserProxy:     opt.BrowserProxy,
		HTTPWorkerSocket: opt.HTTPWorkerSocket,
	}
	if err := ValidateMounts(spec.Mounts); err != nil {
		return ProjectSpec{}, err
	}
	return spec, nil
}

func capabilitiesForProfile(profile, netProfile string) []string {
	// DAC override/read-search are not optional: the project runs as root but
	// the container drops ALL capabilities, and the scan directory is owned by
	// the platform's uid with mode 0700. Without these two, root is subject to
	// file DAC and cannot write its own workspace.
	caps := []string{"CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH"}
	if profile == ProfileFull {
		caps = append(caps, "NET_RAW", "NET_ADMIN")
	}
	if netProfile == NetProfileRaw && profile != ProfileFull {
		caps = append(caps, "NET_RAW")
	}
	return caps
}

func sanitizeNetName(scanID string) string {
	var b strings.Builder
	for _, r := range scanID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := b.String()
	if len(name) > 40 {
		name = name[:40]
	}
	return name
}

func defaultContainerPath() string {
	// Matches tools/toolbox/toolpack.yaml: /opt/xalgorix/bin first so wrappers win
	// over distro binaries (httpx → ProjectDiscovery, not the Python client).
	return "/opt/xalgorix/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:" +
		"/root/go/bin:/root/.local/bin:/root/.cargo/bin:/usr/local/go/bin"
}

// InstanceID returns a stable identifier for this platform process. It scopes
// container adoption: an instance never touches another instance's containers.
func InstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "xalgorix"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}
