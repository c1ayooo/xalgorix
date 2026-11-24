-- 0034_intel_access.sql — M2 of INTEL_HANDOFF_SPEC: the reachability
-- projection of recorded intelligence.
--
-- intel_access answers one question per intel item: which doors can this
-- result open? It is derived (never authored): ConcludeIntent projects the
-- scan's intelligence snapshot here, idempotently keyed (scan_id,
-- fingerprint). No plaintext: the content stays in the intelligence snapshot.
--
-- Identity is scan-scoped (SPEC D8): reachability authorizes THIS scan's
-- dispatch; the fingerprint stays the task-level identity used by triage.

CREATE TABLE IF NOT EXISTS intel_access (
    id               BIGSERIAL   PRIMARY KEY,
    -- Informational export column; not part of the identity (D8).
    project_id       TEXT        NOT NULL DEFAULT '',
    scan_id          TEXT        NOT NULL,
    fingerprint      TEXT        NOT NULL,
    kind             TEXT        NOT NULL,
    -- Account/asset the result is about (host derived from the endpoint).
    subject          TEXT        NOT NULL DEFAULT '',
    state            TEXT        NOT NULL DEFAULT 'proposed',
    source_intent_id BIGINT,
    confirmed_by     TEXT        NOT NULL DEFAULT '',
    confirmed_at     TIMESTAMPTZ,
    expires_at       TIMESTAMPTZ,
    last_used_at     TIMESTAMPTZ,
    use_count        INT         NOT NULL DEFAULT 0,
    fail_count       INT         NOT NULL DEFAULT 0,
    revoke_reason    TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_intel_access_kind  CHECK (kind IN ('credential','session','path')),
    CONSTRAINT ck_intel_access_state CHECK (state IN ('proposed','confirmed','active','expired','revoked')),
    -- Confirmed/active require a confirmer and time. proposed needs nothing;
    -- expired may have expired before anyone confirmed; revoked is explained
    -- by revoke_reason (failure policy / operator), never by a fake confirmer.
    CONSTRAINT ck_intel_access_confirmed CHECK (
        state IN ('proposed','expired')
        OR (confirmed_by <> '' AND confirmed_at IS NOT NULL)
        OR (state = 'revoked' AND revoke_reason <> '')
    ),
    CONSTRAINT ck_intel_access_revoke CHECK (
        state <> 'revoked' OR revoke_reason <> ''
    ),
    CONSTRAINT intel_access_identity UNIQUE (scan_id, fingerprint)
);

CREATE TABLE IF NOT EXISTS intel_access_targets (
    access_id  BIGINT NOT NULL REFERENCES intel_access(id) ON DELETE CASCADE,
    -- Canonical host:port or CIDR (intelaccess.NormalizeTarget is the only
    -- writer-side normalizer).
    target     TEXT   NOT NULL,
    via        TEXT   NOT NULL DEFAULT '',
    PRIMARY KEY (access_id, target),
    CONSTRAINT ck_intel_access_target_shape CHECK (
        target LIKE '%:%' OR target LIKE '%/%'
    )
);

CREATE INDEX IF NOT EXISTS intel_access_scan_idx ON intel_access (scan_id, state);
CREATE INDEX IF NOT EXISTS intel_access_fp_idx ON intel_access (fingerprint);
