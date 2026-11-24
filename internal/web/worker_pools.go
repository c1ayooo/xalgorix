package web

// worker_pools.go — hot resizing of the planner/worker pools.
//
// The settings value XALGORIX_WORKER_CONCURRENCY is intentionally live: the
// env file is re-read at scan start and by a small watcher, so an operator can
// change the concurrency without restarting the server. New scans pick the
// value up directly; already-running pools are resized through SetWorkers
// (grow immediately, shrink gracefully after the in-flight intent).

import (
	"log"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/orchestrator"
)

const workerConcurrencyWatchInterval = 5 * time.Second

func (s *Server) registerWorkerPool(instanceID string, pool *orchestrator.Pool) {
	if s == nil || pool == nil || instanceID == "" {
		return
	}
	s.workerPoolsMu.Lock()
	if s.workerPools == nil {
		s.workerPools = make(map[string]*orchestrator.Pool)
	}
	s.workerPools[instanceID] = pool
	s.workerPoolsMu.Unlock()
}

func (s *Server) unregisterWorkerPool(instanceID string, pool *orchestrator.Pool) {
	if s == nil || pool == nil || instanceID == "" {
		return
	}
	s.workerPoolsMu.Lock()
	if current, ok := s.workerPools[instanceID]; ok && current == pool {
		delete(s.workerPools, instanceID)
	}
	s.workerPoolsMu.Unlock()
}

// resizeWorkerPools applies n to every live pool. It never starts/stops scans.
func (s *Server) resizeWorkerPools(n int) {
	if s == nil {
		return
	}
	if n < 1 {
		n = 1
	}
	if n > 10 {
		n = 10
	}
	s.workerPoolsMu.Lock()
	pools := make([]*orchestrator.Pool, 0, len(s.workerPools))
	for _, p := range s.workerPools {
		pools = append(pools, p)
	}
	s.workerPoolsMu.Unlock()
	for _, p := range pools {
		if p.WorkerCount() != n {
			p.SetWorkers(n)
			log.Printf("[orchestrator] hot-resized worker pool to %d", n)
		}
	}
}

// startWorkerConcurrencyWatcher makes an env-file edit hot for RUNNING pools
// too. Settings-page saves resize immediately; this watcher covers direct file
// edits and external configuration managers.
func (s *Server) startWorkerConcurrencyWatcher() {
	if s == nil {
		return
	}
	s.settingsMu.RLock()
	last := s.cfg.WorkerConcurrency
	s.settingsMu.RUnlock()
	go func() {
		ticker := time.NewTicker(workerConcurrencyWatchInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.settingsMu.RLock()
			fallback := s.cfg.WorkerConcurrency
			s.settingsMu.RUnlock()
			n := config.LiveWorkerConcurrency(fallback)
			if n == last {
				continue
			}
			last = n
			s.settingsMu.Lock()
			s.cfg.WorkerConcurrency = n
			s.settingsMu.Unlock()
			s.resizeWorkerPools(n)
		}
	}()
}
