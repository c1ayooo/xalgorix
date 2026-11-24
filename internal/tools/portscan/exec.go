package portscan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/egress"
	"github.com/c1ayooo/xalgorix/v4/internal/platformutil"
	egressproxy "github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/resources"
	toolrt "github.com/c1ayooo/xalgorix/v4/internal/runtime"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/iolimit"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/terminal"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// scanOptions is the fully-validated input to a single gogo invocation.
type scanOptions struct {
	Bin       string
	Target    string
	Ports     string
	Mode      string
	WorkDir   string
	ContextID string
	Timeout   time.Duration
}

// scanOutcome carries everything the caller needs to build a Tool Result. It
// is deliberately a value (not an error) for expected outcomes: a timeout, a
// non-zero exit, or a missing result file are normal for a network scanner run
// against an uncooperative target and must not trip the circuit breaker.
type scanOutcome struct {
	ResultFile string
	Stdout     string
	Stderr     string
	Truncated  bool
	ExitCode   int
	TimedOut   bool
	Canceled   bool
}

// buildArgs renders the gogo command line.
//
// Only flags confirmed by the gogo README are used: -i target, -p ports,
// -o jl (JSON Lines), -f output file, --no-color. Anything not confirmed
// upstream is deliberately omitted rather than guessed — an invalid flag makes
// gogo exit immediately, which would look like a scan failure forever.
func buildArgs(o scanOptions, resultFile string) []string {
	args := []string{
		"-i", o.Target,
		"-p", o.Ports,
		"-o", "jl",
		"-f", resultFile,
		"--no-color",
	}
	if mode := strings.ToLower(strings.TrimSpace(o.Mode)); mode != "" && mode != "default" {
		args = append(args, "-m", mode)
	}
	return args
}

