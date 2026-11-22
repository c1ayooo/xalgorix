package web

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

// targetConcurrencyLimit resolves the opt-in per-run target concurrency.
//
// Default 1 preserves the historical serial queue byte-for-byte. Values are
// capped at 4: one instance runs one agent tree per target, so a larger fan-out
// would multiply LLM in-flight demand and host memory without an admission
// decision to back it. This is a scheduling improvement, not a capacity change.
func targetConcurrencyLimit(targets int) int {
	raw := strings.TrimSpace(os.Getenv("XALGORIX_TARGET_CONCURRENCY"))
	if raw == "" {
		return 1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 1
	}
	if n > 4 {
		n = 4
	}
	if targets > 0 && n > targets {
		n = targets
	}
	return n
}

// runTargetsParallel executes the run's targets with bounded concurrency. It is
// reached only when XALGORIX_TARGET_CONCURRENCY > 1 and the mode is a plain
// multi-target queue (wildcard/DAST keep their serial, cooldown-driven loops).
//
// Lifecycle contract kept intact:
//   - instance.cancel is the BATCH cancel func, so Stop All / single stop /
//     pause cancels every in-flight target through the existing path;
//   - the global stopReq is still only consulted as a read (never set here);
//   - the queue is advanced exactly once, after the batch drains, so a resume
//     restarts at the first un-started target rather than mid-batch.
//
// Caveat (documented in API_CONTRACT): in parallel mode the per-target queue
// pointer is written by the batch, not by each target, so an interrupted batch
// resumes from its recorded index instead of the exact target that was running.
func (s *Server) runTargetsParallel(instance *ScanInstance, scanCfg *config.Config, req ScanRequest, limit int) bool {
	instanceID := req.InstanceID
	total := len(req.Targets)

	parentCtx, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()

	instance.mu.Lock()
	instance.cancel = cancelAll
	instance.mu.Unlock()
	s.mu.Lock()
	s.cancelScan = cancelAll
	s.mu.Unlock()

	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var stateMu sync.Mutex
	interrupted := false
	markInterrupted := func() {
		stateMu.Lock()
		interrupted = true
		stateMu.Unlock()
	}
	isInterrupted := func() bool {
		stateMu.Lock()
		defer stateMu.Unlock()
		return interrupted
	}

	for i, target := range req.Targets {
		instance.mu.RLock()
		status := instance.Status
		instance.mu.RUnlock()
		if status == "stopped" || status == "paused" {
			markInterrupted()
			if status == "paused" {
				s.broadcastToInstance(instanceID, WSEvent{Type: "paused", Content: "Scan queue paused"})
			} else if !instanceHasExactStopEvent(instance) {
				s.broadcastToInstance(instanceID, WSEvent{Type: "stopped", Content: "Scan queue stopped by user"})
			}
			break
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(index int, target string) {
			defer wg.Done()
			defer func() { <-sem }()

			// Re-check under the slot: a stop that arrived while this target
			// waited must not start new work.
			instance.mu.RLock()
			status := instance.Status
			instance.mu.RUnlock()
			if status == "stopped" || status == "paused" || parentCtx.Err() != nil {
				markInterrupted()
				return
			}

			s.saveQueueState(index, req)
			switch req.ScanMode {
			case "dast":
				s.runDASTTarget(parentCtx, scanCfg, req, target, index, total)
			default:
				s.runSingleTarget(parentCtx, scanCfg, req, target, index, total)
			}

			instance.mu.RLock()
			after := instance.Status
			instance.mu.RUnlock()
			stopRequested := s.stopReq.Load() || s.instanceInterrupted(instanceID) || parentCtx.Err() != nil
			if !shouldAdvanceQueueAfterTarget(stopRequested, after) {
				markInterrupted()
			}
		}(i, target)
	}

	wg.Wait()

	instance.mu.RLock()
	finalStatus := instance.Status
	instance.mu.RUnlock()
	stopRequested := s.stopReq.Load() || s.instanceInterrupted(instanceID) || parentCtx.Err() != nil
	if !isInterrupted() && shouldAdvanceQueueAfterTarget(stopRequested, finalStatus) {
		s.saveQueueState(total, req)
		return false
	}
	return true
}
