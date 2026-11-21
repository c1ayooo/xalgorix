-- 0005_engine_checkpoints.sql — R4 Eino interrupt/resume checkpoints.
--
-- The payload is the opaque byte blob produced by the Eino runner (it is not
-- necessarily JSON). It is a cold-path continuation record only: scan.json and
-- scanctx remain the state authority, and a checkpoint never re-runs a scan.

CREATE TABLE IF NOT EXISTS engine_checkpoints (
    checkpoint_id TEXT        PRIMARY KEY,
    payload       BYTEA       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS engine_checkpoints_updated_idx
    ON engine_checkpoints (updated_at);
