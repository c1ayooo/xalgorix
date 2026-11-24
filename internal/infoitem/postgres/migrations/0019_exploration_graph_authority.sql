-- 0019_exploration_graph_authority.sql — dual-graph authority (R7/M1).
--
-- The exploration graph is the authoritative model for intent / belief /
-- fact / finding / goal / hint / digest nodes and their lineage edges.
-- PlanTask, PlanView, /plan and /units are read views over intent nodes
-- (0021 creates those views). The ledger keeps evidence detail; hypothesis
-- lifecycle / owner / lease live on belief nodes.
--
-- Identity discipline: graph rows are keyed by the RUN identity (D11
-- instance_id) in the scan_id column, so a re-dispatch/sibling session shares
-- the same plan. id is a BIGSERIAL shared across runs; the composite unique
-- key lets edges join within one run without a cross-run FK. payload stays
-- TEXT (migration 0013 byte fidelity rule); graph payloads are small JSON
-- documents, never tool output.
--
-- Real-time authority: claim/settle are CAS UPDATEs against this table.
-- There is no in-memory shadow in production; a process-local store is only
-- used by tests and the legacy/offline fallback.

CREATE TABLE IF NOT EXISTS exploration_nodes (
    id              BIGSERIAL   NOT NULL,
    scan_id         TEXT        NOT NULL,
    instance_id     TEXT        NOT NULL DEFAULT '',
    kind            TEXT        NOT NULL,
    state           TEXT        NOT NULL DEFAULT 'open',
    priority        INT         NOT NULL DEFAULT 0,
    origin          TEXT        NOT NULL DEFAULT '',
    owner           TEXT        NOT NULL DEFAULT '',
    lease_until     TIMESTAMPTZ NULL,
    blocked_reason  TEXT        NOT NULL DEFAULT '',
    delete_reason   TEXT        NOT NULL DEFAULT '',
    payload         TEXT        NOT NULL DEFAULT '{}',
    content_version BIGINT      NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ NULL,
    PRIMARY KEY (id),
    CONSTRAINT exploration_nodes_scan_id_id_key UNIQUE (scan_id, id),
    CONSTRAINT ck_exploration_node_kind CHECK (kind IN
        ('goal','intent','belief','fact','finding','hint','digest')),
    CONSTRAINT ck_exploration_node_state CHECK (
        (kind = 'intent' AND state IN
            ('open','running','paused','done','blocked','exhausted','stopped','deleted')) OR
        (kind = 'belief' AND state IN
            ('queued','testing','proven','rejected','blocked','exhausted')) OR
        (kind = 'fact' AND state IN ('confirmed','dismissed','origin')) OR
        (kind = 'finding' AND state IN ('confirmed','dismissed')) OR
        (kind = 'goal' AND state IN ('open','met','abandoned')) OR
        (kind = 'hint' AND state IN ('active','consumed')) OR
        (kind = 'digest' AND state IN ('active','superseded'))
    )
);

-- Frontier: open intents ordered by priority (higher first), then FIFO id.
-- The composite index leaves room for a dependency join in the claim query.
CREATE INDEX IF NOT EXISTS exploration_nodes_frontier_idx
    ON exploration_nodes (scan_id, kind, state, priority DESC, id)
    WHERE kind = 'intent' AND state = 'open';

CREATE INDEX IF NOT EXISTS exploration_nodes_scan_kind_idx
    ON exploration_nodes (scan_id, kind, state);

CREATE INDEX IF NOT EXISTS exploration_nodes_owner_idx
    ON exploration_nodes (scan_id, owner)
    WHERE owner <> '';

CREATE INDEX IF NOT EXISTS exploration_nodes_instance_idx
    ON exploration_nodes (instance_id);

CREATE TABLE IF NOT EXISTS exploration_edges (
    scan_id    TEXT        NOT NULL,
    src_id     BIGINT      NOT NULL,
    rel        TEXT        NOT NULL,
    dst_id     BIGINT      NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scan_id, src_id, rel, dst_id),
    CONSTRAINT ck_exploration_edge_rel CHECK (rel IN
        ('spawns','derived_from','yields','proves','supports','covers')),
    CONSTRAINT exploration_edges_src_fk FOREIGN KEY (scan_id, src_id)
        REFERENCES exploration_nodes (scan_id, id) ON DELETE CASCADE,
    CONSTRAINT exploration_edges_dst_fk FOREIGN KEY (scan_id, dst_id)
        REFERENCES exploration_nodes (scan_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS exploration_edges_dst_idx
    ON exploration_edges (scan_id, dst_id, rel);

CREATE INDEX IF NOT EXISTS exploration_edges_src_idx
    ON exploration_edges (scan_id, src_id, rel);

-- content_version is the projection/change detector (digest and panel
-- invalidation read it). updated_at always advances; content_version only
-- advances when state or payload actually changed.
CREATE OR REPLACE FUNCTION exploration_nodes_touch() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    IF NEW.state IS DISTINCT FROM OLD.state
       OR NEW.payload IS DISTINCT FROM OLD.payload THEN
        NEW.content_version = OLD.content_version + 1;
    END IF;
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS exploration_nodes_touch_trg ON exploration_nodes;
CREATE TRIGGER exploration_nodes_touch_trg
    BEFORE UPDATE ON exploration_nodes
    FOR EACH ROW EXECUTE FUNCTION exploration_nodes_touch();
