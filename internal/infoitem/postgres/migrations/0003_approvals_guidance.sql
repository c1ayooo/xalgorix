-- 0003_approvals_guidance.sql — R2a HITL approval pipeline
-- (REDESIGN §5 / D5, docs/API_CONTRACT.md §3/§3.1).
--
-- approvals = the pending Operation obligations the engine cannot self-decide:
-- command executions and high-risk HTTP requests. The gate service (web layer)
-- writes a pending row, broadcasts approval_requested, and blocks the tool call
-- until the operator decides, the deadline elapses (timeout = rejection,
-- REDESIGN D5), or the run stops (batch cancelled, §5.3).
--
-- guidance = human-in-the-loop intent turns (source=human InfoItems,
-- charter §3/§8). A turn is injected into a running agent's next conversation
-- round, or queued onto the task and merged into the next session's briefing.
--
-- instance_id is TEXT rather than a FK: it mirrors runs.instance_id but must
-- exist even for dispatches whose run mount failed (the gate still needs
-- per-instance slot accounting and stop-time cancellation). The id/decided_by
-- vocabulary follows API_CONTRACT §3: decided_by ∈ "operator:<user>" |
-- "timeout" | "system".
--
-- The info-model migrator (migrate.go) applies this file inside the same
-- advisory-locked transaction as 0001/0002; multi-statement scripts go
-- through PgConn.Exec (simple protocol).

CREATE TABLE IF NOT EXISTS approvals (
    id          UUID PRIMARY KEY,
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id     UUID        NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id      UUID        NULL REFERENCES runs(id) ON DELETE CASCADE,
    instance_id TEXT        NOT NULL DEFAULT '',
    kind        TEXT        NOT NULL CHECK (kind IN ('command', 'http')),
    payload     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    slice       JSONB       NULL,
    status      TEXT        NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'approved', 'rejected', 'timeout', 'cancelled')),
    deadline_at TIMESTAMPTZ NOT NULL,
    decided_at  TIMESTAMPTZ NULL,
    decided_by  TEXT        NOT NULL DEFAULT '',
    reason      TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS approvals_project_idx
    ON approvals (project_id, created_at DESC);

-- Pending deadline scans (timeout sweep) and the OpsPulse pending_total.
CREATE INDEX IF NOT EXISTS approvals_status_deadline_idx
    ON approvals (status, deadline_at) WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS approvals_run_idx
    ON approvals (run_id);

-- Per-instance slot accounting (max_pending) and stop-time batch cancellation.
CREATE INDEX IF NOT EXISTS approvals_instance_idx
    ON approvals (instance_id) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS guidance (
    id          UUID PRIMARY KEY,
    task_id     UUID        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id      UUID        NULL REFERENCES runs(id) ON DELETE CASCADE,
    instance_id TEXT        NOT NULL DEFAULT '',
    message     TEXT        NOT NULL,
    status      TEXT        NOT NULL DEFAULT 'queued'
                CHECK (status IN ('injected', 'queued', 'consumed')),
    request_id  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    consumed_at TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS guidance_task_idx
    ON guidance (task_id, created_at DESC);

-- Idempotency key (API_CONTRACT §3.1): a client retry carrying the same
-- request_id must never duplicate the human turn. Partial so rows without a
-- request_id stay unconstrained. The store resolves a lost insert race by
-- re-reading (same pattern as uq_tasks_legacy_import).
CREATE UNIQUE INDEX IF NOT EXISTS uq_guidance_request_id
    ON guidance (task_id, request_id)
    WHERE request_id IS NOT NULL AND request_id <> '';
