// Package jobs wires River to the optional PostgreSQL cold path. River never
// owns scan lifecycle, agentsgraph, HITL, or WebSocket work.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const DefaultQueue = "coldpath"

// Processor applies a bounded, ordered journal range to the cold path.
type Processor interface {
	ProcessRange(ctx context.Context, scanID, scanDir string, fromSeq, toSeq uint64) error
}

// IndexJobArgs requests one scan's journal range. Sequences are per-scan.
type IndexJobArgs struct {
	ScanID  string `json:"scan_id"`
	ScanDir string `json:"scan_dir"`
	FromSeq uint64 `json:"from_seq"`
	ToSeq   uint64 `json:"to_seq"`
}

func (IndexJobArgs) Kind() string { return "xalgorix_coldpath_index" }

func (a IndexJobArgs) Validate() error {
	if strings.TrimSpace(a.ScanID) == "" || strings.TrimSpace(a.ScanDir) == "" {
		return errors.New("jobs: scan_id and scan_dir are required")
	}
	if a.FromSeq == 0 || a.ToSeq < a.FromSeq {
		return fmt.Errorf("jobs: invalid sequence range %d..%d", a.FromSeq, a.ToSeq)
	}
	return nil
}

type indexWorker struct {
	river.WorkerDefaults[IndexJobArgs]
	processor Processor
}

func (w *indexWorker) Work(ctx context.Context, job *river.Job[IndexJobArgs]) error {
	if w.processor == nil {
		return errors.New("jobs: cold-path processor is not configured")
	}
	if err := job.Args.Validate(); err != nil {
		return err
	}
	return w.processor.ProcessRange(ctx, job.Args.ScanID, job.Args.ScanDir, job.Args.FromSeq, job.Args.ToSeq)
}

// Manager owns the River client and migrations.
type Manager struct {
	client *river.Client[pgx.Tx]
}

// ManagerOption customises NewManager without breaking its historical signature.
type ManagerOption func(*managerConfig)

type managerConfig struct {
	toolbox ToolboxHandlers
}

// WithToolbox registers the container-runtime janitorial workers on the toolbox
// queue. Without it the manager only drives the InfoItem cold path, which is the
// pre-existing behaviour.
func WithToolbox(handlers ToolboxHandlers) ManagerOption {
	return func(c *managerConfig) { c.toolbox = handlers }
}

// NewManager runs River's embedded migrations and constructs a client. It is
// only called when a DSN is configured.
func NewManager(ctx context.Context, pool *pgxpool.Pool, processor Processor, opts ...ManagerOption) (*Manager, error) {
	if pool == nil {
		return nil, errors.New("jobs: nil PostgreSQL pool")
	}
	if processor == nil {
		return nil, errors.New("jobs: nil processor")
	}
	var cfg managerConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return nil, fmt.Errorf("jobs: create River migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("jobs: apply River migrations: %w", err)
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, &indexWorker{processor: processor})
	queues := map[string]river.QueueConfig{
		DefaultQueue: {MaxWorkers: 2},
	}
	if cfg.toolbox != nil {
		registerToolboxWorkers(workers, cfg.toolbox)
		// Small worker count on purpose: housekeeping must never compete with a
		// running scan for CPU or for the container runtime's API.
		queues[ToolboxQueue] = river.QueueConfig{MaxWorkers: 3}
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:  queues,
		Workers: workers,
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: create River client: %w", err)
	}
	return &Manager{client: client}, nil
}

func (m *Manager) Start(ctx context.Context) error {
	if m == nil || m.client == nil {
		return errors.New("jobs: manager unavailable")
	}
	if err := m.client.Start(ctx); err != nil {
		return fmt.Errorf("jobs: start River: %w", err)
	}
	return nil
}

func (m *Manager) Stop(ctx context.Context) error {
	if m == nil || m.client == nil {
		return nil
	}
	if err := m.client.Stop(ctx); err != nil {
		return fmt.Errorf("jobs: stop River: %w", err)
	}
	return nil
}

// EnqueueRange inserts one cold-path batch. The processor is idempotent, so a
// duplicate job is safe.
func (m *Manager) EnqueueRange(ctx context.Context, scanID, scanDir string, fromSeq, toSeq uint64) error {
	if m == nil || m.client == nil {
		return errors.New("jobs: manager unavailable")
	}
	args := IndexJobArgs{ScanID: scanID, ScanDir: scanDir, FromSeq: fromSeq, ToSeq: toSeq}
	if err := args.Validate(); err != nil {
		return err
	}
	if _, err := m.client.Insert(ctx, args, &river.InsertOpts{
		MaxAttempts: 10,
		Queue:       DefaultQueue,
	}); err != nil {
		return fmt.Errorf("jobs: enqueue cold-path range: %w", err)
	}
	log.Printf("[coldpath] enqueued %s seq %d..%d", scanID, fromSeq, toSeq)
	return nil
}
