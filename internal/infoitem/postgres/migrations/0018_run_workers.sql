-- 0018_run_workers.sql — durable per-run worker lifecycle (2026-11-23).
--
-- The session page's Workers list must survive the live graph: a finished
-- run, a released agent or a process restart previously left only the
-- execution-graph checkpoint to rebuild from. Worker state is run state
-- (charter v3.0 §6: PG is the primary store), so lifecycle transitions are
-- upserted here at the mutation boundary and the panel reads this table.
--
-- Identity: one row per (instance_id, agent_id). instance_id is the run
-- identity (D11 re-dispatch shares it across sibling sessions); scan_id keeps
-- the session that produced the row for provenance.

CREATE TABLE IF NOT EXISTS run_workers (
    instance_id TEXT        NOT NULL,
    scan_id     TEXT        NOT NULL,
    agent_id    TEXT        NOT NULL,
    lane        TEXT        NOT NULL DEFAULT '',
    task        TEXT        NOT NULL DEFAULT '',
    target      TEXT        NOT NULL DEFAULT '',
    status      TEXT        NOT NULL,
    error       TEXT        NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (instance_id, agent_id)
);

CREATE INDEX IF NOT EXISTS run_workers_instance_idx
    ON run_workers (instance_id, updated_at DESC);
