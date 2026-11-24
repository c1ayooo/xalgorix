package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Toolpack is the parsed inventory baked into the toolbox image at
// MountToolpack. The image is the authority on what it contains; the platform
// only reads this manifest to render a read-only view. It is never a substitute
// for the image's own selftest.
type Toolpack struct {
	Schema int            `json:"schema"`
	Tools  []ToolpackTool `json:"tools"`
}

// ToolpackTool mirrors one entry of tools/toolbox/toolpack.yaml.
type ToolpackTool struct {
	Name         string   `json:"name"`
	Bin          string   `json:"bin,omitempty"`
	Category     string   `json:"category,omitempty"`
	Purpose      string   `json:"purpose,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Profiles     []string `json:"profiles,omitempty"`
}

const (
	// toolpackSchema is the only manifest schema this reader understands.
	toolpackSchema = 1
	// maxToolpackBytes bounds the manifest read. The real file is a few tens of
	// KiB; the cap keeps a corrupted/hostile file from filling the response.
	maxToolpackBytes = 1 << 20
	// toolpackReadTimeout bounds the in-container read. It is a diagnostic read,
	// not a scan step: a slow container must not hold the request open.
	toolpackReadTimeout = 3 * time.Second
)

// ErrToolpackUnavailable wraps every failure to read the manifest from a live
// container, so callers can present "tools unavailable" without treating it as
// a request failure.
var ErrToolpackUnavailable = errors.New("runtime: toolbox manifest unavailable")

// ParseToolpack decodes and validates a toolpack manifest.
func ParseToolpack(data []byte) (Toolpack, error) {
	var pack Toolpack
	if err := yaml.Unmarshal(data, &pack); err != nil {
		return Toolpack{}, fmt.Errorf("runtime: parse toolpack: %w", err)
	}
	if pack.Schema != toolpackSchema {
		return Toolpack{}, fmt.Errorf("runtime: unsupported toolpack schema %d (want %d)", pack.Schema, toolpackSchema)
	}
	if len(pack.Tools) == 0 {
		return Toolpack{}, errors.New("runtime: toolpack declares no tools")
	}
	for i := range pack.Tools {
		t := &pack.Tools[i]
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" {
			return Toolpack{}, fmt.Errorf("runtime: toolpack entry %d has no name", i)
		}
		if t.Bin == "" {
			t.Bin = t.Name
		}
		t.Profiles = normalizeProfiles(t.Profiles)
		t.Capabilities = normalizeCapabilities(t.Capabilities)
	}
	return pack, nil
}

// ToolsForProfile returns the manifest entries the image profile is expected to
// contain. full and web are built FROM core, so core tools belong to them too;
// full-only tools are not in web and vice versa. An empty or unknown profile
// returns the whole manifest rather than silently showing nothing.
func (p Toolpack) ToolsForProfile(profile string) []ToolpackTool {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" || profile == "all" {
		return append([]ToolpackTool(nil), p.Tools...)
	}
	out := make([]ToolpackTool, 0, len(p.Tools))
	for _, t := range p.Tools {
		if toolInProfile(t.Profiles, profile) {
			out = append(out, t)
		}
	}
	return out
}

func toolInProfile(profiles []string, want string) bool {
	for _, p := range profiles {
		if p == want {
			return true
		}
		if p == "core" && (want == "full" || want == "web") {
			return true
		}
	}
	return false
}

func normalizeProfiles(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func normalizeCapabilities(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// LoadToolpackFromProject reads and parses the manifest from a live project
// container.
//
// The argv is fixed and the path is the image's own constant: no request input
// reaches the command. This is a read-only diagnostic, so it deliberately does
// not create or start a container — an absent/stopped container yields
// ErrToolpackUnavailable and the caller still renders the rest of the metadata.
func LoadToolpackFromProject(ctx context.Context, drv Driver, projectID string) (Toolpack, error) {
	if drv == nil {
		return Toolpack{}, fmt.Errorf("%w: no container driver", ErrToolpackUnavailable)
	}
	if strings.TrimSpace(projectID) == "" {
		return Toolpack{}, fmt.Errorf("%w: no project id", ErrToolpackUnavailable)
	}
	execCtx, cancel := context.WithTimeout(ctx, toolpackReadTimeout+2*time.Second)
	defer cancel()
	res, err := drv.Exec(execCtx, ExecSpec{
		ProjectID:   projectID,
		ScanID:      projectID,
		Tool:        "toolbox_manifest",
		Argv:        []string{"cat", MountToolpack},
		Cwd:         "/",
		Timeout:     toolpackReadTimeout,
		StdoutLimit: maxToolpackBytes,
	})
	if err != nil {
		return Toolpack{}, fmt.Errorf("%w: %v", ErrToolpackUnavailable, err)
	}
	if res.ExitCode != 0 {
		return Toolpack{}, fmt.Errorf("%w: cat exited %d: %s", ErrToolpackUnavailable, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	if res.StdoutTruncated {
		return Toolpack{}, fmt.Errorf("%w: manifest exceeds %d bytes", ErrToolpackUnavailable, maxToolpackBytes)
	}
	pack, err := ParseToolpack([]byte(res.Stdout))
	if err != nil {
		return Toolpack{}, fmt.Errorf("%w: %v", ErrToolpackUnavailable, err)
	}
	return pack, nil
}
