package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const testToolpack = `schema: 1
tools:
  - name: subfinder
    bin: /root/go/bin/subfinder
    category: recon
    purpose: passive subdomain enumeration
    capabilities: []
    profiles: [core]
  - name: nmap
    bin: /usr/bin/nmap
    category: network
    purpose: port scanning
    capabilities: [NET_RAW]
    profiles: [full]
  - name: chromium
    bin: /usr/bin/chromium
    category: browser
    purpose: headless browser
    capabilities: []
    profiles: [web]
`

func TestParseToolpackValidatesAndNormalizes(t *testing.T) {
	pack, err := ParseToolpack([]byte(testToolpack))
	if err != nil {
		t.Fatal(err)
	}
	if pack.Schema != 1 || len(pack.Tools) != 3 {
		t.Fatalf("unexpected pack: %+v", pack)
	}
	if pack.Tools[1].Capabilities[0] != "NET_RAW" {
		t.Fatalf("capabilities must keep their case, got %v", pack.Tools[1].Capabilities)
	}
}

func TestParseToolpackRejectsBadManifests(t *testing.T) {
	cases := map[string]string{
		"wrong schema": "schema: 2\ntools:\n  - name: x\n",
		"no tools":     "schema: 1\ntools: []\n",
		"bad yaml":     "tools: [",
		"empty name":   "schema: 1\ntools:\n  - bin: /bin/x\n",
	}
	for name, manifest := range cases {
		if _, err := ParseToolpack([]byte(manifest)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestToolsForProfileIncludesCoreInDerivedImages(t *testing.T) {
	pack, err := ParseToolpack([]byte(testToolpack))
	if err != nil {
		t.Fatal(err)
	}
	names := func(profile string) []string {
		var out []string
		for _, tool := range pack.ToolsForProfile(profile) {
			out = append(out, tool.Name)
		}
		return out
	}
	assertNames := func(profile string, want ...string) {
		t.Helper()
		got := names(profile)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("profile %s = %v, want %v", profile, got, want)
		}
	}
	assertNames("core", "subfinder")
	assertNames("full", "subfinder", "nmap")
	assertNames("web", "subfinder", "chromium")
	assertNames("", "subfinder", "nmap", "chromium")
	assertNames("all", "subfinder", "nmap", "chromium")
}

type manifestDriver struct {
	fakeDriver
	stdout   string
	exitCode int
	execErr  error
}

func (d *manifestDriver) Exec(_ context.Context, spec ExecSpec) (*ExecResult, error) {
	if d.execErr != nil {
		return nil, d.execErr
	}
	if len(spec.Argv) != 2 || spec.Argv[0] != "cat" || spec.Argv[1] != MountToolpack {
		return nil, errors.New("unexpected argv")
	}
	return &ExecResult{ExitCode: d.exitCode, Stdout: d.stdout}, nil
}

func TestLoadToolpackFromProject(t *testing.T) {
	pack, err := LoadToolpackFromProject(context.Background(), &manifestDriver{stdout: testToolpack}, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(pack.Tools))
	}
}

func TestLoadToolpackFromProjectUnavailable(t *testing.T) {
	if _, err := LoadToolpackFromProject(context.Background(), nil, "scan-1"); !errors.Is(err, ErrToolpackUnavailable) {
		t.Fatalf("nil driver err = %v, want ErrToolpackUnavailable", err)
	}
	if _, err := LoadToolpackFromProject(context.Background(), &manifestDriver{exitCode: 1}, "scan-1"); !errors.Is(err, ErrToolpackUnavailable) {
		t.Fatalf("non-zero exit err = %v, want ErrToolpackUnavailable", err)
	}
	if _, err := LoadToolpackFromProject(context.Background(), &manifestDriver{execErr: errors.New("stopped")}, "scan-1"); !errors.Is(err, ErrToolpackUnavailable) {
		t.Fatalf("exec err = %v, want ErrToolpackUnavailable", err)
	}
}
