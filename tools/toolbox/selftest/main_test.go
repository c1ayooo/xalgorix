package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The manifest parser is a hand-written YAML subset. This test pins the subset
// and, crucially, asserts that every entry in the shipped manifest is
// well-formed — a typo there would silently disable a tool's health check.
func TestParseShippedToolpack(t *testing.T) {
	path := filepath.Join("..", "toolpack.yaml")
	tools, err := parseToolpack(path)
	if err != nil {
		t.Fatalf("parse shipped toolpack: %v", err)
	}
	if len(tools) < 20 {
		t.Fatalf("parsed only %d tools; the manifest should list the full toolbox", len(tools))
	}
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if tool.Name == "" {
			t.Fatalf("tool without a name: %+v", tool)
		}
		if seen[tool.Name] {
			t.Errorf("duplicate tool %q in manifest", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Bin == "" {
			t.Errorf("tool %q has no bin path", tool.Name)
		}
		if len(tool.Profiles) == 0 {
			t.Errorf("tool %q declares no profile", tool.Name)
		}
		if len(tool.Smoke) == 0 {
			t.Errorf("tool %q declares no smoke command", tool.Name)
		}
		for _, profile := range tool.Profiles {
			switch profile {
			case "core", "full", "web":
			default:
				t.Errorf("tool %q has unknown profile %q", tool.Name, profile)
			}
		}
		for _, capability := range tool.Capabilities {
			switch capability {
			case "NET_RAW", "NET_ADMIN", "SYS_PTRACE":
			default:
				t.Errorf("tool %q declares unknown capability %q", tool.Name, capability)
			}
		}
	}
	for _, required := range []string{"nmap", "nuclei", "httpx", "toolshim", "chromium"} {
		if !seen[required] {
			t.Errorf("manifest is missing %q", required)
		}
	}
}

// loadToolpack is the path main() takes: parse + vocabulary check. The shipped
// manifest must pass both, which keeps the capability/profile sets in this
// helper in sync with what the platform actually grants.
func TestLoadShippedToolpack(t *testing.T) {
	tools, err := loadToolpack(filepath.Join("..", "toolpack.yaml"))
	if err != nil {
		t.Fatalf("load shipped toolpack: %v", err)
	}
	if len(tools) < 20 {
		t.Fatalf("loaded only %d tools", len(tools))
	}
}

// The shipped manifest is the contract: exactly the 50 declared tools must keep
// parsing and passing the vocabulary check. A parser regression that drops or
// mangles entries shows up here as a count/inventory mismatch. Adding a tool is
// a deliberate manifest change and must update this count.
func TestShippedManifestPinsFiftyTools(t *testing.T) {
	path := filepath.Join("..", "toolpack.yaml")
	tools, err := loadToolpack(path)
	if err != nil {
		t.Fatalf("shipped manifest must pass loadToolpack (parse + vocabulary): %v", err)
	}
	if len(tools) != 50 {
		t.Fatalf("shipped manifest declares %d tools, want 50", len(tools))
	}
	if err := validateToolpack(tools); err != nil {
		t.Fatalf("shipped manifest fails vocabulary check: %v", err)
	}
}

// A requested profile is a filter, not free text. Before this check a typo such
// as `--profile cor` skipped every tool and exited 0: the image gate passed
// without running a single smoke command.
func TestValidateProfileFlag(t *testing.T) {
	for _, ok := range []string{"core", "full", "web", "all"} {
		if err := validateProfileFlag(ok); err != nil {
			t.Errorf("profile %q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "cor", "debug", "CORE"} {
		err := validateProfileFlag(bad)
		if err == nil {
			t.Errorf("profile %q must be rejected", bad)
			continue
		}
		if !strings.Contains(err.Error(), bad) && bad != "" {
			t.Errorf("error should name the offending profile %q: %v", bad, err)
		}
	}
}

func TestValidateToolpackAcceptsKnownVocabulary(t *testing.T) {
	tools := []tool{{
		Name:         "nmap",
		Capabilities: []string{"NET_RAW", "NET_ADMIN", "SYS_PTRACE"},
		Profiles:     []string{"core", "full", "web"},
	}}
	if err := validateToolpack(tools); err != nil {
		t.Fatalf("known vocabulary rejected: %v", err)
	}
}

func TestValidateToolpackRejectsUnknownCapability(t *testing.T) {
	tools := []tool{{
		Name:         "sneaky",
		Capabilities: []string{"NET_RAW", "SYS_ADMIN"},
		Profiles:     []string{"core"},
	}}
	err := validateToolpack(tools)
	if err == nil {
		t.Fatal("unknown capability must be rejected")
	}
	msg := err.Error()
	if !strings.Contains(msg, "sneaky") {
		t.Errorf("error does not name the tool: %v", err)
	}
	if !strings.Contains(msg, "SYS_ADMIN") {
		t.Errorf("error does not name the offending capability: %v", err)
	}
}

func TestValidateToolpackRejectsUnknownProfile(t *testing.T) {
	tools := []tool{{
		Name:     "browserish",
		Profiles: []string{"core", "debug"},
	}}
	err := validateToolpack(tools)
	if err == nil {
		t.Fatal("unknown profile must be rejected")
	}
	msg := err.Error()
	if !strings.Contains(msg, "browserish") {
		t.Errorf("error does not name the tool: %v", err)
	}
	if !strings.Contains(msg, "debug") {
		t.Errorf("error does not name the offending profile: %v", err)
	}
}

// A manifest that parses cleanly but declares an out-of-vocabulary capability
// must fail the image gate instead of silently dropping the grant.
func TestLoadToolpackRejectsUnknownCapabilityEndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	manifest := "tools:\n  - name: rogue\n    bin: /bin/true\n    capabilities: [NET_ADMIN, DAC_OVERRIDE]\n    profiles: [core]\n    smoke: [\"/bin/true\"]\n"
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadToolpack(path)
	if err == nil {
		t.Fatal("manifest with unknown capability must be rejected")
	}
	if !strings.Contains(err.Error(), "rogue") || !strings.Contains(err.Error(), "DAC_OVERRIDE") {
		t.Errorf("error should name the tool and the capability: %v", err)
	}
}

func TestProfileMatching(t *testing.T) {
	cases := []struct {
		profiles []string
		want     string
		match    bool
	}{
		{[]string{"core"}, "core", true},
		{[]string{"full"}, "core", false},
		{[]string{"core", "full"}, "full", true},
		{[]string{"web"}, "all", true},
		{nil, "core", true},
	}
	for _, tc := range cases {
		if got := profileMatches(tc.profiles, tc.want); got != tc.match {
			t.Errorf("profileMatches(%v, %q) = %v, want %v", tc.profiles, tc.want, got, tc.match)
		}
	}
}

func TestParseArrayAndUnquote(t *testing.T) {
	if got := parseArray("[NET_RAW, NET_ADMIN]"); len(got) != 2 || got[0] != "NET_RAW" || got[1] != "NET_ADMIN" {
		t.Fatalf("parseArray = %v", got)
	}
	if got := parseArray("[]"); len(got) != 0 {
		t.Fatalf("empty array = %v", got)
	}
	if got := unquote(`"nmap"`); got != "nmap" {
		t.Fatalf("unquote = %q", got)
	}
}

func TestParseToolpackRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(path, []byte("schema: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseToolpack(path); err == nil {
		t.Fatal("a manifest with no tools must be rejected")
	}
}
