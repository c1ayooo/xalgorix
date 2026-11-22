-- 0014_intel_triage.sql — operator triage annotations for recorded intelligence
-- (task detail 情报 tab, REDESIGN §7.2 任务详情页).
--
-- Renumbered from 0009 → 0014: 0009 collided with 0009_approval_rule_decision,
-- which made loadMigrations() fail with "duplicate migration version 9" on a
-- fresh database (platform.Open then aborted startup). Version numbers are
-- append-only, so this file keeps its original statements unchanged; the
-- CREATE TABLE IF NOT EXISTS below makes a replay on a database that already
-- applied it under the old 0009 number a no-op.
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
-- advisory-locked transaction as the other migrations; single statement, no
-- data backfill.

CREATE TABLE IF NOT EXISTS intel_triage (
    task_id     UUID        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    fingerprint TEXT        NOT NULL,
    status      TEXT        NOT NULL CHECK (status IN ('open', 'confirmed', 'dismissed')),
    decided_by  TEXT        NOT NULL DEFAULT 'operator',
    decided_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, fingerprint)
);
