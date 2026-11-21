//go:build linux

package egress

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNFTScriptAllowsOnlyProxyPortForUID(t *testing.T) {
	script := nftScript("xalgorix_egress_test", 1234, 4321)
	for _, want := range []string{
		"add table inet xalgorix_egress_test",
		"meta skuid 1234 ip daddr 127.0.0.1 tcp dport 4321 accept",
		"meta skuid 1234 ip6 daddr ::1 tcp dport 4321 accept",
		"meta skuid 1234 drop",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("nft script missing %q:\n%s", want, script)
		}
	}
}

func TestApplyChildCredentialUsesEgressUID(t *testing.T) {
	t.Setenv("XALGORIX_EGRESS_UID", "1234")
	t.Setenv("XALGORIX_EGRESS_GID", "2345")
	mu.Lock()
	byScan["cred-scan"] = Status{Mode: "enforce"}
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		delete(byScan, "cred-scan")
		mu.Unlock()
	})

	cmd := exec.Command("true")
	if err := ApplyChildCredential(cmd, "cred-scan"); err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		t.Fatal("credential not applied")
	}
	if cmd.SysProcAttr.Credential.Uid != 1234 || cmd.SysProcAttr.Credential.Gid != 2345 {
		t.Fatalf("credential = %+v", cmd.SysProcAttr.Credential)
	}
}

func TestPrepareChildAccessMakesWorkspaceReadableAndWritable(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	t.Setenv("XALGORIX_EGRESS_UID", strconv.Itoa(uid))
	t.Setenv("XALGORIX_EGRESS_GID", strconv.Itoa(gid))
	mu.Lock()
	byScan["access-scan"] = Status{Mode: "enforce", Enforced: true}
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		delete(byScan, "access-scan")
		mu.Unlock()
	})

	root := t.TempDir()
	sub := filepath.Join(root, "nested")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "evidence.txt")
	if err := os.WriteFile(file, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareChildAccess("access-scan", root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, sub} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o750 {
			t.Errorf("%s mode = %o, want 750", path, info.Mode().Perm())
		}
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("file mode = %o, want 640", info.Mode().Perm())
	}
}

func TestForgetPlatformCleansTableAtLastScan(t *testing.T) {
	oldRunner := nftRunner
	defer func() { nftRunner = oldRunner }()
	var scripts []string
	nftRunner = func(_ context.Context, script string) error {
		scripts = append(scripts, script)
		return nil
	}
	platformMu.Lock()
	scans = map[string]struct{}{"a": {}, "b": {}}
	installed = true
	tableName = "xalgorix_egress_test"
	platformMu.Unlock()

	forgetPlatform("a")
	if len(scripts) != 0 {
		t.Fatalf("table cleaned too early: %v", scripts)
	}
	forgetPlatform("b")
	if len(scripts) != 1 || !strings.Contains(scripts[0], "delete table inet xalgorix_egress_test") {
		t.Fatalf("cleanup script = %v", scripts)
	}
}
