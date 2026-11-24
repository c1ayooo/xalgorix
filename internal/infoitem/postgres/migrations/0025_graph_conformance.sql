-- 0025_graph_conformance.sql — durable audit snapshots of graph conformance.
--
-- INFO_GRAPH_CONTRACT §3.3: projection reconciliation and invariant checks
-- must be visible to operators, not only in process logs. Each row is one
-- read-only snapshot; the checker never repairs or rewrites graph state.
CREATE TABLE IF NOT EXISTS graph_conformance (
    scan_id     TEXT        NOT NULL,
    checked_at  TIMESTAMPTZ NOT NULL,
    ok          BOOLEAN     NOT NULL,
    errors      INT         NOT NULL DEFAULT 0,
    warnings    INT         NOT NULL DEFAULT 0,
    expected    INT         NOT NULL DEFAULT 0,
    observed    INT         NOT NULL DEFAULT 0,
    missing     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    stale       JSONB       NOT NULL DEFAULT '[]'::jsonb,
    violations  JSONB       NOT NULL DEFAULT '[]'::jsonb,
    truncated   BOOLEAN     NOT NULL DEFAULT false,
    PRIMARY KEY (scan_id, checked_at)
);

CREATE INDEX IF NOT EXISTS graph_conformance_scan_idx
    ON graph_conformance (scan_id, checked_at DESC);
