-- 0008_integrations.sql — operator-authored integration definitions
-- (MCP servers / user skills / agent profiles).
--
-- These three tables hold engine-input CONFIGURATION, not judgements
-- (charter §3.5): an MCP server row never vouches for its tools' output, a
-- user skill is prompt input (a Hint), and an agent profile bounds
-- delegation without bypassing the verifier or the finish gate. A missing
-- row means "not configured" — identical to the no-DSN file store posture
-- (charter §6.6: every core capability survives with zero rows here).
--
-- Credential material (mcp_servers.env / .headers) is stored as the caller
-- submitted it; the web layer masks on every read and treats "****"-prefixed
-- submissions as "unchanged". The tables themselves are not read by any
-- code path that echoes credentials back.
--
-- Built-in specialist profiles are NOT stored here — they stay code-defined
-- (internal/agent ledger hooks) and are merged at read time by the web
-- layer; agent_profiles only holds operator-authored profiles.
--
-- The info-model migrator (migrate.go) applies this file inside the same
-- advisory-locked transaction as 0001–0007; multi-statement scripts go
-- through PgConn.Exec (simple protocol).

CREATE TABLE IF NOT EXISTS mcp_servers (
    id               UUID PRIMARY KEY,
    name             TEXT        NOT NULL UNIQUE,
    transport        TEXT        NOT NULL CHECK (transport IN ('stdio', 'http')),
    command          TEXT        NOT NULL DEFAULT '',
    args             JSONB       NOT NULL DEFAULT '[]'::jsonb,
    env              JSONB       NOT NULL DEFAULT '{}'::jsonb,
    url              TEXT        NOT NULL DEFAULT '',
    headers          JSONB       NOT NULL DEFAULT '{}'::jsonb,
    enabled          BOOLEAN     NOT NULL DEFAULT true,
    require_approval BOOLEAN     NOT NULL DEFAULT true,
    timeout_seconds  INT         NOT NULL DEFAULT 120,
    tool_count       INT         NOT NULL DEFAULT 0,
    last_healthy_at  TIMESTAMPTZ NULL,
    last_error       TEXT        NOT NULL DEFAULT '',
    created_by       TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NULL
);

CREATE TABLE IF NOT EXISTS user_skills (
    id          UUID PRIMARY KEY,
    name        TEXT        NOT NULL UNIQUE,
    category    TEXT        NOT NULL DEFAULT 'user',
    description TEXT        NOT NULL DEFAULT '',
    tags        JSONB       NOT NULL DEFAULT '[]'::jsonb,
    intent      TEXT        NOT NULL DEFAULT '',
    body        TEXT        NOT NULL,
    enabled     BOOLEAN     NOT NULL DEFAULT true,
    created_by  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NULL
);

CREATE TABLE IF NOT EXISTS agent_profiles (
    id                UUID PRIMARY KEY,
    role              TEXT        NOT NULL UNIQUE,
    focus             TEXT        NOT NULL,
    vuln_classes      JSONB       NOT NULL DEFAULT '[]'::jsonb,
    evidence_contract TEXT        NOT NULL,
    stopping_rule     TEXT        NOT NULL,
    tool_scope_hidden JSONB       NOT NULL DEFAULT '[]'::jsonb,
    enabled           BOOLEAN     NOT NULL DEFAULT true,
    created_by        TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NULL
);
