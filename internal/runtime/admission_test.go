package runtime

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the admission layer: digest enforcement, project-slot
// accounting, and image-GC selection. None of them need a Docker daemon, a
// network, or PostgreSQL.

const pinnedHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func pinnedRef(repo string) string { return repo + "@sha256:" + pinnedHex }

func TestValidateToolboxImageDigest(t *testing.T) {
	cases := []struct {
		name          string
		image         string
		allowUnpinned bool
		wantErr       bool
	}{
		{name: "pinned", image: pinnedRef("ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core")},
		{name: "pinned-without-tag", image: pinnedRef("ghcr.io/c1ayooo/xalgorix-toolbox")},
		{name: "tag-only-rejected", image: "ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core", wantErr: true},
		// docker-compose.yml ships this tag-only default; the driver refuses it,
		// so the shipped compose value must be made digest-pinned (or the dev
		// profile must set XALGORIX_ALLOW_UNPINNED_TOOLBOX=1).
		{name: "shipped-compose-default-rejected", image: "ghcr.io/c1ayooo/xalgorix-toolbox:core", wantErr: true},
		{name: "tag-only-allowed-with-override", image: "ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core", allowUnpinned: true},
		{name: "short-digest-rejected", image: "ghcr.io/c1ayooo/xalgorix-toolbox@sha256:abc", wantErr: true},
		{name: "non-hex-digest-rejected", image: "ghcr.io/c1ayooo/xalgorix-toolbox@sha256:" + strings.Repeat("z", 64), wantErr: true},
		{name: "empty-rejected", image: "   ", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateToolboxImage(tc.image, tc.allowUnpinned)
			if tc.wantErr && err == nil {
				t.Fatalf("validateToolboxImage(%q, %v) = nil, want error", tc.image, tc.allowUnpinned)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateToolboxImage(%q, %v) = %v, want nil", tc.image, tc.allowUnpinned, err)
			}
		})
	}
}

func TestAllowUnpinnedToolboxOnlyRecognisesTruthyValues(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " on "} {
		t.Run("truthy-"+v, func(t *testing.T) {
			t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", v)
			if !allowUnpinnedToolbox() {
				t.Fatalf("%q must be treated as true", v)
			}
		})
	}
	for _, v := range []string{"", "0", "false", "no", "off", "2", "enabled"} {
		t.Run("falsy-"+v, func(t *testing.T) {
			t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", v)
			if allowUnpinnedToolbox() {
				t.Fatalf("%q must not be treated as true", v)
			}
		})
	}
}

func TestNewDockerDriverRejectsUnpinnedImage(t *testing.T) {
	t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", "")
	_, err := NewDockerDriver(DockerConfig{Image: "ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core"})
	if err == nil {
		t.Fatal("an unpinned toolbox image must be rejected")
	}
	if !strings.Contains(err.Error(), "XALGORIX_ALLOW_UNPINNED_TOOLBOX") {
		t.Fatalf("error must point at the override, got %v", err)
	}
}

func TestNewDockerDriverAcceptsPinnedImageAndOverride(t *testing.T) {
	socket := fakeDockerSocket(t)
	t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", "")
	if _, err := NewDockerDriver(DockerConfig{
		SocketPath: socket,
		Image:      pinnedRef("ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core"),
	}); err != nil {
		t.Fatalf("pinned image must be accepted: %v", err)
	}

	t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", "yes")
	if _, err := NewDockerDriver(DockerConfig{
		SocketPath: socket,
		Image:      "ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core",
	}); err != nil {
		t.Fatalf("explicit dev override must accept a tag: %v", err)
	}
}

func TestBuildProjectRejectsUnpinnedImage(t *testing.T) {
	t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", "")
	opt := ProjectOptions{
		ScanID:     "scan-a",
		ScanDir:    t.TempDir(),
		SharedDir:  t.TempDir(),
		Image:      "ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core",
		NetProfile: NetProfileNone,
	}
	if _, err := BuildProject(opt); err == nil {
		t.Fatal("BuildProject must reject an unpinned image on the platform side too")
	}

	opt.Image = pinnedRef("ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core")
	spec, err := BuildProject(opt)
	if err != nil {
		t.Fatalf("pinned image must be accepted: %v", err)
	}
	if spec.Image != opt.Image {
		t.Fatalf("spec image = %q, want %q", spec.Image, opt.Image)
	}
}

