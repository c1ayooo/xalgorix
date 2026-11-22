package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/riverqueue/river"
)

// ToolboxQueue isolates container housekeeping from the cold-path queue.
//
// Rationale (charter §8): River drives cold-path catch-up only. Container
// lifecycle and tool dispatch stay synchronous in the scan loop; what River gets
// here is the idempotent janitorial work — reclaiming orphans, adopting
// containers after a crash, prefetching images, running selftests, and nudging
// the desired/applied watermark forward.
const ToolboxQueue = "toolbox"

// Job kinds. Stable strings: they are persisted in river_job.kind.
const (
	KindToolboxGC        = "xalgorix_toolbox_gc_sweep"
	KindToolboxReapIdle  = "xalgorix_toolbox_reap_idle"
	KindToolboxAdopt     = "xalgorix_toolbox_adopt"
	KindToolboxPrefetch  = "xalgorix_toolbox_prefetch_image"
	KindToolboxSelftest  = "xalgorix_toolbox_selftest"
	KindToolboxReconcile = "xalgorix_toolbox_reconcile_project"
	KindToolboxImageGC   = "xalgorix_toolbox_image_gc"
)

// ToolboxHandlers is the set of idempotent operations the workers perform. It is
// an interface (not the concrete runtime.Service) so jobs stays testable without
// a container runtime or a database.
type ToolboxHandlers interface {
	// ReconcileOnce applies pending desired-state rows.
	ReconcileOnce(ctx context.Context) error
	// ReclaimOrphans removes containers with this instance's labels but no
	// durable desired state.
	ReclaimOrphans(ctx context.Context) error
	// Adopt re-registers containers that survived a platform restart.
	Adopt(ctx context.Context) error
	// PrefetchImage ensures the image digest is present locally.
	PrefetchImage(ctx context.Context, image, profile string) error
	// Selftest runs the image's tool inventory check.
	Selftest(ctx context.Context, image, profile string) (passed int, failed int, report string, err error)
	// ImageGC drops stale toolbox images, keeping the newest N digests.
	ImageGC(ctx context.Context, keep int) error
}

// ── Job args ──

type GCJobArgs struct {
	InstanceID string `json:"instance_id"`
	DryRun     bool   `json:"dry_run,omitempty"`
}

func (GCJobArgs) Kind() string { return KindToolboxGC }

func (a GCJobArgs) Validate() error {
	if strings.TrimSpace(a.InstanceID) == "" {
		return errors.New("jobs: instance_id is required")
	}
	return nil
}

type ReapIdleJobArgs struct {
	InstanceID     string `json:"instance_id"`
	IdleTTLSeconds int    `json:"idle_ttl_seconds"`
	// MaxLifeSeconds is the hard cap on project age. 0 means "use the runtime
	// default" (XALGORIX_PROJECT_MAX_LIFE_SECONDS, 21600s), which is the normal
	// path while main.go does not pass the value yet.
	MaxLifeSeconds int `json:"max_life_seconds"`
}

func (ReapIdleJobArgs) Kind() string { return KindToolboxReapIdle }

func (a ReapIdleJobArgs) Validate() error {
	if strings.TrimSpace(a.InstanceID) == "" {
		return errors.New("jobs: instance_id is required")
	}
	if a.MaxLifeSeconds < 0 {
		return fmt.Errorf("jobs: max_life_seconds must be >= 0, got %d", a.MaxLifeSeconds)
	}
	return nil
}

type AdoptJobArgs struct {
	InstanceID string `json:"instance_id"`
}

func (AdoptJobArgs) Kind() string { return KindToolboxAdopt }

func (a AdoptJobArgs) Validate() error {
	if strings.TrimSpace(a.InstanceID) == "" {
		return errors.New("jobs: instance_id is required")
	}
	return nil
}

type PrefetchImageJobArgs struct {
	Image   string `json:"image"`
	Profile string `json:"profile"`
}

func (PrefetchImageJobArgs) Kind() string { return KindToolboxPrefetch }

func (a PrefetchImageJobArgs) Validate() error {
	if strings.TrimSpace(a.Image) == "" {
		return errors.New("jobs: image is required")
	}
	return nil
}

type SelftestJobArgs struct {
	Image   string `json:"image"`
	Profile string `json:"profile"`
}

