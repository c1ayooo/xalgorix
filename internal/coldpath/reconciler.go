package coldpath

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem/postgres"
)

type watermarkReader interface {
	Watermark(ctx context.Context, scanID string) (postgres.Watermark, error)
}

type rangeEnqueuer interface {
	EnqueueRange(ctx context.Context, scanID, scanDir string, fromSeq, toSeq uint64) error
}

// Reconciler compares local journals with PostgreSQL watermarks and enqueues
// missing ranges. River handles latency; the reconciler handles crashes between
// JSON persistence and job enqueue.
type Reconciler struct {
	root     string
	store    watermarkReader
	enqueuer rangeEnqueuer
}

func NewReconciler(root string, store watermarkReader, enqueuer rangeEnqueuer) (*Reconciler, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("coldpath: data root is required")
	}
	if store == nil || enqueuer == nil {
		return nil, errors.New("coldpath: watermark reader and enqueuer are required")
	}
	return &Reconciler{root: filepath.Clean(root), store: store, enqueuer: enqueuer}, nil
}

func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[coldpath] reconcile warning: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ReconcileOnce enqueues every journal whose durable last sequence is ahead of
// its PostgreSQL watermark. Sources are both journal families: legacy
// per-scan files under the data root (pre-R6 directories; the R6.0 importer
// keeps the directories) and the PG primary store's run_journal streams —
// from R6.1 production journals have no file, so crash catch-up enumerates
// the durable stream through the process backend (charter §6.4). Applying a
// range is idempotent, so a scan present in both families may be enqueued
// twice without double-applying.
func (r *Reconciler) ReconcileOnce(ctx context.Context) error {
	var errs []error
	err := filepath.WalkDir(r.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			errs = append(errs, walkErr)
			return nil
		}
		if entry.IsDir() || entry.Name() != domainjournal.FileName {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		records, _, readErr := domainjournal.ReadRecords(path)
		if readErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, readErr))
			return nil
		}
		if len(records) == 0 {
			return nil
		}
		scanID := records[0].ScanID
		var last uint64
		for _, rec := range records {
			if rec.ScanID != scanID {
				errs = append(errs, fmt.Errorf("%s: mixed scan IDs %q and %q", path, scanID, rec.ScanID))
				return nil
			}
			if rec.Seq > last {
				last = rec.Seq
			}
		}
		wm, wmErr := r.store.Watermark(ctx, scanID)
		if wmErr != nil {
			errs = append(errs, fmt.Errorf("%s: watermark: %w", path, wmErr))
			return nil
		}
		if last <= wm.AppliedSeq {
			return nil
		}
		relDir, relErr := filepath.Rel(r.root, filepath.Dir(path))
		if relErr != nil {
			errs = append(errs, fmt.Errorf("%s: relative dir: %w", path, relErr))
			return nil
		}
		if relDir == "." || relDir == "" {
			errs = append(errs, fmt.Errorf("%s: journal is outside a scan directory", path))
			return nil
		}
		if enqueueErr := r.enqueuer.EnqueueRange(ctx, scanID, relDir, wm.AppliedSeq+1, last); enqueueErr != nil {
			errs = append(errs, fmt.Errorf("%s: enqueue: %w", path, enqueueErr))
		}
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	if err := r.reconcilePGSources(ctx, &errs); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// reconcilePGSources enqueues the missing tail of every run_journal stream
// reported by the process backend. The relative scan dir passed downstream
// is the scan id itself — scan directories are named by scan id, so the
// processor's legacy file fallback resolves the conventional path (and is
// simply absent for journals that only ever lived in PG).
func (r *Reconciler) reconcilePGSources(ctx context.Context, errs *[]error) error {
	backend := domainjournal.CurrentBackend()
	if backend == nil {
		return nil
	}
	sources, err := backend.LoadJournalSources(ctx)
	if err != nil {
		// The backstop being unable to see the durable streams is itself a
		// visible failure (charter §6.4) — reported, never silent; the file
		// family above has already been processed.
		return fmt.Errorf("coldpath: journal sources: %w", err)
	}
	for _, src := range sources {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		wm, wmErr := r.store.Watermark(ctx, src.ScanID)
		if wmErr != nil {
			*errs = append(*errs, fmt.Errorf("%s: watermark: %w", src.ScanID, wmErr))
			continue
		}
		if src.LastSeq <= wm.AppliedSeq {
			continue
		}
		if enqueueErr := r.enqueuer.EnqueueRange(ctx, src.ScanID, src.ScanID, wm.AppliedSeq+1, src.LastSeq); enqueueErr != nil {
			*errs = append(*errs, fmt.Errorf("%s: enqueue: %w", src.ScanID, enqueueErr))
		}
	}
	return nil
}
