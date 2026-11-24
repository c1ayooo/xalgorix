-- 0029_run_worker_units.sql — worker rows carry their authoritative intent.
--
-- R7 pool workers own exactly one intent (unit), but run_workers keyed only by
-- agent_id. A retry or re-dispatch of the same intent therefore produced a new
-- agent row and the session page showed it as a second worker, while the work
-- unit (intent) stayed one. unit_id lets the projection keep one visible worker
-- per work unit and lets transcript lanes resolve unit -> agent ids after the
-- live graph is gone.
--
-- Empty for legacy model-initiated spawns (no unit) and for rows written before
-- this migration; those keep agent_id as the identity.

ALTER TABLE run_workers
    ADD COLUMN IF NOT EXISTS unit_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS run_workers_unit_idx
    ON run_workers (instance_id, unit_id)
    WHERE unit_id <> '';