// runGogo executes gogo as a bounded child process and returns its outcome.
//
// The child is configured exactly like terminal_execute / python_action:
// dedicated process group (so a timeout reaps grandchildren), egress env +
// credential so a scanner that bypasses Go's DialGuard still honors the
// operator's proxy posture, sanitized environment so model/provider keys never
// reach a binary that prints its env, and iolimit buffers so a chatty run
// cannot exhaust memory.
//
// gogo writes results to resultFile (-o jl -f ...); stdout is captured for
// diagnostics only, so truncation never loses findings.
func runGogo(parent context.Context, o scanOptions) (*scanOutcome, error) {
	waitCtx := parent
	if waitCtx == nil {
		waitCtx = context.Background()
	}

	// A scan is a heavy tool: it fans out across ports. Reuse the same launch
	// queue as terminal/python so a port sweep cannot starve other work.
	lease, err := resources.AcquireToolLeaseContext(waitCtx, true, "port_scan")
	if err != nil {
		if errors.Is(err, resources.ErrToolLeaseWaitTimeout) {
			return nil, fmt.Errorf("tool launch queue stayed saturated for %s: %w", resources.DefaultToolLeaseMaxWait, err)
		}
		return nil, err
	}
	defer lease.Release()

	ctx, cancel := context.WithTimeout(waitCtx, o.Timeout)
	defer cancel()

	resultFile := filepath.Join(o.WorkDir, "portscan", fmt.Sprintf("gogo-%d.jsonl", time.Now().UnixNano()))
	if err := os.MkdirAll(filepath.Dir(resultFile), 0o700); err != nil {
		return nil, fmt.Errorf("create portscan archive dir: %w", err)
	}

	// Container execution plane. gogo needs raw sockets, so the project must have
	// been admitted with net_profile=raw (NET_RAW); a proxy-only project rejects
	// the call here rather than silently degrading to a connect scan.
	if tools.ContainerActive() {
		return runGogoInContainer(ctx, o, resultFile)
	}

	cmd := exec.CommandContext(ctx, o.Bin, buildArgs(o, resultFile)...)
	cmd.Dir = o.WorkDir

	cmdEnv := tools.SanitizeEnviron(os.Environ())
	// Keep the child's HOME/TMPDIR inside the scan workspace so gogo's caches
	// and any stray .dat files never land in the operator's home directory.
	cmdEnv = append(cmdEnv,
		"HOME="+o.WorkDir,
		"TMPDIR="+filepath.Join(o.WorkDir, "tmp"),
	)
	_ = os.MkdirAll(filepath.Join(o.WorkDir, "tmp"), 0o700)
	if trafficproxy.Enabled() {
		// Required-proxy posture is fail-closed: no proxy, no scan.
		cmdEnv, err = trafficproxy.Environment(cmdEnv)
		if err != nil {
			return nil, fmt.Errorf("required proxy unavailable: %w", err)
		}
	} else {
		egressEnv, eerr := egressproxy.EgressEnv(o.ContextID, config.Get().ProxyRequired)
		if eerr != nil {
			return nil, fmt.Errorf("required proxy unavailable: %w", eerr)
		}
		cmdEnv = append(cmdEnv, egressEnv...)
	}
	cmdEnv = append(cmdEnv, egress.EnvForChild(o.ContextID)...)
	cmd.Env = cmdEnv

	platformutil.ConfigureProcessGroup(cmd)
	cmd.Cancel = func() error {
		_ = platformutil.KillProcessTree(cmd)
		return os.ErrProcessDone
	}

	if err := egress.PrepareChildAccess(o.ContextID, o.WorkDir); err != nil {
		return nil, fmt.Errorf("egress workspace: %w", err)
	}
	if err := egress.ApplyChildCredential(cmd, o.ContextID); err != nil {
		return nil, fmt.Errorf("egress credential: %w", err)
	}

	stdout := iolimit.New(1 << 20)
	stderr := iolimit.New(1 << 19)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start gogo: %w", err)
	}
	// Post-start rlimit: gogo is a memory-bounded scanner, but a hostile target
	// could still drive it into a deep-scan allocation spike.
	terminal.ApplyProcessLimitsWithLimit(cmd, true, lease.MemoryLimitBytes())
	terminal.TrackProcessForContext(o.ContextID, cmd, cancel, "port_scan: "+o.Target)
	defer terminal.UntrackProcessForContext(o.ContextID, cmd)

	waitErr := cmd.Wait()

	out := &scanOutcome{
		ResultFile: resultFile,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		Truncated:  stdout.Truncated() || stderr.Truncated(),
	}
	if waitErr != nil {
		exitErr := &exec.ExitError{}
		if errors.As(waitErr, &exitErr) {
			out.ExitCode = exitErr.ExitCode()
		} else {
			out.ExitCode = -1
		}
	}
	switch ctx.Err() {
	case context.DeadlineExceeded:
		out.TimedOut = true
	case context.Canceled:
		out.Canceled = true
	}
	return out, nil
}

var portscanContainerSeq atomic.Uint64

// runGogoInContainer executes gogo inside the scan's project container and maps
// the result onto the same scanOutcome the native path produces.
func runGogoInContainer(ctx context.Context, o scanOptions, resultFile string) (*scanOutcome, error) {
	sc := scanctx.Get(o.ContextID)
	if sc == nil {
		return nil, fmt.Errorf("port_scan requires an active scan context for container execution")
	}
	bin := o.Bin
	if filepath.IsAbs(bin) {
		// Host paths do not exist in the container; the image publishes gogo on
		// PATH under this name.
		bin = filepath.Base(bin)
	}
	res, err := tools.RunInProject(ctx, sc, toolrt.ExecSpec{
		CallSeq:     portscanContainerSeq.Add(1),
		Tool:        "port_scan",
		Argv:        append([]string{bin}, buildArgs(o, resultFile)...),
		Cwd:         o.WorkDir,
		Timeout:     o.Timeout,
		StdoutLimit: iolimit.DefaultStdoutLimit,
		StderrLimit: iolimit.DefaultStderrLimit,
		ArtifactDir: filepath.Join(o.WorkDir, "tool-outputs"),
		Heavy:       true,
	})
	if err != nil {
		return nil, err
	}
	return &scanOutcome{
		ResultFile: resultFile,
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		Truncated:  res.StdoutTruncated || res.StderrTruncated,
		ExitCode:   res.ExitCode,
		TimedOut:   res.TimedOut,
		Canceled:   res.Canceled,
	}, nil
}
