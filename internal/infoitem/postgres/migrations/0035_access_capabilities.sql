-- 0035_access_capabilities.sql — M3 of INTEL_HANDOFF_SPEC: one-shot
-- capabilities for using a result OUTSIDE the project scope.
--
-- A capability never widens the task scope: it authorizes one intent to reach
-- one exact target, for a bounded number of uses and a bounded lifetime, and
-- it is consumed at dispatch only (SPEC D9). DenyTargets never receive one.
-- The row is issued by the operator (decision inbox) or a rule engine; tools
-- and workers have no write path.

CREATE TABLE IF NOT EXISTS access_capabilities (
    id         BIGSERIAL   PRIMARY KEY,
    scan_id    TEXT        NOT NULL,
    intent_id  BIGINT      NOT NULL,
    access_id  BIGINT      NOT NULL REFERENCES intel_access(id) ON DELETE CASCADE,
    target     TEXT        NOT NULL,
    -- One approval may cover several targets: they share a batch id.
    batch_id   TEXT        NOT NULL DEFAULT '',
    uses_left  INT         NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    issued_by  TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_cap_uses     CHECK (uses_left > 0),
    CONSTRAINT ck_cap_lifetime CHECK (expires_at > created_at),
    CONSTRAINT ck_cap_issued   CHECK (issued_by <> ''),
    -- One live capability per (intent, access, target); re-issuing updates the
    -- existing row instead of stacking authority.
    CONSTRAINT cap_identity UNIQUE (scan_id, intent_id, access_id, target)
);

CREATE INDEX IF NOT EXISTS cap_lookup_idx ON access_capabilities (scan_id, intent_id, target);
CREATE INDEX IF NOT EXISTS cap_expiry_idx ON access_capabilities (scan_id, expires_at);
