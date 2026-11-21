// Package python provides the python_action tool via subprocess.
package python

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/egress"
	"github.com/xalgord/xalgorix/v4/internal/platformutil"
	egressproxy "github.com/xalgord/xalgorix/v4/internal/proxy"
	"github.com/xalgord/xalgorix/v4/internal/resources"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/storage"
	"github.com/xalgord/xalgorix/v4/internal/tools"
	"github.com/xalgord/xalgorix/v4/internal/tools/iolimit"
	"github.com/xalgord/xalgorix/v4/internal/tools/terminal"
	"github.com/xalgord/xalgorix/v4/internal/trafficproxy"
)

// configureProcessGroupKill starts cmd in its own process group and installs a
// Cancel that SIGKILLs the whole group when the context is canceled/times out.
//
// Without this, exec.CommandContext's default cancel sends SIGKILL only to the
// direct python3 PID. Because the child runs in a new process group (Setpgid),
// any grandchild the script spawns (subprocess.Popen/os.system/fork) survives
// as an orphan and keeps the inherited stdout/stderr pipes open, so cmd.Wait()
// blocks well past the timeout. Killing the negative PID (the whole group)
// reaps those orphans and lets Wait() return. Mirrors internal/tools/terminal.
func configureProcessGroupKill(cmd *exec.Cmd) {
	platformutil.ConfigureProcessGroup(cmd)
	cmd.Cancel = func() error {
		_ = platformutil.KillProcessTree(cmd)
		return os.ErrProcessDone
	}
}

// Register adds the python_action tool to the registry.
func Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name:        "python_action",
		Description: "Execute Python code in a subprocess. Python 3 must be installed.",
		Parameters: []tools.Parameter{
			{Name: "code", Description: "Python code to execute", Required: true},
			{Name: "timeout", Description: "Timeout in seconds (default: 1800 = 30 min)", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return executePythonForContext(r.GetScanContextID(), args)
		},
		ExecuteContext: func(ctx context.Context, args map[string]string) (tools.Result, error) {
			return executePythonForContextCtx(ctx, r.GetScanContextID(), args)
		},
	})
}

func executePython(args map[string]string) (tools.Result, error) {
	return executePythonForContext(scanctx.Default().ID, args)
}

func executePythonForContext(contextID string, args map[string]string) (tools.Result, error) {
	return executePythonForContextCtx(context.Background(), contextID, args)
}

