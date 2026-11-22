package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ProjectMaxLifeEnv is the environment knob for the hard upper bound on a
	// project container's lifetime. Unlike the idle TTL it is not reset by
	// activity: a long-running scan still cannot keep a container forever.
	ProjectMaxLifeEnv = "XALGORIX_PROJECT_MAX_LIFE_SECONDS"
	// DefaultProjectMaxLife is the fallback when the env value is missing,
	// unparseable, or non-positive (21600s = 6h).
	DefaultProjectMaxLife = 21600 * time.Second
)

// projectMaxLifeFromEnv parses ProjectMaxLifeEnv. Invalid or <= 0 falls back to
// DefaultProjectMaxLife: a missing knob must never silently disable the cap.
func projectMaxLifeFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv(ProjectMaxLifeEnv))
	if raw == "" {
		return DefaultProjectMaxLife
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return DefaultProjectMaxLife
	}
	return time.Duration(secs) * time.Second
}

// Toolbox binds a driver, the desired-state store and the reconciler into the
// handler set the River toolbox workers call.
//
// Everything here is idempotent and safe to run concurrently with the hot path.
type Toolbox struct {
	Driver     Driver
	Store      DesiredStateStore
	InstanceID string
	Image      string
	Profile    string

	reconciler *Reconciler
	sweeper    *OrphanSweeper

	// maxLife is the hard upper bound on a project's lifetime (created_at based).
	// It is a safety net on top of the idle TTL, never a replacement for it.
	maxLife time.Duration

	mu       sync.Mutex
	lastExec map[string]time.Time
	// inFlight counts tool executions currently running per project. A long
	// command is activity, not idleness, so idle reaping must skip a project
	// while at least one exec is in flight (see BeginExec).
	inFlight map[string]int

	// RecordHealth persists a selftest result (PostgreSQL projection). Optional.
	RecordHealth func(ctx context.Context, h ImageHealthReport) error
	logf         func(format string, args ...any)
}

// ImageHealthReport is a selftest outcome.
type ImageHealthReport struct {
	ImageDigest string `json:"image_digest"`
	Profile     string `json:"profile"`
	Passed      int    `json:"passed"`
	Failed      int    `json:"failed"`
	Report      string `json:"report"`
}

// NewToolbox wires the pieces. A nil store yields a toolbox whose housekeeping
// is a no-op, which is what native mode wants.
func NewToolbox(driver Driver, store DesiredStateStore, instanceID, image, profile string) *Toolbox {
	t := &Toolbox{
		Driver:     driver,
		Store:      store,
		InstanceID: instanceID,
		Image:      image,
		Profile:    profile,
		maxLife:    projectMaxLifeFromEnv(),
		lastExec:   make(map[string]time.Time),
		inFlight:   make(map[string]int),
		logf:       log.Printf,
	}
	if driver != nil && store != nil {
		t.reconciler = NewReconciler(driver, store)
		t.reconciler.SetLogger(t.logf)
		t.sweeper = NewOrphanSweeper(driver, store, instanceID)
		t.sweeper.logf = t.logf
	}
	return t
}

// Enabled reports whether housekeeping can run.
func (t *Toolbox) Enabled() bool { return t != nil && t.reconciler != nil }

// Touch records activity so idle reaping does not kill a busy project.
//
// Touch is a one-shot marker, not a heartbeat: a single tool invocation longer
// than the idle TTL would otherwise look idle for its whole duration. Callers
// that wrap an actual Exec must use BeginExec for the duration of the call.
func (t *Toolbox) Touch(projectID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.lastExec[projectID] = time.Now()
	t.mu.Unlock()
}

// BeginExec marks one tool execution as running for projectID and returns a
// release func that records the end of that execution. The release func is
// idempotent and safe to call more than once.
//
// While a project has at least one exec in flight, ReapIdle skips it: a long
// command is activity, not idleness. The max-life cap still applies, because it
// is the hard upper bound rather than an activity heuristic.
//
// Usage at the exec call site:
//
//	defer tb.BeginExec(projectID)()
func (t *Toolbox) BeginExec(projectID string) (release func()) {
	if t == nil || projectID == "" {
		return func() {}
	}
	t.mu.Lock()
	t.inFlight[projectID]++
	t.lastExec[projectID] = time.Now()
	t.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if n := t.inFlight[projectID] - 1; n > 0 {
				t.inFlight[projectID] = n
			} else {
				delete(t.inFlight, projectID)
			}
			t.lastExec[projectID] = time.Now()
		})
	}
}

