// Package portscan exposes chainreactors/gogo as a first-class agent tool.
//
// gogo is invoked as an EXTERNAL PROCESS rather than imported:
//
//   - gogo v2's module root is `package main`, so its CLI is the only stable
//     entry point;
//   - importing its core/engine packages would make the whole Xalgorix binary a
//     GPL-3.0 derivative work. Out-of-process invocation keeps this repository
//     Apache-2.0 (the GPL obligation is limited to redistributing the gogo
//     binary itself, see NOTICE);
//   - it matches how nmap/ffuf/katana are already consumed by the agent
//     (internal/agent/agent_prompt.go), so the deployment shape is unchanged.
//
// Everything the child process touches follows the same discipline as
// terminal_execute and python_action: process group + tree kill, egress proxy
// env and credential, sanitized environment, bounded IO, and rlimits. See
// exec.go.
package portscan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/terminal"
)

const (
	defaultTimeoutSeconds = 600  // 10 minutes — a /24 top-port sweep fits easily
	maxTimeoutSeconds     = 3600 // hard cap: one tool call must not own an hour
	defaultPorts          = "top2"
	maxSummaryRows        = 80
)

// Register adds the port_scan tool to the registry.
func Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name: "port_scan",
		Description: `Discover live hosts, open ports, service banners and technology fingerprints with gogo (chainreactors/gogo).
Use this instead of hand-building nmap/naabu command lines in terminal_execute: results come back parsed, deduplicated and archived, and the run is attributed to recon coverage (service_discovery / tech_fingerprint).

INPUTS: target accepts a single IP, a CIDR (10.0.0.0/24), or a hostname. ports accepts a gogo preset (top2, common, all) or an explicit list/range (80,443,8000-8100).

SCOPE: the target is checked against the scope guard — operator-machine and dashboard-listener addresses are refused. A call without a target is invalid; this tool never sweeps a default range.

FAILURE MODES: when the gogo binary is absent the tool returns an install hint rather than an error, so you should fall back to terminal_execute + nmap and not retry blindly. A timeout still returns partial results found so far.

⟦ex⟧Recon on a host:
  port_scan target=10.0.0.5 ports=top2
Full-range service sweep of a subnet:
  port_scan target=10.0.0.0/24 ports=all timeout=1800⟦/ex⟧`,
		Parameters: []tools.Parameter{
			{Name: "target", Description: "IP, CIDR or hostname to scan (e.g. 10.0.0.5, 10.0.0.0/24, app.example.com)", Required: true},
			{Name: "ports", Description: "gogo port preset (top2|common|all) or explicit list/range (80,443,8000-8100). Default: top2", Required: false},
			{Name: "mode", Description: "gogo scan mode: default (connect scan), ss (socket), sc (smart). Default: default", Required: false},
			{Name: "timeout", Description: "Overall timeout in seconds (default 600, max 3600)", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return Execute(context.Background(), r.GetScanContextID(), args)
		},
		ExecuteContext: func(ctx context.Context, args map[string]string) (tools.Result, error) {
			return Execute(ctx, r.GetScanContextID(), args)
		},
	})
}

// Execute runs one gogo scan and normalises the outcome into a Tool Result.
//
// Soft failures (missing binary, unreachable target, timeout, non-zero exit)
// are reported through Result.Output with an empty Error on purpose: the
// registry feeds any non-empty Result.Error into the circuit breaker
// (registry_test.go pins this), and an unreachable target is a normal finding
// about the target, not a broken tool.
func Execute(ctx context.Context, contextID string, args map[string]string) (tools.Result, error) {
	target := strings.TrimSpace(args["target"])
	if target == "" {
		return tools.Result{Error: "target is required: pass an IP, CIDR or hostname (this tool never scans a default range)"}, nil
	}
	if err := validateTarget(target); err != nil {
		return tools.Result{Error: err.Error()}, nil
	}

	ports := strings.TrimSpace(args["ports"])
	if ports == "" {
		ports = defaultPorts
	}
	if err := validatePorts(ports); err != nil {
		return tools.Result{Error: err.Error()}, nil
	}

	mode := strings.ToLower(strings.TrimSpace(args["mode"]))
	switch mode {
	case "", "default", "ss", "sc":
	default:
		return tools.Result{Error: fmt.Sprintf("invalid mode %q: use default, ss or sc", mode)}, nil
	}

	timeoutSec := defaultTimeoutSeconds
	if raw := strings.TrimSpace(args["timeout"]); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return tools.Result{Error: fmt.Sprintf("invalid timeout %q: must be a number of seconds", raw)}, nil
		}
		if parsed > 0 {
			timeoutSec = parsed
		}
		if timeoutSec > maxTimeoutSeconds {
			timeoutSec = maxTimeoutSeconds
		}
	}

	bin, err := locateGogo()
	if err != nil {
		if errors.Is(err, errGogoUnavailable) {
			return tools.Result{Output: gogoMissingHint}, nil
		}
		return tools.Result{Output: fmt.Sprintf("port_scan is misconfigured: %v\n\n%s", err, gogoMissingHint)}, nil
	}

	workDir := workDirFor(contextID)

	outcome, err := runGogo(ctx, scanOptions{
		Bin:       bin,
		Target:    target,
		Ports:     ports,
		Mode:      mode,
		WorkDir:   workDir,
		ContextID: contextID,
		Timeout:   time.Duration(timeoutSec) * time.Second,
	})
	if err != nil {
		// Only genuine internal failures land here: no launch slot, no archive
		// directory, unusable proxy, egress credential failure.
		return tools.Result{Error: fmt.Sprintf("port_scan could not run: %v", err)}, nil
	}

	return buildResult(outcome, target, ports, timeoutSec), nil
}

