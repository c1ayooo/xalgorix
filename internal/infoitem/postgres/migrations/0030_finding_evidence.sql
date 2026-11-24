-- 0030_finding_evidence.sql — immutable, sha256-versioned evidence snapshots
-- for reported findings.
--
-- The traffic archive is disposable (its whole scan directory is subject to
-- retention), so a finding that only references session:seq loses its wire
-- packet when the scan dir is pruned. These tables pin a content-addressed
-- copy of every referenced packet under <DataDir>/evidence and keep the
-- metadata authoritative in PostgreSQL.
--
-- Additive only: no existing table is altered.

CREATE TABLE IF NOT EXISTS finding_evidence_snapshots (
    id               TEXT PRIMARY KEY,              -- derived from scan/source/ref/hashes
    scan_id          TEXT NOT NULL,                 -- session directory basename
    session          TEXT NOT NULL DEFAULT '',      -- sibling session basename (may be '')
    seq              BIGINT NOT NULL DEFAULT 0,
    source           TEXT NOT NULL,                 -- traffic | tool_output
    source_ref       TEXT NOT NULL DEFAULT '',      -- session:seq or tool-output id
    captured_at      TIMESTAMPTZ,
    method           TEXT NOT NULL DEFAULT '',
    url              TEXT NOT NULL DEFAULT '',
    status           INTEGER NOT NULL DEFAULT 0,
    req_sha256       TEXT NOT NULL DEFAULT '',
    resp_sha256      TEXT NOT NULL DEFAULT '',
    req_len          BIGINT NOT NULL DEFAULT 0,
    resp_len         BIGINT NOT NULL DEFAULT 0,
    req_head         TEXT NOT NULL DEFAULT '',
    resp_head        TEXT NOT NULL DEFAULT '',
    req_truncated    BOOLEAN NOT NULL DEFAULT FALSE,
    resp_truncated   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS finding_evidence_snapshots_source_idx
    ON finding_evidence_snapshots (scan_id, source, source_ref);
CREATE INDEX IF NOT EXISTS finding_evidence_snapshots_scan_idx
    ON finding_evidence_snapshots (scan_id, created_at DESC);

CREATE TABLE IF NOT EXISTS finding_evidence_bindings (
    scan_id          TEXT NOT NULL,
    finding_id       TEXT NOT NULL,
    snapshot_id      TEXT NOT NULL REFERENCES finding_evidence_snapshots(id) ON DELETE CASCADE,
    role             TEXT NOT NULL DEFAULT 'proof'
                     CHECK (role IN ('baseline', 'proof', 'output')),
    position         INTEGER NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scan_id, finding_id, snapshot_id)
);

CREATE INDEX IF NOT EXISTS finding_evidence_bindings_finding_idx
    ON finding_evidence_bindings (scan_id, finding_id, position);

CREATE TABLE IF NOT EXISTS finding_evidence_versions (
    scan_id          TEXT NOT NULL,
    finding_id       TEXT NOT NULL,
    version          INTEGER NOT NULL DEFAULT 1,
    report_version   INTEGER NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scan_id, finding_id)
);