// executing reports whether projectID has a tool execution in flight.
func (t *Toolbox) executing(projectID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inFlight[projectID] > 0
}

// LastExec returns the last observed activity for a project.
func (t *Toolbox) LastExec(projectID string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastExec[projectID]
}

// ReconcileOnce implements jobs.ToolboxHandlers.
func (t *Toolbox) ReconcileOnce(ctx context.Context) error {
	if !t.Enabled() {
		return nil
	}
	return t.reconciler.ReconcileOnce(ctx)
}

// ReclaimOrphans implements jobs.ToolboxHandlers.
func (t *Toolbox) ReclaimOrphans(ctx context.Context) error {
	if !t.Enabled() {
		return nil
	}
	return t.sweeper.Sweep(ctx)
}

// Adopt implements jobs.ToolboxHandlers.
//
// Adoption re-links containers that outlived a platform restart to their durable
// desired state. It never adopts a container without a row: that is an orphan,
// and orphans belong to ReclaimOrphans.
func (t *Toolbox) Adopt(ctx context.Context) error {
	if !t.Enabled() {
		return nil
	}
	lister, ok := t.Driver.(ProjectLister)
	if !ok {
		return nil
	}
	containers, err := lister.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("runtime: adopt list: %w", err)
	}
	for _, c := range containers {
		row, getErr := t.Store.Get(ctx, c.ProjectID)
		if getErr != nil {
			continue
		}
		switch row.DesiredState {
		case DesiredRemoved:
			if err := t.Driver.RemoveProject(ctx, c.ProjectID); err != nil && !IsCode(err, CodeNotFound) {
				t.logf("[toolbox] adopt: remove %s: %v", c.ProjectID, err)
			}
			_ = t.Store.Delete(ctx, c.ProjectID)
		case DesiredStopping:
			_ = t.Driver.StopProject(ctx, c.ProjectID, 10*time.Second)
		default:
			if row.ContainerID == c.ContainerID && row.AppliedGen >= row.DesiredGen {
				continue
			}
			if err := t.Store.MarkApplied(ctx, c.ProjectID, row.DesiredGen, c.ContainerID, c.State); err != nil {
				t.logf("[toolbox] adopt: mark applied %s: %v", c.ProjectID, err)
			}
		}
	}
	return nil
}

// ReapIdle stops projects that are inactive past the TTL or older than the hard
// maximum lifetime, then reconciles once so the stop reaches the runtime.
//
// The two guards are independent: ttl <= 0 disables only the idle branch, it
// never disables the max-life cap. maxLife <= 0 means "use the toolbox default"
// parsed from XALGORIX_PROJECT_MAX_LIFE_SECONDS at construction time.
//
// Rows come from Store.Active, not Unreconciled: an ordinary running project is
// already applied (applied_gen >= desired_gen, status=applied), so the
// unreconciled view would never show the projects this reaper must consider.
func (t *Toolbox) ReapIdle(ctx context.Context, ttl, maxLife time.Duration) error {
	if !t.Enabled() {
		return nil
	}
	if maxLife <= 0 {
		maxLife = t.maxLife
	}
	if ttl <= 0 && maxLife <= 0 {
		return nil
	}
	rows, err := t.Store.Active(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if row.DesiredState != DesiredReady && row.DesiredState != DesiredPending {
			continue
		}
		reason := reapReason(t, row, ttl, maxLife, now)
		if reason == "" {
			continue
		}
		if _, err := t.Store.RecordProject(ctx, DesiredProject{
			ProjectID: row.ProjectID, ScanID: row.ScanID, InstanceID: row.InstanceID,
			ImageDigest: row.ImageDigest, Profile: row.Profile, NetProfile: row.NetProfile,
			DesiredState: DesiredStopping, Spec: row.Spec,
		}); err != nil {
			t.logf("[toolbox] reap %s: %v", row.ProjectID, err)
			continue
		}
		t.logf("[toolbox] reaping project %s: %s", row.ProjectID, reason)
	}
	return t.reconciler.ReconcileOnce(ctx)
}

