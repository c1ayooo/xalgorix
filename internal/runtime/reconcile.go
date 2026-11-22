package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

// Reconciler drives desired state towards actual state.
//
// It is deliberately a reconciler and not a scheduler: it is idempotent, it
// tolerates arbitrary delay, and it is safe to run concurrently with the hot
// path (EnsureProject is idempotent). River jobs call ReconcileOnce; the ticker
// is the crash-catch-up backstop for "container created, PG never updated".
type Reconciler struct {
	driver Driver
	store  DesiredStateStore
	logf   func(format string, args ...any)
}

// NewReconciler builds a reconciler. A nil store disables it (native mode).
func NewReconciler(driver Driver, store DesiredStateStore) *Reconciler {
	return &Reconciler{driver: driver, store: store, logf: log.Printf}
}

// SetLogger overrides the logger (tests).
func (r *Reconciler) SetLogger(fn func(string, ...any)) {
	if fn != nil {
		r.logf = fn
	}
}

// Run ticks ReconcileOnce until the context is canceled.
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
			r.logf("[runtime] reconcile warning: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ReconcileOnce applies every row whose applied generation is behind.
func (r *Reconciler) ReconcileOnce(ctx context.Context) error {
	if r == nil || r.driver == nil || r.store == nil {
		return nil
	}
	rows, err := r.store.Unreconciled(ctx, 64)
	if err != nil {
		return fmt.Errorf("runtime: list unreconciled projects: %w", err)
	}
	var errs []error
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := r.apply(ctx, row); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", row.ProjectID, err))
		}
	}
	return errors.Join(errs...)
}

func (r *Reconciler) apply(ctx context.Context, row DesiredProject) error {
	gen := row.DesiredGen
	switch row.DesiredState {
	case DesiredRemoved:
		if err := r.driver.RemoveProject(ctx, row.ProjectID); err != nil &&
			!IsCode(err, CodeNotFound) {
			_ = r.store.MarkFailed(ctx, row.ProjectID, gen, err.Error())
			return err
		}
		return r.store.Delete(ctx, row.ProjectID)

	case DesiredStopping:
		if err := r.driver.StopProject(ctx, row.ProjectID, 10*time.Second); err != nil &&
			!IsCode(err, CodeNotFound) {
			_ = r.store.MarkFailed(ctx, row.ProjectID, gen, err.Error())
			return err
		}
		return r.store.MarkApplied(ctx, row.ProjectID, gen, "", StateStopped)

	default: // pending, ready
		spec, err := row.ProjectSpec()
		if err != nil {
			_ = r.store.MarkFailed(ctx, row.ProjectID, gen, err.Error())
			return err
		}
		handle, ensureErr := r.driver.EnsureProject(ctx, spec)
		if ensureErr != nil {
			if IsCode(ensureErr, CodeProjectLimit) || IsCode(ensureErr, CodeRuntimeUnavailable) {
				// Transient: leave the row pending so the next tick retries. A
				// gap marker would escalate too eagerly for ordinary pressure.
				return nil
			}
			_ = r.store.MarkFailed(ctx, row.ProjectID, gen, ensureErr.Error())
			return ensureErr
		}
		return r.store.MarkApplied(ctx, row.ProjectID, gen, handle.ContainerID, handle.State)
	}
}

// ProjectLister is implemented by drivers that can enumerate their containers.
// GC uses it to find orphans; drivers that cannot (native) simply are not
// garbage-collected.
type ProjectLister interface {
	ListProjects(ctx context.Context) ([]ProjectState, error)
}

// OrphanSweeper removes containers that carry this instance's labels but have no
// durable desired state. It is the counterpart of adoption: without it, a crash
// between container creation and the first PG write leaks a container forever.
type OrphanSweeper struct {
	driver     Driver
	store      DesiredStateStore
	instanceID string
	logf       func(format string, args ...any)
}

// NewOrphanSweeper builds a sweeper for one platform instance.
func NewOrphanSweeper(driver Driver, store DesiredStateStore, instanceID string) *OrphanSweeper {
	return &OrphanSweeper{driver: driver, store: store, instanceID: instanceID, logf: log.Printf}
}

// Sweep implements one GC pass. It never touches containers belonging to a
// different platform instance: adopting someone else's container would break
// scan isolation.
func (s *OrphanSweeper) Sweep(ctx context.Context) error {
	if s == nil || s.driver == nil || s.store == nil {
		return nil
	}
	lister, ok := s.driver.(ProjectLister)
	if !ok {
		return nil
	}
	containers, err := lister.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("runtime: list project containers: %w", err)
	}
	var errs []error
	for _, c := range containers {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, getErr := s.store.Get(ctx, c.ProjectID)
		switch {
		case getErr == nil:
			// Known project: only reclaim when its recorded container is gone
			// or superseded, which the reconciler handles. Nothing to do here.
			continue
		case !errors.Is(getErr, ErrProjectNotFound):
			errs = append(errs, fmt.Errorf("%s: %w", c.ProjectID, getErr))
			continue
		}
		if err := s.driver.RemoveProject(ctx, c.ProjectID); err != nil && !IsCode(err, CodeNotFound) {
			errs = append(errs, fmt.Errorf("%s: %w", c.ProjectID, err))
			continue
		}
		s.logf("[runtime] reclaimed orphan container %s (%s)", c.ProjectID, c.ContainerID)
	}
	return errors.Join(errs...)
}
