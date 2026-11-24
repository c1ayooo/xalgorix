-- 0036_intel_access_use.sql — M4 of INTEL_HANDOFF_SPEC: the audit projection of
-- authorization decisions and explicit detail fetches.
--
-- This is a projection, not an authority: state lives in intel_access /
-- access_capabilities. A write failure here never blocks a dispatch (the judge
-- logs and continues), while use_count/last_used_at are updated in the consume
-- transaction so the counters cannot diverge from the receipt.

CREATE TABLE IF NOT EXISTS intel_access_use (
    id              BIGSERIAL   PRIMARY KEY,
    scan_id         TEXT        NOT NULL,
    intent_id       BIGINT      NOT NULL DEFAULT 0,
    fingerprint     TEXT        NOT NULL DEFAULT '',
    access_id       BIGINT,
    target          TEXT        NOT NULL DEFAULT '',
    -- allow | deny | needs_capability | detail_fetch
    decision        TEXT        NOT NULL,
    reason          TEXT        NOT NULL DEFAULT '',
    capability_id   BIGINT,
    uses_left_after INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_intel_use_decision CHECK (
        decision IN ('allow','deny','needs_capability','detail_fetch')
    )
);

CREATE INDEX IF NOT EXISTS intel_access_use_scan_idx
    ON intel_access_use (scan_id, created_at DESC);
CREATE INDEX IF NOT EXISTS intel_access_use_fp_idx
    ON intel_access_use (scan_id, fingerprint);
