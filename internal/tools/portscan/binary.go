package portscan

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gogoPathEnvVar lets an operator pin a specific gogo build without touching
// PATH. It is consulted first so a container image may ship gogo anywhere.
const gogoPathEnvVar = "XALGORIX_GOGO_PATH"

// errGogoUnavailable is the sentinel for "no gogo binary on this machine".
// Callers translate it into a soft failure (Result.Success=true, hint in
// Output) rather than a tool error, because a missing optional binary must not
// trip the registry circuit breaker.
var errGogoUnavailable = errors.New("gogo binary not found")

// gogoMissingHint is shown to the model verbatim. It must name a concrete
// fallback: the agent always has terminal_execute and nmap, so a missing gogo
// is a degraded path, not a dead end.
const gogoMissingHint = `gogo binary not found — port_scan needs the chainreactors/gogo CLI.
Resolve it with ONE of:
  1) install: go install github.com/chainreactors/gogo/v2@latest (lands in $GOBIN or ~/go/bin)
  2) pin it:  export XALGORIX_GOGO_PATH=/absolute/path/to/gogo
  3) package: copy a release binary to /usr/local/bin/gogo
Until then use terminal_execute with nmap for port/service discovery,
for example: nmap -sV -Pn -T4 <target>. Do not retry port_scan blindly.`

// locateGogo resolves the gogo binary in a fixed order:
//
//	XALGORIX_GOGO_PATH -> PATH lookup -> well-known install locations.
//
// It returns errGogoUnavailable when nothing is usable, and a descriptive
// error when XALGORIX_GOGO_PATH was set to something invalid (silently falling
// back to PATH there would hide an operator misconfiguration).
func locateGogo() (string, error) {
	if pinned := strings.TrimSpace(os.Getenv(gogoPathEnvVar)); pinned != "" {
		if isExecutableFile(pinned) {
			return pinned, nil
		}
		if resolved, err := exec.LookPath(pinned); err == nil {
			return resolved, nil
		}
		return "", fmt.Errorf("%s=%q does not point at an executable file", gogoPathEnvVar, pinned)
	}

	if resolved, err := exec.LookPath("gogo"); err == nil {
		return resolved, nil
	}

	for _, candidate := range wellKnownGogoPaths() {
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}

	return "", errGogoUnavailable
}

// wellKnownGogoPaths mirrors browser.wellKnownBrowserPaths(): LookPath misses
// real installs (notably $HOME/go/bin on hosts whose PATH does not include it).
func wellKnownGogoPaths() []string {
	paths := []string{
		"/usr/local/bin/gogo",
		"/usr/bin/gogo",
		"/opt/gogo/gogo",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths,
			filepath.Join(home, "go", "bin", "gogo"),
			filepath.Join(home, ".local", "bin", "gogo"),
		)
	}
	return paths
}

// isExecutableFile reports whether path is a regular file with at least one
// execute bit set.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
