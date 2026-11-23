package web

// workers_backend.go — process-wide registration of the durable worker
// lifecycle backend (runstate.Store's run_workers table, migration 0018).
//
// The live agentsgraph stays the authority while a scan runs; this backend is
// written at every lifecycle boundary and read by GET /agents so a run's
// workers (including finished ones) are visible without a live graph or the
// execution-graph checkpoint. PG-only by design (charter v3.0 §6.6): nil
// keeps the legacy checkpoint projection for tests and old deployments.

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
)

// WorkerStateBackend persists and reads delegated-worker lifecycle rows.
type WorkerStateBackend interface {
	UpsertWorker(ctx context.Context, rec runstate.WorkerRecord) error
	ListWorkersByInstance(ctx context.Context, instanceID string) ([]runstate.WorkerRecord, error)
}

var (
	workerStateBackendMu sync.RWMutex
	workerStateBackend   WorkerStateBackend
)

// SetWorkerStateBackend registers the process-wide worker lifecycle backend
// (cmd/xalgorix wires the runstate store). Pass nil to keep the legacy
// checkpoint projection (tests).
func SetWorkerStateBackend(b WorkerStateBackend) {
	workerStateBackendMu.Lock()
	defer workerStateBackendMu.Unlock()
	workerStateBackend = b
}

func currentWorkerStateBackend() WorkerStateBackend {
	workerStateBackendMu.RLock()
	defer workerStateBackendMu.RUnlock()
	return workerStateBackend
}

// persistWorkerLifecycle projects one delegated-agent lifecycle transition
// into the durable worker table. It runs off the event path (bounded backend
// context, own goroutine): a slow PG write must never stall the session's
// single processEvent goroutine, and a failed write is logged, not fatal —
// the live agentsgraph stays the authority while the run is in flight.
func (s *Server) persistWorkerLifecycle(evt agent.Event, sess *scanSession) {
	backend := currentWorkerStateBackend()
	if backend == nil || sess == nil || sess.instanceID == "" {
		return
	}
	var status string
	switch evt.Type {
	case "agent_queued":
		status = "queued"
	case "agent_started":
		status = "running"
	case "agent_completed":
		status = "completed"
	case "agent_failed":
		status = "failed"
	default:
		return
	}
	meta := evt.Meta
	if meta == nil {
		return
	}
	agentID, _ := meta["agent_id"].(string)
	if agentID == "" {
		return
	}
	if st, _ := meta["status"].(string); st != "" {
		status = st
	}
	lane, _ := meta["lane"].(string)
	task, _ := meta["task"].(string)
	target, _ := meta["target"].(string)

	now := time.Now().UTC()
	rec := runstate.WorkerRecord{
		InstanceID: sess.instanceID,
		ScanID:     sess.id,
		AgentID:    agentID,
		Lane:       lane,
		Task:       task,
		Target:     target,
		Status:     status,
	}
	switch evt.Type {
	case "agent_started":
		rec.StartedAt = now
	case "agent_completed", "agent_failed":
		rec.FinishedAt = now
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[workers] persist %s/%s panicked: %v", rec.InstanceID, rec.AgentID, r)
			}
		}()
		ctx, cancel := eventStateCtx()
		defer cancel()
		if err := backend.UpsertWorker(ctx, rec); err != nil {
			log.Printf("[workers] persist %s/%s (%s) failed: %v", rec.InstanceID, rec.AgentID, rec.Status, err)
		}
	}()
}