// reapReason returns a human-readable reason to stop the project, or "" to keep
// it. Max life wins over idle, since it is the harder safety bound.
func reapReason(t *Toolbox, row DesiredProject, ttl, maxLife time.Duration, now time.Time) string {
	// created_at is the max-life anchor. When it is missing (hand-built rows,
	// legacy data) fall back to updated_at; when both are zero the row's age is
	// unknowable, so no hard cap applies.
	anchor := row.CreatedAt
	if anchor.IsZero() {
		anchor = row.UpdatedAt
	}
	if maxLife > 0 && !anchor.IsZero() && now.Sub(anchor) >= maxLife {
		return fmt.Sprintf("exceeded max life %s (age %s)", maxLife.Round(time.Second), now.Sub(anchor).Round(time.Second))
	}
	if ttl <= 0 {
		return ""
	}
	// A project with a running tool call is busy even if the call started
	// before the TTL elapsed: there is no per-exec heartbeat, so the only safe
	// signal is the in-flight counter.
	if t.executing(row.ProjectID) {
		return ""
	}
	last := t.LastExec(row.ProjectID)
	if last.IsZero() {
		last = row.UpdatedAt
	}
	if now.Sub(last) < ttl {
		return ""
	}
	return fmt.Sprintf("idle %s", now.Sub(last).Round(time.Second))
}

// PrefetchImage implements jobs.ToolboxHandlers.
func (t *Toolbox) PrefetchImage(ctx context.Context, image, profile string) error {
	if t == nil || t.Driver == nil {
		return nil
	}
	puller, ok := t.Driver.(interface{ PullImage(context.Context) error })
	if !ok {
		return nil
	}
	_ = image
	_ = profile
	return puller.PullImage(ctx)
}

// Selftest runs the image's tool inventory check inside a throwaway project
// container and returns the counts.
func (t *Toolbox) Selftest(ctx context.Context, image, profile string) (int, int, string, error) {
	if t == nil || t.Driver == nil {
		return 0, 0, "", fmt.Errorf("runtime: selftest requires a container driver")
	}
	profile = orDefault(profile, t.Profile)
	projectID := "selftest-" + profile
	dir := selftestDir(projectID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, 0, "", fmt.Errorf("runtime: prepare selftest dir: %w", err)
	}
	spec, err := BuildProject(ProjectOptions{
		InstanceID: t.InstanceID,
		ScanID:     projectID,
		ScanDir:    dir,
		Image:      orDefault(image, t.Image),
		Profile:    profile,
		NetProfile: NetProfileNone,
	})
	if err != nil {
		return 0, 0, "", err
	}
	if _, err := t.Driver.EnsureProject(ctx, spec); err != nil {
		return 0, 0, "", err
	}
	defer func() { _ = t.Driver.RemoveProject(context.WithoutCancel(ctx), projectID) }()

	res, err := t.Driver.Exec(ctx, ExecSpec{
		ProjectID: projectID,
		ScanID:    projectID,
		Tool:      "toolbox_selftest",
		Argv:      []string{"/opt/xalgorix/bin/toolbox-selftest", "--profile", profile, "--json"},
		Cwd:       "/",
		Timeout:   5 * time.Minute,
	})
	if err != nil {
		return 0, 0, "", err
	}
	var report struct {
		Passed []string `json:"passed"`
		Failed []string `json:"failed"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &report); err != nil {
		return 0, 0, truncateForReport(res.Stdout + res.Stderr), fmt.Errorf("runtime: parse selftest output: %w", err)
	}
	detail := fmt.Sprintf("passed=%d failed=%d exit=%d", len(report.Passed), len(report.Failed), res.ExitCode)
	if t.RecordHealth != nil {
		_ = t.RecordHealth(ctx, ImageHealthReport{
			ImageDigest: digestOf(orDefault(image, t.Image)),
			Profile:     profile,
			Passed:      len(report.Passed),
			Failed:      len(report.Failed),
			Report:      strings.Join(report.Failed, ","),
		})
	}
	return len(report.Passed), len(report.Failed), detail, nil
}

func truncateForReport(s string) string {
	if len(s) > 2000 {
		return s[:2000]
	}
	return s
}

func selftestDir(projectID string) string {
	// Selftest does not touch any scan artefacts; a stable temp path keeps the
	// same-path mount contract satisfied without creating project state.
	return "/tmp/xalgorix-" + projectID
}

// ImageGC implements jobs.ToolboxHandlers.
func (t *Toolbox) ImageGC(ctx context.Context, keep int) error {
	if t == nil || t.Driver == nil {
		return nil
	}
	gc, ok := t.Driver.(interface {
		GarbageCollectImages(context.Context, int) error
	})
	if !ok {
		return nil
	}
	return gc.GarbageCollectImages(ctx, keep)
}
