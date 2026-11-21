package coldpath

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xalgord/xalgorix/v4/internal/domainjournal"
	"github.com/xalgord/xalgorix/v4/internal/infoitem"
	"github.com/xalgord/xalgorix/v4/internal/infoitem/postgres"
	"github.com/xalgord/xalgorix/v4/internal/jobs"
)

type observedAppend struct {
	path   string
	scanID string
	seq    uint64
}

// Config controls the optional cold path. Empty DSN means disabled.
type Config struct {
	DSN               string
	DataDir           string
	ReconcileInterval time.Duration
}

// Service owns the PostgreSQL pool, River manager, and reconciler.
type Service struct {
	pool       *pgxpool.Pool
	store      *postgres.Store
	jobs       *jobs.Manager
	reconciler *Reconciler
	interval   time.Duration
	enabled    bool

	mu             sync.Mutex
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	started        bool
	observerCancel func()
	readerCancel   func()
	events         chan observedAppend
}

// New constructs the cold path. With no DSN it returns a disabled service and
// no error, preserving the file-only single-binary mode.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return &Service{}, nil
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errors.New("coldpath: data directory is required when PostgreSQL is configured")
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("coldpath: parse database URL: %w", err)
	}
	if poolConfig.MaxConns < 2 || poolConfig.MaxConns > 8 {
		poolConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("coldpath: create PostgreSQL pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("coldpath: PostgreSQL unavailable: %w", err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	store := postgres.New(pool)
	processor, err := NewProcessor(cfg.DataDir, store)
	if err != nil {
		pool.Close()
		return nil, err
	}
	manager, err := jobs.NewManager(ctx, pool, processor)
	if err != nil {
		pool.Close()
		return nil, err
	}
	reconciler, err := NewReconciler(cfg.DataDir, store, manager)
	if err != nil {
		pool.Close()
		return nil, err
	}
	interval := cfg.ReconcileInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Service{
		pool:       pool,
		store:      store,
		jobs:       manager,
		reconciler: reconciler,
		interval:   interval,
		enabled:    true,
	}, nil
}

func (s *Service) Enabled() bool {
	return s != nil && s.enabled
}

func (s *Service) Store() *postgres.Store {
	if s == nil {
		return nil
	}
	return s.store
}

// Start starts River and the background reconciler. It never blocks startup.
func (s *Service) Start(ctx context.Context) error {
	if s == nil || !s.enabled {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	if err := s.jobs.Start(ctx); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.readerCancel = infoitem.RegisterReader(s.store)
	s.events = make(chan observedAppend, 256)
	s.observerCancel = domainjournal.RegisterObserver(func(path, scanID string, seq uint64) {
		select {
		case s.events <- observedAppend{path: path, scanID: scanID, seq: seq}:
		default:
			// The periodic reconciler is the backstop for dropped fast-path
			// notifications. Never block the hot journal append.
		}
	})
	s.started = true
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.reconciler.Run(runCtx, s.interval)
	}()
	go func() {
		defer s.wg.Done()
		s.runObservedAppends(runCtx)
	}()
	log.Printf("[coldpath] PostgreSQL InfoItem replica enabled; reconcile interval=%s", s.interval)
	return nil
}

func (s *Service) runObservedAppends(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-s.events:
			s.enqueueObserved(ctx, event)
		}
	}
}

func (s *Service) enqueueObserved(ctx context.Context, event observedAppend) {
	if strings.TrimSpace(event.path) == "" || event.seq == 0 {
		return
	}
	relDir, err := filepath.Rel(s.reconciler.root, filepath.Dir(event.path))
	if err != nil || relDir == "." || relDir == ".." || strings.HasPrefix(relDir, ".."+string(filepath.Separator)) {
		log.Printf("[coldpath] ignoring journal outside data root: %s", event.path)
		return
	}
	if err := s.jobs.EnqueueRange(ctx, event.scanID, relDir, event.seq, event.seq); err != nil && ctx.Err() == nil {
		log.Printf("[coldpath] fast-path enqueue warning (reconciler will retry): %v", err)
	}
}

// Stop stops the reconciler, waits for River to drain, and closes the pool.
func (s *Service) Stop(ctx context.Context) error {
	if s == nil || !s.enabled {
		return nil
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		s.pool.Close()
		return nil
	}
	cancel := s.cancel
	observerCancel := s.observerCancel
	readerCancel := s.readerCancel
	s.started = false
	s.mu.Unlock()

	if observerCancel != nil {
		observerCancel()
	}
	if readerCancel != nil {
		readerCancel()
	}
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	var errs []error
	if err := s.jobs.Stop(ctx); err != nil {
		errs = append(errs, err)
	}
	s.pool.Close()
	return errors.Join(errs...)
}
