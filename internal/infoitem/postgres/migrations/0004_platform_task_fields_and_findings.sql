-- 0004_platform_task_fields_and_findings.sql — R1 platform parity fixes.
--
-- 1. tasks gains the two Goal+Context fields the API already exposes but the
--    PostgreSQL store previously dropped (REDESIGN §3.1, API_CONTRACT §2).
-- 2. run_findings is a cold-path projection of already-accepted scan.json
--    findings for one mounted Run. It never participates in acceptance,
--    verification or report-gate ordering; it is a readable replica used by
--    task-level findings views when a DSN is configured.

ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS instruction TEXT NOT NULL DEFAULT '';

ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS context_paths JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE IF NOT EXISTS run_findings (
    run_id          UUID        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    task_id         UUID        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    project_id      UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    dedup_key       TEXT        NOT NULL,
    scan_started_at TIMESTAMPTZ NOT NULL,
    finding         JSONB       NOT NULL,
    PRIMARY KEY (run_id, dedup_key)
);

-- Task-level dedup reads the newest row per key. run_id is the deterministic
-- tie-break when two runs started at the same instant.
CREATE INDEX IF NOT EXISTS run_findings_task_latest_idx
    ON run_findings (task_id, dedup_key, scan_started_at DESC, run_id);

CREATE INDEX IF NOT EXISTS run_findings_project_idx
    ON run_findings (project_id, task_id);
