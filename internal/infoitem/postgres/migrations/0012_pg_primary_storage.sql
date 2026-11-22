-- 0012_pg_primary_storage.sql — R6.0 foundation (REDESIGN D12, charter v3.0).
--
-- State and panel data become PostgreSQL-primary (user ruling 2026-11-22,
-- route B): the per-session scan directories stop being the durable unit.
-- These tables are the single durable home for run state from R6.1 onward;
-- nothing writes or reads them before that batch lands, so shipping the DDL
-- first keeps migrations stable while consumers are built.
--
-- Identity discipline (charter §6.3 v3.0): the idempotency key stays
-- (scan_id, seq) — per-session seq semantics are unchanged, so the journal
-- code keeps its meaning — and every row carries the instance_id stamped at
-- the write boundary. The D11 fill-once legacy_scan_id linkage retires with
-- scan.json: identity no longer depends on after-the-fact backfill.
--
-- Opaque payload columns are TEXT, not JSONB: engine content (tool
-- output text embedded in events/journal/ledger) can contain NUL-byte
-- escapes that JSONB rejects (SQLSTATE 22P05), and these documents are
-- decoded wholesale — TEXT preserves byte fidelity exactly.
-- What deliberately stays OUT of PG (byte artifacts, file layer):
-- tool-output archives, traffic raw packets, uploads, report deliverables,
-- and the scan working directory itself (sandbox scratch).

-- ── runs takes over scan.json (R6.1 stops writing the file) ──────────────
-- Typed columns for the fields projections query directly; scan_detail
-- carries the long tail (name, instruction, severity filter, phases,
-- branding, stop metadata) verbatim so the reader can decode the record
-- without per-field DDL churn.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scan_status        TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scan_target        TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scan_parent_target TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scan_started_at    TIMESTAMPTZ NULL;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scan_finished_at   TIMESTAMPTZ NULL;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scan_current_phase INT  NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scan_plan_total     INT NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scan_plan_completed INT NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scan_detail TEXT NULL;

CREATE INDEX IF NOT EXISTS runs_scan_status_idx
    ON runs (scan_status, scan_started_at DESC);

-- ── conversation/telemetry events (was events.jsonl) ─────────────────────
-- Read paths: transcript, session events window, security-chain scan, and
-- the D11 resume replay (which reads the whole run's history via
-- instance_id, tail-budgeted).
CREATE TABLE IF NOT EXISTS run_events (
    scan_id     TEXT        NOT NULL,
    instance_id TEXT        NOT NULL DEFAULT '',
    seq         BIGINT      NOT NULL,
    type        TEXT        NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload     TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (scan_id, seq)
);

CREATE INDEX IF NOT EXISTS run_events_instance_idx
    ON run_events (instance_id, occurred_at);

-- ── domain journal (was domain_journal.jsonl) ────────────────────────────
-- Rows decode into domainjournal.Record verbatim; the structured audit
-- projection (info_audit_records) and its watermarks stay unchanged.
CREATE TABLE IF NOT EXISTS run_journal (
    scan_id     TEXT        NOT NULL,
    instance_id TEXT        NOT NULL DEFAULT '',
    seq         BIGINT      NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    record      TEXT        NOT NULL,
    PRIMARY KEY (scan_id, seq)
);

CREATE INDEX IF NOT EXISTS run_journal_instance_idx
    ON run_journal (instance_id, seq);

-- ── ledger state authority (was ledger.json) ─────────────────────────────
-- The in-memory ledger is authoritative during a run; persistence is one
-- snapshot row per scan, rewritten at every mutation boundary (the ledger is
-- bounded: dozens of hypotheses). seq is the store's monotonic mutation
-- counter for watermark comparisons.
CREATE TABLE IF NOT EXISTS run_ledger_snapshots (
    scan_id     TEXT        NOT NULL PRIMARY KEY,
    instance_id TEXT        NOT NULL DEFAULT '',
    seq         BIGINT      NOT NULL DEFAULT 0,
    data        TEXT        NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS run_ledger_instance_idx
    ON run_ledger_snapshots (instance_id);

-- ── misc JSON states (was intelligence/notes/coverage/queue/token files) ─
-- One bounded state document per (scan, kind); kinds are engine-owned
-- constants ("intelligence", "notes", "coverage", "queue", "tokens").
CREATE TABLE IF NOT EXISTS run_state_files (
    scan_id     TEXT        NOT NULL,
    kind        TEXT        NOT NULL,
    instance_id TEXT        NOT NULL DEFAULT '',
    seq         BIGINT      NOT NULL DEFAULT 0,
    data        TEXT        NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scan_id, kind)
);

-- ── dispatch snapshots (was dataDir/.dispatches/<instance>.json) ─────────
-- The re-dispatch tombstone (D11 reactivateTerminalDispatchSnapshot) and the
-- in-process reservation read these; PG becomes their home in R6.1.
CREATE TABLE IF NOT EXISTS dispatch_snapshots (
    instance_id TEXT        NOT NULL PRIMARY KEY,
    data        TEXT        NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── watermarks: the M3 four-state protocol extended to every state table ─
-- kind ∈ {events, journal, ledger, state:<kind>, findings, ...}; reconciler
-- compares source seq against applied_seq and repairs per §6.4.
CREATE TABLE IF NOT EXISTS run_state_watermarks (
    scan_id     TEXT        NOT NULL,
    kind        TEXT        NOT NULL,
    instance_id TEXT        NOT NULL DEFAULT '',
    applied_seq BIGINT      NOT NULL DEFAULT 0,
    status      TEXT        NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','applied','gap','failed')),
    error       TEXT        NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scan_id, kind)
);

CREATE INDEX IF NOT EXISTS run_state_watermarks_status_idx
    ON run_state_watermarks (status, updated_at);