func executePythonForContextCtx(parent context.Context, contextID string, args map[string]string) (tools.Result, error) {
	if strings.TrimSpace(contextID) == "" {
		contextID = scanctx.Default().ID
	}

	code := args["code"]
	if code == "" {
		return tools.Result{}, fmt.Errorf("code is required")
	}

	timeoutSec := 1800 // 30 minutes — exploit scripts can run long
	if t := args["timeout"]; t != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return tools.Result{Error: fmt.Sprintf("invalid timeout value '%s': must be a number in seconds", t)}, nil
		}
		timeoutSec = parsed
		if timeoutSec <= 0 {
			timeoutSec = 1800
		}
		if timeoutSec > 7200 { // Cap at 2 hours
			timeoutSec = 7200
		}
	}

	// Find python3
	pythonBin := "python3"
	if _, err := exec.LookPath(pythonBin); err != nil {
		pythonBin = "python"
		if _, err := exec.LookPath(pythonBin); err != nil {
			return tools.Result{}, fmt.Errorf("python not found. Install python3")
		}
	}

	waitCtx := pythonWaitContext(contextID)
	if parent != nil && parent.Done() != nil {
		waitCtx = parent
	}
	lease, err := resources.AcquireToolLeaseContext(waitCtx, false, "python_action")
	if err != nil {
		if errors.Is(err, resources.ErrToolLeaseWaitTimeout) {
			return tools.Result{Output: fmt.Sprintf("[THROTTLE] python_action could not start within %s: %v. The tool launch queue stayed saturated — a previous command may still hold resources. Retry with a shorter script; if this keeps happening the exec channel is wedged: record a typed disposition instead of queuing more work.", resources.DefaultToolLeaseMaxWait, err)}, nil
		}
		return tools.Result{Output: fmt.Sprintf("[CANCELED] python_action launch canceled before starting: %v", err)}, nil
	}
	defer lease.Release()

	ctx, cancel := context.WithTimeout(waitCtx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, pythonBin, "-c", code)
	// Resolve the python child's working directory in this priority order so
	// the .tmp/.cache/.config/.local scratch dirs created by
	// preparePythonWorkspace land inside an Allow_List root (R8.7, R8.10):
	//
	//   1. sc.ScanDir — when a Scan_Context owns the invocation, all
	//      python-tool side effects must stay inside that scan's working
	//      directory.
	//   2. terminal.GetWorkDirForContext — honors any explicit cd performed
	//      via the terminal tool inside the same agent session, so python
	//      and shell stay in sync.
	//   3. cfg.WorkspaceRoot (= Data_Dir) — the CLI / no-Scan_Context
	//      fallback. Per R8.10, no Filesystem_Tool is allowed to fall back
	//      to $CWD here; WorkspaceRoot is always an Allow_List descendant.
	scanDir := ""
	if sc := scanctx.Get(contextID); sc != nil && sc.ScanDir != "" {
		scanDir = sc.ScanDir
	} else if wd := terminal.GetWorkDirForContext(contextID); wd != "" {
		scanDir = wd
	} else {
		scanDir = config.Get().WorkspaceRoot
	}
	cmd.Dir = filepath.Clean(scanDir)
	preparePythonWorkspace(cmd.Dir)
	// pythonWorkspaceEnv roots HOME, TMPDIR, and XDG_{CACHE,CONFIG,DATA}_HOME
	// at cmd.Dir, which is now guaranteed to be sc.ScanDir or
	// cfg.WorkspaceRoot — both Allow_List descendants. That satisfies R8.8.
	cmdEnv := pythonWorkspaceEnv(cmd.Dir)
	if config.Get().ProxyRequired || egressproxy.Required() || trafficproxy.Enabled() {
		cmdEnv, err = trafficproxy.Environment(cmdEnv)
		if err != nil {
			return tools.Result{}, fmt.Errorf("required proxy unavailable: %w", err)
		}
	}
	cmdEnv, err = trafficProxyTLSBundleEnv(cmdEnv, cmd.Dir)
	if err != nil {
		return tools.Result{}, err
	}
	cmdEnv = append(cmdEnv, egress.EnvForChild(contextID)...)
	cmd.Env = cmdEnv
	configureProcessGroupKill(cmd)
	if err := egress.PrepareChildAccess(contextID, cmd.Dir); err != nil {
		return tools.Result{}, fmt.Errorf("egress workspace: %w", err)
	}
	if err := egress.ApplyChildCredential(cmd, contextID); err != nil {
		return tools.Result{}, fmt.Errorf("egress credential: %w", err)
	}

	stdout := iolimit.New(1 << 20)
	stderr := iolimit.New(1 << 19)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return tools.Result{Error: fmt.Sprintf("Failed to start python process: %v", err)}, nil
	}
	terminal.ApplyProcessLimitsWithLimit(cmd, true, lease.MemoryLimitBytes())

	// Register with terminal so watchdog knows we are active
	cleanCode := code
	if len(cleanCode) > 100 {
		cleanCode = cleanCode[:100] + "..."
	}
	terminal.TrackProcessForContext(contextID, cmd, cancel, "python: "+strings.ReplaceAll(cleanCode, "\n", " "))
	defer terminal.UntrackProcessForContext(contextID, cmd)

	waitErr := cmd.Wait()

	var b strings.Builder
	if stdout.Len() > 0 {
		out := stdout.String()
		if len(out) > 15000 {
			out = out[:15000] + "\n... [OUTPUT TRUNCATED]"
		}
		b.WriteString(out)
		if stdout.Truncated() {
			b.WriteString("\n... [OUTPUT TRUNCATED: exceeded 1MB]")
		}
	}

	if stderr.Len() > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("STDERR:\n")
		errOut := stderr.String()
		if len(errOut) > 5000 {
			errOut = errOut[:5000] + "\n... [TRUNCATED]"
		}
		b.WriteString(errOut)
		if stderr.Truncated() {
			b.WriteString("\n... [STDERR TRUNCATED: exceeded 512KB]")
		}
	}

	if waitErr != nil {
		switch ctx.Err() {
		case context.DeadlineExceeded:
			b.WriteString(fmt.Sprintf("\n[TIMEOUT: exceeded %ds]", timeoutSec))
			return tools.Result{Output: b.String(), Error: fmt.Sprintf("python_action timed out after %ds", timeoutSec)}, nil
		case context.Canceled:
			b.WriteString("\n[CANCELED] python_action canceled")
			return tools.Result{Output: b.String(), Error: "python_action canceled"}, nil
		}
		exitErr := &exec.ExitError{}
		if errors.As(waitErr, &exitErr) {
			b.WriteString(fmt.Sprintf("\n[exit code: %d]", exitErr.ExitCode()))
		}
	}

	if b.Len() == 0 {
		b.WriteString("(no output)")
	}

	return tools.Result{Output: b.String()}, nil
}

