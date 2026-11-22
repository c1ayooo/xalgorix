package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The mount contract is deliberately boring: every path is mounted at the SAME
// absolute path inside the container. That single rule is what keeps the rest of
// the platform unchanged — sandbox.Policy compares absolute paths, the ledger
// persists <ScanDir>/ledger.json, tool output archives live at
// <ScanDir>/tool-outputs, traffic at <ScanDir>/traffic, and every one of those
// keeps working without a host↔container translation layer.
//
// Shared assets (wordlists, the traffic-proxy CA, tool install caches) follow
// the same rule for consistency; only their mode differs (ro vs rw).

// Standard shared mount points inside the toolbox image.
const (
	// MountWordlists is the read-only wordlist root baked into the image.
	MountWordlists = "/usr/share/wordlists"
	// MountCache is the writable shared install cache (go/pipx/cargo/npm).
	MountCache = "/opt/xalgorix/cache"
	// MountCACert is the traffic-proxy CA shared with the container.
	MountCACert = "/opt/xalgorix/ca/ca.pem"
	// MountToolpack is where the image manifests its own tool inventory.
	MountToolpack = "/opt/xalgorix/toolpack.yaml"
)

// MountPlan is the resolved set of bind mounts for one project.
type MountPlan struct {
	Mounts []Mount
	// Writable lists the container paths a tool may write to. Anything else is
	// on the read-only rootfs or a ro mount.
	Writable []string
}

// BuildMounts resolves the mount plan for a scan directory.
//
// scanDir is the only writable business path. sharedRoot is the platform data
// directory that carries long-lived shared assets; empty values simply omit the
// corresponding mount (useful for tests and for native-ish deployments).
func BuildMounts(scanDir, sharedRoot string) (MountPlan, error) {
	if strings.TrimSpace(scanDir) == "" {
		return MountPlan{}, fmt.Errorf("runtime: scan dir is required for mounts")
	}
	absScan, err := filepath.Abs(scanDir)
	if err != nil {
		return MountPlan{}, fmt.Errorf("runtime: resolve scan dir: %w", err)
	}
	plan := MountPlan{
		Mounts: []Mount{
			{Host: absScan, Container: absScan, Mode: MountReadWrite},
		},
		Writable: []string{absScan},
	}
	if strings.TrimSpace(sharedRoot) != "" {
		absShared, absErr := filepath.Abs(sharedRoot)
		if absErr != nil {
			return MountPlan{}, fmt.Errorf("runtime: resolve shared root: %w", absErr)
		}
		// Wordlists are optional: an empty prepared directory must not shadow
		// the image's baked /usr/share/wordlists. Mount only when it has content.
		wordlists := filepath.Join(absShared, "wordlists")
		if entries, readErr := os.ReadDir(wordlists); readErr == nil && len(entries) > 0 {
			plan.Mounts = append(plan.Mounts,
				Mount{Host: wordlists, Container: MountWordlists, Mode: MountReadOnly})
		}
		// The install cache is writable and shared; create it so the mount is
		// never a Docker-created empty directory with unexpected ownership.
		cache := filepath.Join(absShared, "cache")
		if mkErr := os.MkdirAll(cache, 0o750); mkErr == nil {
			plan.Mounts = append(plan.Mounts,
				Mount{Host: cache, Container: MountCache, Mode: MountReadWrite})
			plan.Writable = append(plan.Writable, MountCache)
		}
		// A missing CA file is a Docker "create a directory" trap on the bind
		// source; omit the mount and let the caller omit the matching env vars.
		caFile := filepath.Join(absShared, "ca", "ca.pem")
		if info, statErr := os.Stat(caFile); statErr == nil && !info.IsDir() {
			plan.Mounts = append(plan.Mounts,
				Mount{Host: caFile, Container: MountCACert, Mode: MountReadOnly})
		}
	}
	if err := ValidateMounts(plan.Mounts); err != nil {
		return MountPlan{}, err
	}
	return plan, nil
}

// ValidateMounts rejects mount plans that would either break the same-path
// invariant or hand a tool write access to something that must stay immutable.
func ValidateMounts(mounts []Mount) error {
	seen := make(map[string]struct{}, len(mounts))
	for _, m := range mounts {
		if !filepath.IsAbs(m.Host) || !filepath.IsAbs(m.Container) {
			return fmt.Errorf("runtime: mount paths must be absolute: %q -> %q", m.Host, m.Container)
		}
		switch m.Mode {
		case MountReadOnly, MountReadWrite, "":
		default:
			return fmt.Errorf("runtime: unsupported mount mode %q for %q", m.Mode, m.Container)
		}
		clean := filepath.Clean(m.Container)
		if _, dup := seen[clean]; dup {
			return fmt.Errorf("runtime: duplicate mount target %q", clean)
		}
		seen[clean] = struct{}{}
		if clean == "/" {
			return fmt.Errorf("runtime: refusing to mount over /")
		}
		// Same-path is the contract for business paths. Shared assets are the
		// exception (they are addressed by well-known image paths), so only
		// enforce identity for mounts that are not one of the published roots.
		if isSharedMountTarget(clean) {
			continue
		}
		if filepath.Clean(m.Host) != clean {
			return fmt.Errorf("runtime: business mounts must keep the same path (host %q, container %q)", m.Host, clean)
		}
	}
	return nil
}

func isSharedMountTarget(container string) bool {
	for _, p := range []string{MountWordlists, MountCache, MountCACert, MountToolpack} {
		if container == p || strings.HasPrefix(container, p+"/") {
			return true
		}
	}
	return false
}

// ContainerPath maps a host path into the container namespace and verifies the
// path is actually reachable (mounted) rather than silently resolving to the
// read-only image layer.
func (p MountPlan) ContainerPath(hostPath string) (string, error) {
	if strings.TrimSpace(hostPath) == "" {
		return "", fmt.Errorf("runtime: empty path")
	}
	abs, err := filepath.Abs(hostPath)
	if err != nil {
		return "", fmt.Errorf("runtime: resolve %q: %w", hostPath, err)
	}
	for _, m := range p.Mounts {
		if abs == filepath.Clean(m.Host) || strings.HasPrefix(abs, filepath.Clean(m.Host)+string(filepath.Separator)) {
			// Same-path mounts: the container path is the host path. Shared
			// roots are addressed by their published container path, which for
			// this platform is only used for reading wordlists.
			return abs, nil
		}
	}
	return "", fmt.Errorf("runtime: path %q is not covered by any project mount", abs)
}

// WritablePath reports whether a container path may be written by a tool.
func (p MountPlan) WritablePath(containerPath string) bool {
	clean := filepath.Clean(containerPath)
	for _, w := range p.Writable {
		if clean == filepath.Clean(w) || strings.HasPrefix(clean, filepath.Clean(w)+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
