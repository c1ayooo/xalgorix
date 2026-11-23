package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

// hostDataDirError validates the same-path data-directory contract for a
// containerised platform.
//
// The platform hands scan paths to the container runtime verbatim for bind
// mounts. When the platform itself runs in a container, the daemon resolves
// those paths on the HOST, so the host path must equal the platform's
// XALGORIX_DATA_DIR exactly. A named volume or a different host path would make
// every project container mount an empty or nonexistent directory. A host
// binary needs no such check: its DataDir already is the host path.
func hostDataDirError(dataDir, hostDataDir string, inContainer bool) error {
	if !inContainer {
		return nil
	}
	host := strings.TrimSpace(hostDataDir)
	if host == "" {
		return fmt.Errorf("XALGORIX_HOST_DATA_DIR is required when the platform runs in a container and XALGORIX_RUNTIME_BACKEND=docker: set it to the host directory bind-mounted at XALGORIX_DATA_DIR (%s)", dataDir)
	}
	if filepath.Clean(host) != filepath.Clean(strings.TrimSpace(dataDir)) {
		return fmt.Errorf("XALGORIX_HOST_DATA_DIR (%s) must equal XALGORIX_DATA_DIR (%s): project containers receive scan paths verbatim, so the platform does not translate host/container paths", host, dataDir)
	}
	return nil
}

// runningInContainer is a best-effort detector for the common container
// runtimes (Docker, Podman, Kubernetes). A host binary returns false and is
// exempt from the host-path contract.
func runningInContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	if strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST")) != "" {
		return true
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		if strings.Contains(s, "docker") || strings.Contains(s, "kubepods") || strings.Contains(s, "containerd") {
			return true
		}
	}
	return false
}

func preflightContainerRuntime(cfg *config.Config) error {
	if cfg == nil || cfg.RuntimeBackend != "docker" {
		return nil
	}
	return hostDataDirError(cfg.DataDir, cfg.HostDataDir, runningInContainer())
}
