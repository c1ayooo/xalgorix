// Command toolbox-selftest verifies that a toolbox image really contains the
// tools its manifest claims.
//
// It is the image's own acceptance gate: CI runs it as the final Docker target,
// and the platform runs it inside a throwaway project container when it first
// uses a new image digest. A missing or non-executable tool is a build failure,
// not a runtime surprise for the agent.
//
//	go build ./tools/toolbox/selftest
//	toolbox-selftest --profile core --json
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type tool struct {
	Name         string
	Bin          string
	Category     string
	Purpose      string
	Capabilities []string
	Profiles     []string
	Smoke        []string
}

type report struct {
	Profile string   `json:"profile"`
	Passed  []string `json:"passed"`
	Failed  []string `json:"failed"`
	Skipped []string `json:"skipped"`
}

func main() {
	var (
		packPath = flag.String("toolpack", "/opt/xalgorix/toolpack.yaml", "path to toolpack.yaml")
		profile  = flag.String("profile", "core", "profile to verify: core|full|web|all")
		asJSON   = flag.Bool("json", false, "emit machine-readable JSON")
		timeout  = flag.Duration("timeout", 20*time.Second, "per-tool smoke timeout")
	)
	flag.Parse()

	// Reject an unknown --profile before touching the manifest: silently
	// skipping every tool would let the image gate pass without running a single
	// smoke command.
	if err := validateProfileFlag(*profile); err != nil {
		fmt.Fprintf(os.Stderr, "toolbox-selftest: %v\n", err)
		os.Exit(2)
	}

	tools, err := loadToolpack(*packPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "toolbox-selftest: %v\n", err)
		os.Exit(2)
	}

	rep := report{Profile: *profile}
	for _, t := range tools {
		if !profileMatches(t.Profiles, *profile) {
			rep.Skipped = append(rep.Skipped, t.Name)
			continue
		}
		if problem := verify(t, *timeout); problem != "" {
			rep.Failed = append(rep.Failed, t.Name+": "+problem)
			continue
		}
		rep.Passed = append(rep.Passed, t.Name)
	}

	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(rep)
	} else {
		fmt.Printf("profile=%s passed=%d failed=%d skipped=%d\n", rep.Profile, len(rep.Passed), len(rep.Failed), len(rep.Skipped))
		for _, f := range rep.Failed {
			fmt.Printf("  FAIL %s\n", f)
		}
	}
	if len(rep.Failed) > 0 {
		os.Exit(1)
	}
}

func profileMatches(profiles []string, want string) bool {
	if want == "all" || len(profiles) == 0 {
		return true
	}
	for _, p := range profiles {
		if strings.EqualFold(strings.TrimSpace(p), want) {
			return true
		}
	}
	return false
}

func verify(t tool, timeout time.Duration) string {
	bin := t.Bin
	if bin == "" {
		bin = t.Name
	}
	if _, err := os.Stat(bin); err != nil {
		return "binary missing: " + err.Error()
	}
	argv := t.Smoke
	if len(argv) == 0 {
		argv = []string{bin, "--version"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // manifest-controlled argv
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Sprintf("smoke timed out after %s", timeout)
	}
	if err != nil {
		return fmt.Sprintf("smoke failed: %v (%s)", err, firstLine(string(out)))
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return strings.TrimSpace(s)
}

// allowedCapabilities is the closed vocabulary the platform knows how to grant
// (`capabilitiesForProfile` in internal/runtime). A manifest may not invent a
// capability: an unknown one would either be silently dropped or, worse, read as
// a grant request the platform cannot honor.
var allowedCapabilities = map[string]struct{}{
	"NET_RAW":    {},
	"NET_ADMIN":  {},
	"SYS_PTRACE": {},
}

// allowedProfiles is the closed set of image targets. An entry claiming a
// profile that is never built would make its health check vacuous.
var allowedProfiles = map[string]struct{}{
	"core": {},
	"full": {},
	"web":  {},
}

// validateProfileFlag checks the --profile filter against the same closed
// vocabulary as the manifest, plus "all". Without it a misspelled filter selects
// no tools and the gate exits 0 having verified nothing.
func validateProfileFlag(profile string) error {
	if profile == "all" {
		return nil
	}
	if _, ok := allowedProfiles[profile]; ok {
		return nil
	}
	return fmt.Errorf("unknown profile %q (allowed: core, full, web, all)", profile)
}

// loadToolpack parses a manifest and then checks its vocabulary. The two steps
// are used together by main, so folding them into one function means the image
// gate cannot forget the check.
func loadToolpack(path string) ([]tool, error) {
	tools, err := parseToolpack(path)
	if err != nil {
		return nil, err
	}
	if err := validateToolpack(tools); err != nil {
		return nil, err
	}
	return tools, nil
}

// validateToolpack enforces the manifest's declarative vocabulary. Errors name
// the offending tool so a build failure points straight at the line to fix.
func validateToolpack(tools []tool) error {
	for _, t := range tools {
		for _, capability := range t.Capabilities {
			if _, ok := allowedCapabilities[capability]; !ok {
				return fmt.Errorf("tool %q declares unknown capability %q (allowed: NET_RAW, NET_ADMIN, SYS_PTRACE)", t.Name, capability)
			}
		}
		for _, profile := range t.Profiles {
			if _, ok := allowedProfiles[profile]; !ok {
				return fmt.Errorf("tool %q declares unknown profile %q (allowed: core, full, web)", t.Name, profile)
			}
		}
	}
	return nil
}

// parseToolpack reads the small YAML subset used by toolpack.yaml: a flat list of
// mappings under `tools:`, with scalar values and inline `[a, b]` arrays. A full
// YAML parser would add a dependency to an image-only helper; this subset is
// pinned by a unit test in the repository.
func parseToolpack(path string) ([]tool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var tools []tool
	inTools := false
	var current *tool

	flush := func() {
		if current != nil {
			tools = append(tools, *current)
			current = nil
		}
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			inTools = trimmed == "tools:"
			continue
		}
		if !inTools {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			flush()
			current = &tool{}
			applyField(current, strings.TrimPrefix(trimmed, "- "))
			continue
		}
		if current != nil {
			applyField(current, trimmed)
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("%s declares no tools", path)
	}
	return tools, nil
}

func applyField(t *tool, line string) {
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	switch key {
	case "name":
		t.Name = unquote(value)
	case "bin":
		t.Bin = unquote(value)
	case "category":
		t.Category = unquote(value)
	case "purpose":
		t.Purpose = unquote(value)
	case "capabilities":
		t.Capabilities = parseArray(value)
	case "profiles":
		t.Profiles = parseArray(value)
	case "smoke":
		t.Smoke = parseArray(value)
	}
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

func parseArray(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = unquote(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
