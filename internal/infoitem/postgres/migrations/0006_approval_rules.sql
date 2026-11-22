-- 0006_approval_rules.sql — operator-authored approval exemptions
-- ("approve & remember", REDESIGN §5.2 R2a addendum, docs/API_CONTRACT.md §3.2).
--
-- approval_rules = the persistent half of the operator decision loop: a rule
-- remembered from an approval decision or hand-authored in the rules dialog.
-- Both gate layers (command gate via einoApprovalBroker, wire gate via
-- proxyHighRiskGate) consult the same table before creating an approval row,
-- so the same intent is asked at most once. Matching semantics live in
-- internal/approval (Policy.MatchCommandRule / MatchHTTPRule) and are reused
-- verbatim by the web layer; deny shapes (command substitution, redirection,
-- env shadowing) are never exemptable by any rule.
--
-- created_from_approval_id REFERENCES approvals ON DELETE SET NULL: the
-- approval row itself may be retention-deleted while the remembered rule
-- survives — traceability degrades honestly to the rule's own created_by.
--
-- Expiry is match-time, not swept: expired rules stop matching but stay
-- listed (hit_count/last_hit_at are the runtime evidence for tuning the
-- approval defaults — timeout/max_pending — against real operator load).
--
-- The info-model migrator (migrate.go) applies this file inside the same
-- advisory-locked transaction as 0001–0005; multi-statement scripts go
-- through PgConn.Exec (simple protocol).

CREATE TABLE IF NOT EXISTS approval_rules (
    id                       UUID PRIMARY KEY,
    project_id               UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind                     TEXT        NOT NULL CHECK (kind IN ('command', 'http')),
    match                    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    reason                   TEXT        NOT NULL DEFAULT '',
    created_by               TEXT        NOT NULL DEFAULT '',
    created_from_approval_id UUID        NULL REFERENCES approvals(id) ON DELETE SET NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at               TIMESTAMPTZ NULL,
    hit_count                INT         NOT NULL DEFAULT 0,
    last_hit_at              TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS approval_rules_project_idx
    ON approval_rules (project_id, created_at DESC);
