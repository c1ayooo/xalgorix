-- 0016_run_scan_records.sql — R6.1 runs-table takeover of scan.json:
-- per-scan-record durable rows.
--
-- Migration 0012 put the takeover columns (scan_status … scan_detail) on the
-- runs MOUNT row — one row per dispatch instance, keyed by the unique
-- instance_id. One run can stamp several scan records, though: a multi-target
-- queue dispatches one top-level record per target, a wildcard parent plus its
-- children share the instance, and a D11 re-dispatch leaves ancestor session
-- records behind (resolveScanRecordByIDFrom binds exact ids and newest-session
-- fallbacks against that per-record set). The web layer's full scan set
-- (findAllScanSummaries) therefore needs per-record granularity, which the
-- unique instance_id index forbids on runs itself.
--
-- run_scan_records is that per-record durable home: one row per scan record
-- (scan_id = the record id = the workspace directory basename), with the
-- verbatim record as TEXT (migration 0013's byte-fidelity rule — engine
-- content can carry NUL-byte escapes that JSONB rejects) and the dataDir-
-- relative workspace anchor so the file layer (byte artifacts, journals'
-- legacy fallback) can still reach the directory without a walk.
--
-- Writers: the R6.1 record save point (web saveScanRecordTo via the scan
-- state backend) and the R6.0 directory importer (AttachScanState). The runs
-- mount row keeps carrying the instance's newest TOP-LEVEL record state so
-- direct-SQL consumers of the 0012 columns keep their contract.

CREATE TABLE IF NOT EXISTS run_scan_records (
    scan_id     TEXT        NOT NULL PRIMARY KEY,
    instance_id TEXT        NOT NULL DEFAULT '',
    scan_dir    TEXT        NOT NULL DEFAULT '',
    scan_detail TEXT        NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Run-wide lookups (sibling enumeration, continuity) key on instance_id.
CREATE INDEX IF NOT EXISTS run_scan_records_instance_idx
    ON run_scan_records (instance_id, updated_at DESC);
