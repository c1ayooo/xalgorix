package tools

import (
	"os"
	"path/filepath"
	"testing"

	toolrt "github.com/c1ayooo/xalgorix/v4/internal/runtime"
)

func TestPrepareSharedAssetsPublishesCAAtMountedPath(t *testing.T) {
	dataDir := t.TempDir()
	caSource := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caSource, []byte("first-ca"), 0o600); err != nil {
		t.Fatal(err)
	}

	sharedRoot, mountedCA, err := prepareSharedAssets(dataDir, caSource)
	if err != nil {
		t.Fatal(err)
	}
	wantCA := filepath.Join(dataDir, "shared", "ca", "ca.pem")
	if mountedCA != wantCA {
		t.Fatalf("mounted CA = %q, want %q", mountedCA, wantCA)
	}
	got, err := os.ReadFile(mountedCA)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first-ca" {
		t.Fatalf("published CA = %q, want first-ca", got)
	}
	if _, err := os.Stat(filepath.Join(sharedRoot, "cache")); err != nil {
		t.Fatalf("shared cache dir must exist: %v", err)
	}

	// A rotated CA replaces the published copy.
	if err := os.WriteFile(caSource, []byte("second-ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, mountedCA, err = prepareSharedAssets(dataDir, caSource)
	if err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(mountedCA)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second-ca" {
		t.Fatalf("rotated CA = %q, want second-ca", got)
	}
}

func TestPrepareSharedAssetsWithoutCAOmitsTheMount(t *testing.T) {
	dataDir := t.TempDir()
	sharedRoot, mountedCA, err := prepareSharedAssets(dataDir, filepath.Join(t.TempDir(), "missing.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if mountedCA != "" {
		t.Fatalf("mounted CA = %q, want empty when the source is missing", mountedCA)
	}
	scanDir := t.TempDir()
	plan, err := toolrt.BuildMounts(scanDir, sharedRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range plan.Mounts {
		if m.Container == toolrt.MountCACert {
			t.Fatal("missing CA must not be mounted")
		}
	}
}
