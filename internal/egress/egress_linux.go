//go:build linux

package egress

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

var (
	platformMu sync.Mutex
	scans      = map[string]struct{}{}
	installed  bool
	tableName  string

	// accessTrees remembers workspaces already transferred to the egress UID
	// so each command does not recursively chown an entire scan tree.
	accessTrees sync.Map

	// nftRunner is injectable so tests can verify rule generation without
	// touching the host firewall.
	nftRunner = func(ctx context.Context, script string) error {
		cmd := exec.CommandContext(ctx, "nft", "-f", "-")
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("nft: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

func preparePlatform(ctx context.Context, scanID string) (Status, error) {
	if os.Geteuid() != 0 {
		return Status{}, enforceUnavailable(scanID, fmt.Errorf("enforce mode requires root"))
	}
	if _, err := exec.LookPath("nft"); err != nil {
		return Status{}, enforceUnavailable(scanID, fmt.Errorf("nft binary not found"))
	}
	if !trafficproxy.Enabled() {
		return Status{}, enforceUnavailable(scanID, fmt.Errorf("traffic proxy is disabled"))
	}
	port, err := trafficproxy.LocalPort()
	if err != nil {
		return Status{}, enforceUnavailable(scanID, err)
	}
	uid, _, err := credentialIDs()
	if err != nil {
		return Status{}, enforceUnavailable(scanID, err)
	}

	platformMu.Lock()
	defer platformMu.Unlock()
	if !installed {
		name := fmt.Sprintf("xalgorix_egress_%d", os.Getpid())
		if err := nftRunner(ctx, nftScript(name, uid, port)); err != nil {
			return Status{}, enforceUnavailable(scanID, err)
		}
		tableName = name
		installed = true
	}
	scans[scanID] = struct{}{}
	return Status{
		Mode:     "enforce",
		Enforced: true,
		Reason:   fmt.Sprintf("uid %d restricted to loopback traffic proxy port %d", uid, port),
	}, nil
}

func forgetPlatform(scanID string) {
	platformMu.Lock()
	defer platformMu.Unlock()
	delete(scans, scanID)
	if len(scans) == 0 && installed {
		_ = nftRunner(context.Background(), "delete table inet "+tableName+"\n")
		installed = false
		tableName = ""
	}
}

// PrepareChildAccess transfers the scan workspace to the dedicated egress
// UID/GID before a child is launched. Without this, enforce mode drops the
// child to 65534 while the scan directory remains root-owned 0700, making the
// command unable to enter its cwd or read the proxy files written for it.
//
// Only one full walk is performed per workspace: after the root is owned by
// the egress UID, later calls only repair explicitly listed files.
func PrepareChildAccess(scanID, root string, files ...string) error {
	if strings.TrimSpace(root) == "" || StatusFor(scanID).Mode != "enforce" {
		return nil
	}
	uid, gid, err := credentialIDs()
	if err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("egress: inspect workspace %s: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("egress: workspace %s is not a directory", root)
	}
	key := scanID + "\x00" + root
	if _, loaded := accessTrees.LoadOrStore(key, struct{}{}); !loaded {
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			entryInfo, err := entry.Info()
			if err != nil {
				return err
			}
			return grantChildPath(path, entryInfo, uid, gid)
		})
		if err != nil {
			accessTrees.Delete(key)
			return fmt.Errorf("egress: prepare workspace %s: %w", root, err)
		}
	}
	for _, path := range files {
		fileInfo, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("egress: inspect child file %s: %w", path, err)
		}
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("egress: refusing symlink child file %s", path)
		}
		if err := grantChildPath(path, fileInfo, uid, gid); err != nil {
			return fmt.Errorf("egress: prepare child file %s: %w", path, err)
		}
	}
	return nil
}

func grantChildPath(path string, info fs.FileInfo, uid, gid int) error {
	mode := os.FileMode(0o640)
	if info.IsDir() || info.Mode()&0o111 != 0 {
		mode = 0o750
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// ApplyChildCredential drops a child process to the dedicated egress UID/GID.
// It is a no-op unless the scan is in enforce mode.
func ApplyChildCredential(cmd *exec.Cmd, scanID string) error {
	if cmd == nil || StatusFor(scanID).Mode != "enforce" {
		return nil
	}
	uid, gid, err := credentialIDs()
	if err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	return nil
}

func credentialIDs() (int, int, error) {
	uid := 65534
	gid := 65534
	if raw := strings.TrimSpace(os.Getenv("XALGORIX_EGRESS_UID")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return 0, 0, fmt.Errorf("invalid XALGORIX_EGRESS_UID %q", raw)
		}
		uid = n
	}
	if raw := strings.TrimSpace(os.Getenv("XALGORIX_EGRESS_GID")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return 0, 0, fmt.Errorf("invalid XALGORIX_EGRESS_GID %q", raw)
		}
		gid = n
	}
	return uid, gid, nil
}

func nftScript(table string, uid, port int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\n", table)
	fmt.Fprintf(&b, "add chain inet %s output { type filter hook output priority 0; policy accept; }\n", table)
	fmt.Fprintf(&b, "add rule inet %s output meta skuid %d ip daddr 127.0.0.1 tcp dport %d accept\n", table, uid, port)
	fmt.Fprintf(&b, "add rule inet %s output meta skuid %d ip6 daddr ::1 tcp dport %d accept\n", table, uid, port)
	fmt.Fprintf(&b, "add rule inet %s output meta skuid %d drop\n", table, uid)
	return b.String()
}
