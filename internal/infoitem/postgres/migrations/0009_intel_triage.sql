-- 0009_intel_triage.sql — operator triage annotations for recorded intelligence
-- (task detail 情报 tab, REDESIGN §7.2 任务详情页).
--
-- Field-authority separation (charter §5): the intel ITEMS themselves are
-- observed engine records and stay immutable in <scanDir>/intelligence.json —
-- the run's audit surface. This table holds only the operator's triage label
-- (a human annotation), keyed by the item fingerprint so the same observation
-- re-recorded by later runs stays one triage decision.
--
-- Observations without a DSN still work: the web layer aggregates items by
-- walking run scan dirs; triage is simply unavailable in file mode until the
-- platform store exists (file mode: _platform/platform.json, same dual
-- implementation as approval_rules 0006).
--
-- The info-model migrator (migrate.go) applies this file inside the same
-- advisory-locked transaction as 0001–0008; single statement, no data backfill.

CREATE TABLE IF NOT EXISTS intel_triage (
    task_id     UUID        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    fingerprint TEXT        NOT NULL,
    status      TEXT        NOT NULL CHECK (status IN ('open', 'confirmed', 'dismissed')),
    decided_by  TEXT        NOT NULL DEFAULT 'operator',
    decided_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, fingerprint)
);
