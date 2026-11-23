package runstate

// workers.go — durable worker lifecycle rows (run_workers, migration 0018).
//
// The delegated-worker state authority while a scan runs is the in-memory
// agentsgraph; this table is its persisted projection at every lifecycle
// boundary (agent_queued / agent_started / agent_completed / agent_failed),
// so the session page can list a run's workers — including finished ones —
// without a live graph or the execution-graph checkpoint.
//
// PG-only by design (charter v3.0 §6.6): there is no file fallback.

import (
	"context"
	"errors"
	"time"
)

// WorkerRecord is one worker's persisted lifecycle row.
type WorkerRecord struct {
	InstanceID string
	ScanID     string
	AgentID    string
	Lane       string
	Task       string
	Target     string
	Status     string // queued | running | completed | failed
	Error      string
	StartedAt  time.Time // zero = unknown
	FinishedAt time.Time // zero = not finished
	UpdatedAt  time.Time
}

// UpsertWorker persists one lifecycle transition. Idempotent on
// (instance_id, agent_id): later transitions overwrite status/error/times,
// while non-empty task/target from the spawn notification are preserved
// against later notifications that may omit them.
func (s *Store) UpsertWorker(ctx context.Context, rec WorkerRecord) error {
	if s == nil || s.pool == nil {
		return errors.New("runstate: store is closed")
	}
	if rec.InstanceID == "" || rec.AgentID == "" {
		return errors.New("runstate: worker upsert requires instance_id and agent_id")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO run_workers
			(instance_id, scan_id, agent_id, lane, task, target, status, error,
			 started_at, finished_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
		ON CONFLICT (instance_id, agent_id) DO UPDATE SET
			scan_id     = EXCLUDED.scan_id,
			lane        = CASE WHEN EXCLUDED.lane <> '' THEN EXCLUDED.lane ELSE run_workers.lane END,
			task        = CASE WHEN EXCLUDED.task <> '' THEN EXCLUDED.task ELSE run_workers.task END,
			target      = CASE WHEN EXCLUDED.target <> '' THEN EXCLUDED.target ELSE run_workers.target END,
			status      = EXCLUDED.status,
			error       = EXCLUDED.error,
			started_at  = COALESCE(run_workers.started_at, EXCLUDED.started_at),
			finished_at = EXCLUDED.finished_at,
			updated_at  = now()`,
		rec.InstanceID, rec.ScanID, rec.AgentID, rec.Lane, rec.Task, rec.Target,
		rec.Status, rec.Error, nilOrTime(rec.StartedAt), nilOrTime(rec.FinishedAt))
	return err
}

// ListWorkersByInstance returns every worker row of one run (all sibling
// sessions), newest activity first.
func (s *Store) ListWorkersByInstance(ctx context.Context, instanceID string) ([]WorkerRecord, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("runstate: store is closed")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT instance_id, scan_id, agent_id, lane, task, target, status, error,
		       started_at, finished_at, updated_at
		FROM run_workers
		WHERE instance_id = $1
		ORDER BY updated_at DESC, agent_id`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkerRecord
	for rows.Next() {
		var rec WorkerRecord
		var started, finished *time.Time
		if err := rows.Scan(&rec.InstanceID, &rec.ScanID, &rec.AgentID, &rec.Lane,
			&rec.Task, &rec.Target, &rec.Status, &rec.Error,
			&started, &finished, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		if started != nil {
			rec.StartedAt = *started
		}
		if finished != nil {
			rec.FinishedAt = *finished
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// nilOrTime maps the zero time to SQL NULL so absent timestamps stay absent
// instead of becoming year 1.
func nilOrTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