func (SelftestJobArgs) Kind() string { return KindToolboxSelftest }

func (a SelftestJobArgs) Validate() error {
	if strings.TrimSpace(a.Image) == "" {
		return errors.New("jobs: image is required")
	}
	return nil
}

type ReconcileProjectJobArgs struct {
	ProjectID  string `json:"project_id"`
	DesiredGen int64  `json:"desired_gen"`
}

func (ReconcileProjectJobArgs) Kind() string { return KindToolboxReconcile }

func (a ReconcileProjectJobArgs) Validate() error {
	if strings.TrimSpace(a.ProjectID) == "" {
		return errors.New("jobs: project_id is required")
	}
	return nil
}

type ImageGCJobArgs struct {
	KeepDigests int `json:"keep_digests"`
}

func (ImageGCJobArgs) Kind() string { return KindToolboxImageGC }

func (a ImageGCJobArgs) Validate() error {
	if a.KeepDigests < 1 {
		return fmt.Errorf("jobs: keep_digests must be >= 1, got %d", a.KeepDigests)
	}
	return nil
}

// ── Workers ──

type gcWorker struct {
	river.WorkerDefaults[GCJobArgs]
	handlers ToolboxHandlers
}

func (w *gcWorker) Work(ctx context.Context, job *river.Job[GCJobArgs]) error {
	if w.handlers == nil {
		return errors.New("jobs: toolbox handlers are not configured")
	}
	if err := job.Args.Validate(); err != nil {
		return err
	}
	if job.Args.DryRun {
		log.Printf("[toolbox] gc dry-run for instance %s", job.Args.InstanceID)
		return nil
	}
	if err := w.handlers.ReclaimOrphans(ctx); err != nil {
		return err
	}
	return w.handlers.ReconcileOnce(ctx)
}

type adoptWorker struct {
	river.WorkerDefaults[AdoptJobArgs]
	handlers ToolboxHandlers
}

func (w *adoptWorker) Work(ctx context.Context, job *river.Job[AdoptJobArgs]) error {
	if w.handlers == nil {
		return errors.New("jobs: toolbox handlers are not configured")
	}
	if err := job.Args.Validate(); err != nil {
		return err
	}
	return w.handlers.Adopt(ctx)
}

type reapIdleWorker struct {
	river.WorkerDefaults[ReapIdleJobArgs]
	handlers ToolboxHandlers
}

func (w *reapIdleWorker) Work(ctx context.Context, job *river.Job[ReapIdleJobArgs]) error {
	if w.handlers == nil {
		return errors.New("jobs: toolbox handlers are not configured")
	}
	if err := job.Args.Validate(); err != nil {
		return err
	}
	// Idle reaping is expressed as a full reconcile pass: the reconciler is the
	// only component allowed to decide that a project is finished, so the TTL and
	// the max-life cap are applied there (see runtime.Toolbox.ReapIdle). A zero
	// MaxLifeSeconds is passed through as 0 so the runtime default applies.
	if reaper, ok := w.handlers.(interface {
		ReapIdle(ctx context.Context, ttl, maxLife time.Duration) error
	}); ok {
		ttl := time.Duration(job.Args.IdleTTLSeconds) * time.Second
		maxLife := time.Duration(job.Args.MaxLifeSeconds) * time.Second
		return reaper.ReapIdle(ctx, ttl, maxLife)
	}
	return nil
}

type prefetchWorker struct {
	river.WorkerDefaults[PrefetchImageJobArgs]
	handlers ToolboxHandlers
}

func (w *prefetchWorker) Work(ctx context.Context, job *river.Job[PrefetchImageJobArgs]) error {
	if w.handlers == nil {
		return errors.New("jobs: toolbox handlers are not configured")
	}
	if err := job.Args.Validate(); err != nil {
		return err
	}
	return w.handlers.PrefetchImage(ctx, job.Args.Image, job.Args.Profile)
}

type selftestWorker struct {
	river.WorkerDefaults[SelftestJobArgs]
	handlers ToolboxHandlers
}