func pythonWaitContext(contextID string) context.Context {
	if sc := scanctx.Get(contextID); sc != nil && sc.Ctx != nil {
		return sc.Ctx
	}
	return context.Background()
}

// preparePythonWorkspace creates the .tmp/.cache/.config/.local/share scratch
// dirs that pythonWorkspaceEnv exports to the child via HOME, TMPDIR, and
// XDG_*. Callers MUST pass a workDir that is an Allow_List descendant
// (sc.ScanDir or cfg.WorkspaceRoot per R8.7, R8.10); this function does not
// validate the path itself and would otherwise leak directories into $CWD.
func preparePythonWorkspace(workDir string) {
	_ = os.MkdirAll(filepath.Join(workDir, ".tmp"), 0o700)
	_ = os.MkdirAll(filepath.Join(workDir, ".cache"), 0o700)
	_ = os.MkdirAll(filepath.Join(workDir, ".config"), 0o700)
	_ = os.MkdirAll(filepath.Join(workDir, ".local", "share"), 0o700)
}

func pythonWorkspaceEnv(workDir string) []string {
	replace := map[string]bool{
		"HOME":                    true,
		"TMPDIR":                  true,
		"XDG_CACHE_HOME":          true,
		"XDG_CONFIG_HOME":         true,
		"XDG_DATA_HOME":           true,
		"XALGORIX_WORKSPACE":      true,
		"PYTHONDONTWRITEBYTECODE": true,
	}
	// The agent must never see the operator's provider keys, admin-console
	// login, proxy credentials, or integration tokens through
	// `import os; os.environ` in a python_action call. The loop below drops the
	// whole XALGORIX_* namespace plus the non-namespaced operator secrets via the
	// shared tools.SecretEnvKey deny-list — the same one terminal.go uses, so the
	// two tools' environments cannot drift apart.
	env := make([]string, 0, len(os.Environ())+7)
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if replace[key] {
			continue
		}
		// Drop the whole XALGORIX_* namespace (tools never need it; the few safe
		// ones are re-added below) and any non-namespaced operator secret.
		if strings.HasPrefix(key, "XALGORIX_") || tools.SecretEnvKey(key) {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+workDir,
		"TMPDIR="+filepath.Join(workDir, ".tmp"),
		"XDG_CACHE_HOME="+filepath.Join(workDir, ".cache"),
		"XDG_CONFIG_HOME="+filepath.Join(workDir, ".config"),
		"XDG_DATA_HOME="+filepath.Join(workDir, ".local", "share"),
		"XALGORIX_WORKSPACE="+workDir,
		"PYTHONDONTWRITEBYTECODE=1",
	)
}

// trafficProxyTLSBundleEnv appends the TLS trust environment for the
// traffic-proxy CA. Parent-process injection (the egressproxy.Environment
// precedent) rather than sitecustomize: these keys are not XALGORIX_*, so
// they survive the subprocess env strip, and both requests and stdlib urllib
// honor them without a second generated file surface. SSL_CERT_FILE points
// at the combined bundle (traffic CA + system store) so non-intercepted TLS
// keeps working.
func trafficProxyTLSBundleEnv(env []string, workDir string) ([]string, error) {
	if !trafficproxy.MITMEnabled() {
		return env, nil
	}
	bundle := trafficproxy.BundlePath()
	if bundle == "" {
		return env, nil
	}
	// Enforce mode drops Python to a dedicated UID that cannot traverse the
	// root-only CA directory. Copy the public bundle into the workspace.
	data, err := os.ReadFile(bundle)
	if err != nil {
		return env, fmt.Errorf("read traffic proxy CA bundle: %w", err)
	}
	localBundle := filepath.Join(workDir, ".xalgorix-ca-bundle.pem")
	if err := storage.WriteAtomic(localBundle, data); err != nil {
		return env, fmt.Errorf("write workspace CA bundle: %w", err)
	}
	return append(env,
		"REQUESTS_CA_BUNDLE="+localBundle,
		"SSL_CERT_FILE="+localBundle,
	), nil
}