func TestEnsureProjectRejectsUnpinnedSpec(t *testing.T) {
	t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", "")
	// No client/socket is needed: the digest check runs before any daemon call.
	d := newSlotTestDriver(1)
	_, err := d.EnsureProject(context.Background(), ProjectSpec{
		ProjectID: "scan-a",
		Image:     "ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core",
	})
	if !IsCode(err, CodeImageMissing) {
		t.Fatalf("EnsureProject with a tag-only image must fail closed with %s, got %v", CodeImageMissing, err)
	}
}

func TestResolveProjectLimit(t *testing.T) {
	cases := []struct{ configured, derived, want int }{
		{0, 0, 1},
		{0, 5, 5},
		{-3, 4, 4},
		{-3, -4, 1},
		{7, 2, 7},
		{2, 99, 2},
	}
	for _, tc := range cases {
		if got := resolveProjectLimit(tc.configured, tc.derived); got != tc.want {
			t.Errorf("resolveProjectLimit(%d,%d) = %d, want %d", tc.configured, tc.derived, got, tc.want)
		}
	}
}

func TestNewDockerDriverFallsBackToResourceBudget(t *testing.T) {
	t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", "")
	t.Setenv("XALGORIX_MAX_PROJECTS", "5")
	t.Setenv("XALGORIX_PROJECT_MEM_MB", "1024")
	d, err := NewDockerDriver(DockerConfig{
		SocketPath:   fakeDockerSocket(t),
		Image:        pinnedRef("ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core"),
		ProjectLimit: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.projectLimit != 5 {
		t.Fatalf("projectLimit = %d, want 5 from XALGORIX_MAX_PROJECTS", d.projectLimit)
	}
	if d.slots == nil || cap(d.slots) != 5 {
		t.Fatalf("slots = %v (cap %d), want cap 5", d.slots, cap(d.slots))
	}

	negative, err := NewDockerDriver(DockerConfig{
		SocketPath:   fakeDockerSocket(t),
		Image:        pinnedRef("ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core"),
		ProjectLimit: -9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if negative.projectLimit < 1 || negative.slots == nil || cap(negative.slots) != negative.projectLimit {
		t.Fatalf("negative limit must clamp to >=1; limit=%d slots=%v", negative.projectLimit, negative.slots)
	}
}

func newSlotTestDriver(limit int) *DockerDriver {
	return &DockerDriver{
		image:        pinnedRef("ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core"),
		projectLimit: limit,
		projects:     make(map[string]*dockerProject),
		slots:        make(chan struct{}, limit),
	}
}

func TestAlignSlotsAccountsForAdoptedContainers(t *testing.T) {
	d := newSlotTestDriver(2)
	d.alignSlots([]ProjectState{
		{ProjectID: "a", ContainerID: "c1", State: StateReady},
		{ProjectID: "b", ContainerID: "c2", State: StateReady},
	})
	if len(d.slots) != 2 {
		t.Fatalf("aligned slots = %d, want 2", len(d.slots))
	}
	if err := d.acquireSlot("c"); !IsCode(err, CodeProjectLimit) {
		t.Fatalf("new project past the cap must fail with %s, got %v", CodeProjectLimit, err)
	}

	// Removing an adopted project must free exactly its own token.
	d.forget("a")
	if len(d.slots) != 1 {
		t.Fatalf("slots after forget = %d, want 1", len(d.slots))
	}
	if err := d.acquireSlot("c"); err != nil {
		t.Fatalf("slot must be reusable after removal: %v", err)
	}
}

func TestForgetDoesNotStealAnotherProjectsSlot(t *testing.T) {
	d := newSlotTestDriver(1)
	if err := d.acquireSlot("a"); err != nil {
		t.Fatal(err)
	}
	d.forget("ghost")
	if len(d.slots) != 1 {
		t.Fatalf("forgetting an unknown project released a slot: %d", len(d.slots))
	}
	d.forget("a")
	if len(d.slots) != 0 {
		t.Fatalf("slots after removing the owner = %d, want 0", len(d.slots))
	}
}

func TestOverCapAdoptionDoesNotReleaseBorrowedTokens(t *testing.T) {
	d := newSlotTestDriver(2)
	d.alignSlots([]ProjectState{
		{ProjectID: "a", ContainerID: "c1"},
		{ProjectID: "b", ContainerID: "c2"},
		{ProjectID: "c", ContainerID: "c3"},
	})
	if len(d.slots) != 2 {
		t.Fatalf("slots = %d, want the cap 2", len(d.slots))
	}
	d.mu.Lock()
	heldC := d.projects["c"].slotHeld
	d.mu.Unlock()
	if heldC {
		t.Fatal("the over-cap project must not claim a token")
	}
	// Removing the over-cap project must not release a token it never held.
	d.forget("c")
	if len(d.slots) != 2 {
		t.Fatalf("slots after removing an over-cap project = %d, want 2", len(d.slots))
	}
}

func TestAdoptSlotIsIdempotentAndNonFailingAtCap(t *testing.T) {
	d := newSlotTestDriver(1)
	d.adoptSlot("a")
	d.adoptSlot("a")
	d.adoptSlot("b") // already at cap: adopting an existing container must not fail
	if len(d.slots) != 1 {
		t.Fatalf("slots = %d, want 1", len(d.slots))
	}
	d.mu.Lock()
	aHeld := d.projects["a"].slotHeld
	bHeld := d.projects["b"].slotHeld
	d.mu.Unlock()
	if !aHeld || bHeld {
		t.Fatalf("slot ownership: a=%v b=%v, want a=true b=false", aHeld, bHeld)
	}
}

func TestIsToolboxRepoTagsIsExact(t *testing.T) {
	cases := map[string]bool{
		"ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core":  true,
		"ghcr.io/c1ayooo/xalgorix-toolbox:latest":      true,
		"xalgorix-toolbox:1.0.0-full":                  true,
		"xalgorix-toolbox":                             true,
		"registry:5000/xalgorix-toolbox:web":           true,
		"myorg/xalgorix-toolbox-mirror:latest":         false,
		"xalgorix-toolbox-mirror:latest":               false,
		"nginx:latest":                                 false,
		"ghcr.io/c1ayooo/xalgorix-platform:1.0.0-slim": false,
	}
	for tag, want := range cases {
		if got := isToolboxRepoTags(tag); got != want {
			t.Errorf("isToolboxRepoTags(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestPlanImageGarbageKeepsNewestAndSkipsProtected(t *testing.T) {
	images := []dockerImageSummary{
		{ID: "sha256:aaa1", RepoTags: []string{"ghcr.io/c1ayooo/xalgorix-toolbox:3-core"}, Created: 300},
		{ID: "sha256:aaa2", RepoTags: []string{"ghcr.io/c1ayooo/xalgorix-toolbox:2-core"}, Created: 200},
		{ID: "sha256:aaa3", RepoTags: []string{"ghcr.io/c1ayooo/xalgorix-toolbox:1-core"}, Created: 100},
		// A sibling repository with a similar name must never be collected.
		{ID: "sha256:bbb1", RepoTags: []string{"myorg/xalgorix-toolbox-mirror:latest"}, Created: 50},
		// Non-toolbox images are never touched.
		{ID: "sha256:ccc1", RepoTags: []string{"nginx:latest"}, Created: 400},
		// Dangling images have no repo tags and are not ours to delete.
		{ID: "sha256:ddd1", RepoTags: nil, Created: 10},
	}

	got := planImageGarbage(images, 1, nil)
	if ids := imageIDs(got); !equalStrings(ids, []string{"sha256:aaa2", "sha256:aaa3"}) {
		t.Fatalf("garbage = %v, want [sha256:aaa2 sha256:aaa3]", ids)
	}

	// Protecting the configured image (by digest ref) keeps it even outside the
	// keep window.
	protected := map[string]struct{}{}
	protectImageRef(protected, "ghcr.io/c1ayooo/xalgorix-toolbox:1-core@sha256:aaa3")
	got = planImageGarbage(images, 1, protected)
	if ids := imageIDs(got); !equalStrings(ids, []string{"sha256:aaa2"}) {
		t.Fatalf("garbage with protected = %v, want [sha256:aaa2]", ids)
	}

	// keep <= 0 falls back to the default window (3): nothing to collect.
	if got := planImageGarbage(images, 0, nil); len(got) != 0 {
		t.Fatalf("default keep must retain the three newest toolbox images, got %v", imageIDs(got))
	}
}

func fakeDockerSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return path
}

func imageIDs(images []dockerImageSummary) []string {
	out := make([]string, 0, len(images))
	for _, img := range images {
		out = append(out, img.ID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestBuildProjectAcceptsGatewayProxy pins the container-reachable proxy path:
// a proxy-only project whose proxy is addressed through the project network's
// gateway (the platform traffic proxy bound on 0.0.0.0) is valid — the driver
// resolves the gateway and injects the URL itself after the network exists.
func TestBuildProjectAcceptsGatewayProxy(t *testing.T) {
	t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", "yes")
	opt := ProjectOptions{
		ScanID:           "scan-gwproxy",
		ScanDir:          t.TempDir(),
		SharedDir:        t.TempDir(),
		Image:            "xalgorix-toolbox:dev-core",
		NetProfile:       NetProfileProxyOnly,
		ProxyGatewayPort: 8081,
	}
	spec, err := BuildProject(opt)
	if err != nil {
		t.Fatalf("gateway-addressed proxy must be accepted: %v", err)
	}
	if spec.Network.ProxyGatewayPort != 8081 {
		t.Fatalf("gateway port lost: %#v", spec.Network)
	}
	if !spec.Network.Internal {
		t.Fatal("proxy-only projects must use an internal network")
	}

	// Without either a URL or a gateway port the fail-closed check stays.
	opt.ProxyGatewayPort = 0
	if _, err := BuildProject(opt); err == nil {
		t.Fatal("proxy-only without a reachable proxy must be rejected")
	}
}

// TestBuildProjectMountsHTTPWorkerSocketDir keeps the short socket path
// dialable from both sides: the worker binds it inside the container, the
// platform dials the same absolute path on the host.
func TestBuildProjectMountsHTTPWorkerSocketDir(t *testing.T) {
	t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", "yes")
	socket := filepath.Join(t.TempDir(), "xalg-http", "abc123.sock")
	spec, err := BuildProject(ProjectOptions{
		ScanID:           "scan-hw",
		ScanDir:          t.TempDir(),
		SharedDir:        t.TempDir(),
		Image:            "xalgorix-toolbox:dev-core",
		NetProfile:       NetProfileNone,
		HTTPWorkerSocket: socket,
	})
	if err != nil {
		t.Fatalf("BuildProject: %v", err)
	}
	parent := filepath.Dir(socket)
	found := false
	for _, m := range spec.Mounts {
		if m.Host == parent && m.Container == parent {
			found = true
		}
	}
	if !found {
		t.Fatalf("socket dir %q not mounted: %#v", parent, spec.Mounts)
	}
	if info, statErr := os.Stat(parent); statErr != nil || !info.IsDir() {
		t.Fatalf("socket dir must exist before container creation: %v", statErr)
	}
}

// TestBuildProjectPointsCACertAtContainerMount pins the TLS fix: the host CA
// path is not visible inside the container, so the trust env vars must name
// the mounted location (MountCACert) instead.
func TestBuildProjectPointsCACertAtContainerMount(t *testing.T) {
	t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", "yes")
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte("test-ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := BuildProject(ProjectOptions{
		ScanID:     "scan-ca",
		ScanDir:    t.TempDir(),
		SharedDir:  t.TempDir(),
		Image:      "xalgorix-toolbox:dev-core",
		NetProfile: NetProfileNone,
		CACertPath: ca,
	})
	if err != nil {
		t.Fatalf("BuildProject: %v", err)
	}
	want := "SSL_CERT_FILE=" + MountCACert
	found := false
	for _, e := range spec.Env {
		if e == want {
			found = true
		}
		if strings.Contains(e, ca) {
			t.Fatalf("env leaks the host CA path into the container: %q", e)
		}
	}
	if !found {
		t.Fatalf("container env must trust %s: %#v", MountCACert, spec.Env)
	}
}
