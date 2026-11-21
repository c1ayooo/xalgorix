-- 0002_projects_tasks_runs.sql — R1 platform model (REDESIGN §3.2, D6/D7).
--
-- Only the projects/tasks/runs subset ships in this milestone; approvals and
-- project credentials arrive with R2/HITL. Run status, timing, vuln counts
-- and tokens are deliberately NOT stored here: scanctx + scan.json remain the
-- status authority (charter §5.1) and the web layer projects them from
-- findAllScanSummaries onto these mount records at read time.
--
-- The info-model migrator (migrate.go) applies this file inside the same
-- advisory-locked transaction as 0001; multi-statement scripts go through
-- PgConn.Exec (simple protocol).

CREATE TABLE IF NOT EXISTS projects (
    id          UUID PRIMARY KEY,
    name        TEXT        NOT NULL,
    slug        TEXT        NOT NULL UNIQUE,
    scope       JSONB       NOT NULL DEFAULT '{"targets":[]}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at TIMESTAMPTZ NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id           UUID PRIMARY KEY,
    project_id   UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    goal         TEXT        NOT NULL DEFAULT '',
    -- Free-form context jsonb (REDESIGN §3.2): legacy import hints
    -- (legacy_schedule_id, legacy run day/branding/webhook/model) plus the
    -- per-project derived slug. Never serialized to the API wire.
    context      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    phase_policy JSONB       NULL,
    schedule     JSONB       NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NULL,
    archived_at  TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS tasks_project_idx
    ON tasks (project_id, created_at);

-- Legacy-import idempotency keys, scoped per project (REDESIGN §8): two
-- processes racing their first startup can both run check-then-insert for
-- the umbrella "导入扫描" task and for schedule→task conversions; these
-- partial unique expression indexes make the second insert a no-op and the
-- store falls back to a re-read (same pattern as runs.legacy_scan_id).
CREATE UNIQUE INDEX IF NOT EXISTS uq_tasks_legacy_import
    ON tasks (project_id, ((context->>'legacy_import')))
    WHERE context ? 'legacy_import';

CREATE UNIQUE INDEX IF NOT EXISTS uq_tasks_legacy_schedule
    ON tasks (project_id, ((context->>'legacy_schedule_id')))
    WHERE context ? 'legacy_schedule_id';

CREATE TABLE IF NOT EXISTS runs (
    id             UUID PRIMARY KEY,
    task_id        UUID        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    legacy_scan_id TEXT        NULL,
    instance_id    TEXT        NULL,
    mode           TEXT        NOT NULL DEFAULT '',
    trigger        TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (legacy_scan_id IS NOT NULL OR instance_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS runs_task_idx
    ON runs (task_id, created_at DESC);

-- One mount per dispatch instance: a duplicate CreateRun is a conflict, and
-- the legacy importer's legacy_scan_id key makes re-imports idempotent.
CREATE UNIQUE INDEX IF NOT EXISTS uq_runs_instance_id
    ON runs (instance_id) WHERE instance_id IS NOT NULL AND instance_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_runs_legacy_scan_id
    ON runs (legacy_scan_id) WHERE legacy_scan_id IS NOT NULL AND legacy_scan_id <> '';
