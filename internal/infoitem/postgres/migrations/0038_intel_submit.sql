-- 0038_intel_submit.sql — submission receipts for recorded results.
--
-- Why this table exists: intelligence.Item carries a task-level identity
-- (fingerprint = sha256(kind+NUL+content+NUL+endpoint)), so the same result
-- recorded by a later run aggregates under one row and survives the per-scan
-- partition that wipes notes between sessions. What it could NOT express is
-- "has this result actually been turned into points yet".
--
-- Evidence from the round-7 benchmark: workers proved four challenges and
-- wrote the flags into notes, the control-plane lane never picked them up, and
-- the run finished with 1000 of 23000 while 19 findings — several of them
-- verified critical — sat unsubmitted. A later session could not even tell
-- which of them were already spent.
--
-- Identity is task-level and keyed by the platform's own unique_code, NOT by
-- scan_id: a challenge is submitted (or not) once per task, regardless of how
-- many sessions the engine burned. This is deliberately the opposite trade-off
-- from intel_access (SPEC D8, scan-scoped) — reachability authorizes THIS
-- scan's dispatch, scoring is a fact about the task.
--
-- No plaintext flags live here. unique_code + state + the platform's own
-- response excerpt are enough to reconcile; the flag value stays in the
-- intelligence snapshot and the evidence archive.

CREATE TABLE IF NOT EXISTS intel_submit (
    id               BIGSERIAL   PRIMARY KEY,
    project_id       TEXT        NOT NULL DEFAULT '',
    task_id          TEXT        NOT NULL,
    unique_code      TEXT        NOT NULL,
    -- Optional link to intelligence.Item.Fingerprint; empty when the receipt
    -- was derived from a platform response that no recorded item matched.
    fingerprint      TEXT        NOT NULL DEFAULT '',
    -- Container entry the result came from (host:port as start returned it).
    endpoint         TEXT        NOT NULL DEFAULT '',
    state            TEXT        NOT NULL DEFAULT 'pending',
    awarded          INT         NOT NULL DEFAULT 0,
    attempts         INT         NOT NULL DEFAULT 0,
    last_response    TEXT        NOT NULL DEFAULT '',
    last_attempt_at  TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_intel_submit_state CHECK (
        state IN ('pending','submitted','invalid','duplicate')
    ),
    CONSTRAINT ck_intel_submit_code CHECK (unique_code <> ''),
    -- A submitted receipt must carry awarded >= 0; pending must not claim a
    -- score. This keeps "we think we scored" out of the table: awarded only
    -- ever comes from the platform response.
    CONSTRAINT ck_intel_submit_awarded CHECK (
        state <> 'pending' OR awarded = 0
    ),
    CONSTRAINT intel_submit_identity UNIQUE (task_id, unique_code)
);

CREATE INDEX IF NOT EXISTS intel_submit_pending_idx
    ON intel_submit (task_id, state, updated_at DESC);
CREATE INDEX IF NOT EXISTS intel_submit_fp_idx
    ON intel_submit (fingerprint) WHERE fingerprint <> '';