// buildResult parses the archive file (if any) and renders the model-facing
// output. Parsing is driven by the file, not stdout, so a truncated stdout
// buffer never loses findings.
func buildResult(outcome *scanOutcome, target, ports string, timeoutSec int) tools.Result {
	var b strings.Builder

	switch {
	case outcome.Canceled:
		b.WriteString("[CANCELED] port_scan was canceled; results below are partial.\n")
	case outcome.TimedOut:
		fmt.Fprintf(&b, "[TIMEOUT] gogo exceeded %ds; results below are partial.\n", timeoutSec)
	}

	sum, parseErr := parseArchive(outcome.ResultFile)
	if parseErr != nil {
		b.WriteString(fmt.Sprintf("Result file could not be read: %v\n", parseErr))
	}
	// Fallback: some gogo builds emit JSONL on stdout instead of honoring
	// -f. Recover those rows rather than reporting a clean negative.
	if sum == nil && strings.Contains(outcome.Stdout, "{") {
		if alt, altErr := parseJSONL(strings.NewReader(outcome.Stdout)); altErr == nil && len(alt.Records) > 0 {
			sum = alt
			outcome.ResultFile = "" // no archive exists; do not advertise a path
		}
	}

	if sum != nil {
		b.WriteString(renderSummary(sum, target, ports, outcome.ResultFile, maxSummaryRows))
	} else if !outcome.TimedOut && !outcome.Canceled {
		// No parseable output and no timeout: surface why, but as data.
		fmt.Fprintf(&b, "gogo produced no result lines (exit code %d).\n", outcome.ExitCode)
		if errText := trimForOutput(outcome.Stderr, 2000); errText != "" {
			b.WriteString("STDERR:\n")
			b.WriteString(errText)
			b.WriteString("\n")
		}
		if errText := trimForOutput(outcome.Stdout, 1000); errText != "" {
			b.WriteString("STDOUT:\n")
			b.WriteString(errText)
			b.WriteString("\n")
		}
		b.WriteString("If this repeats, the target may be unreachable or blocking probes; consider terminal_execute with nmap -Pn.\n")
	}

	if outcome.Truncated {
		b.WriteString("[note: child stdout/stderr exceeded the capture limit; the archive file is authoritative]\n")
	}

	result := tools.Result{Output: strings.TrimSpace(b.String())}
	if sum != nil {
		result.Metadata = map[string]any{
			"target":       target,
			"ports":        ports,
			"hosts":        len(sum.Hosts),
			"open_ports":   sum.Ports,
			"frameworks":   sum.Frameworks,
			"result_lines": sum.Lines,
			"malformed":    sum.Malformed,
			"archive":      outcome.ResultFile,
			"exit_code":    outcome.ExitCode,
			"timed_out":    outcome.TimedOut,
			"canceled":     outcome.Canceled,
		}
	}
	return result
}

// parseArchive opens the gogo JSONL file. A missing file is not an error: gogo
// exits before creating it when the target cannot be resolved at all.
func parseArchive(path string) (*scanSummary, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	info, statErr := f.Stat()
	if statErr == nil && info.Size() == 0 {
		return &scanSummary{}, nil
	}
	return parseJSONL(f)
}

// validateTarget rejects values that would be read as a flag or that cannot be
// a host token. exec.Command is not shell-interpreted, so this is about flag
// confusion and prompt-injection noise, not shell metacharacters.
func validateTarget(target string) error {
	if strings.HasPrefix(target, "-") {
		return fmt.Errorf("target %q must not start with '-': pass a host, not a flag", target)
	}
	if strings.ContainsAny(target, " \t\r\n;|&`$()<>\"'") {
		return fmt.Errorf("target %q contains characters that are not valid in a host or CIDR", target)
	}
	return nil
}

// validatePorts accepts gogo presets and the usual list/range syntax.
func validatePorts(ports string) error {
	if strings.HasPrefix(ports, "-") {
		return fmt.Errorf("ports %q must not start with '-'", ports)
	}
	for _, r := range ports {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == ',' || r == '-' || r == '_':
		default:
			return fmt.Errorf("ports %q contains an unsupported character %q: use a preset (top2|common|all) or a list/range (80,443,8000-8100)", ports, string(r))
		}
	}
	return nil
}

// workDirFor resolves the child's working directory with the same priority as
// the python tool: scan dir -> terminal cwd for this session -> workspace root.
func workDirFor(contextID string) string {
	if sc := scanctx.Get(contextID); sc != nil && sc.ScanDir != "" {
		return filepath.Clean(sc.ScanDir)
	}
	if wd := terminal.GetWorkDirForContext(contextID); wd != "" {
		return filepath.Clean(wd)
	}
	if root := strings.TrimSpace(config.Get().WorkspaceRoot); root != "" {
		return filepath.Clean(root)
	}
	return os.TempDir()
}

func trimForOutput(s string, limit int) string {
	s = strings.TrimSpace(s)
	if s == "" || limit <= 0 {
		return s
	}
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n... [truncated]"
}