func (w *selftestWorker) Work(ctx context.Context, job *river.Job[SelftestJobArgs]) error {
	if w.handlers == nil {
		return errors.New("jobs: toolbox handlers are not configured")
	}
	if err := job.Args.Validate(); err != nil {
		return err
	}
	passed, failed, report, err := w.handlers.Selftest(ctx, job.Args.Image, job.Args.Profile)
	if err != nil {
		return err
	}
	log.Printf("[toolbox] selftest %s: passed=%d failed=%d %s", job.Args.Image, passed, failed, report)
	return nil
}

type reconcileProjectWorker struct {
	river.WorkerDefaults[ReconcileProjectJobArgs]
	handlers ToolboxHandlers
}

func (w *reconcileProjectWorker) Work(ctx context.Context, job *river.Job[ReconcileProjectJobArgs]) error {
	if w.handlers == nil {
		return errors.New("jobs: toolbox handlers are not configured")
	}
	if err := job.Args.Validate(); err != nil {
		return err
	}
	return w.handlers.ReconcileOnce(ctx)
}

type imageGCWorker struct {
	river.WorkerDefaults[ImageGCJobArgs]
	handlers ToolboxHandlers
}

func (w *imageGCWorker) Work(ctx context.Context, job *river.Job[ImageGCJobArgs]) error {
	if w.handlers == nil {
		return errors.New("jobs: toolbox handlers are not configured")
	}
	if err := job.Args.Validate(); err != nil {
		return err
	}
	return w.handlers.ImageGC(ctx, job.Args.KeepDigests)
}

// registerToolboxWorkers adds every toolbox worker to an existing worker set.
func registerToolboxWorkers(workers *river.Workers, handlers ToolboxHandlers) {
	river.AddWorker(workers, &gcWorker{handlers: handlers})
	river.AddWorker(workers, &adoptWorker{handlers: handlers})
	river.AddWorker(workers, &reapIdleWorker{handlers: handlers})
	river.AddWorker(workers, &prefetchWorker{handlers: handlers})
	river.AddWorker(workers, &selftestWorker{handlers: handlers})
	river.AddWorker(workers, &reconcileProjectWorker{handlers: handlers})
	river.AddWorker(workers, &imageGCWorker{handlers: handlers})
}

// EnqueueToolbox inserts one toolbox job. Args are validated by the worker too;
// validating here fails fast at the caller.
func (m *Manager) EnqueueToolbox(ctx context.Context, args river.JobArgs) error {
	if m == nil || m.client == nil {
		return errors.New("jobs: manager unavailable")
	}
	if validator, ok := args.(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return err
		}
	}
	if _, err := m.client.Insert(ctx, args, &river.InsertOpts{
		Queue:       ToolboxQueue,
		MaxAttempts: 5,
	}); err != nil {
		return fmt.Errorf("jobs: enqueue %s: %w", args.Kind(), err)
	}
	return nil
}

// EnqueueToolboxUnique deduplicates pending work by args, which is what periodic
// housekeeping wants: two ticks in the same window must not double-sweep.
func (m *Manager) EnqueueToolboxUnique(ctx context.Context, args river.JobArgs, period time.Duration) error {
	if m == nil || m.client == nil {
		return errors.New("jobs: manager unavailable")
	}
	if period <= 0 {
		period = 60 * time.Second
	}
	if validator, ok := args.(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return err
		}
	}
	if _, err := m.client.Insert(ctx, args, &river.InsertOpts{
		Queue:       ToolboxQueue,
		MaxAttempts: 3,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: period,
		},
	}); err != nil {
		return fmt.Errorf("jobs: enqueue %s: %w", args.Kind(), err)
	}
	return nil
}

// EnqueueReconcileProject requests a watermark nudge for one project. Unlike the
// janitorial jobs this one is allowed to retry indefinitely: it is the crash
// catch-up channel, and giving up would strand a project half-created.
func (m *Manager) EnqueueReconcileProject(ctx context.Context, projectID string, desiredGen int64) error {
	if m == nil || m.client == nil {
		return errors.New("jobs: manager unavailable")
	}
	if _, err := m.client.Insert(ctx, ReconcileProjectJobArgs{ProjectID: projectID, DesiredGen: desiredGen}, &river.InsertOpts{
		Queue:       ToolboxQueue,
		MaxAttempts: 0, // unlimited: this is the recovery path
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: 30 * time.Second,
		},
	}); err != nil {
		return fmt.Errorf("jobs: enqueue reconcile for %s: %w", projectID, err)
	}
	return nil
}
