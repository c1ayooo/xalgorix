-- 0020_assets_authority.sql — asset graph authority (R7/M1).
--
-- Asset identity is authoritative here: one row per normalized
-- (project, kind, asset_key). InfoItem remains the observation/provenance
-- authority (observed vs inferred); assets.first_info_item_id /
-- last_info_item_id point at those observations, they never copy content.
--
-- Assets are project-scoped and reused across runs; task_asset_links records
-- how a run/task came to know an asset (system capture / agent / derived),
-- including the exploration node it was first anchored to.

CREATE TABLE IF NOT EXISTS assets (
    id                  BIGSERIAL   PRIMARY KEY,
    project_id          TEXT        NOT NULL DEFAULT '',
    kind                TEXT        NOT NULL,
    asset_key           TEXT        NOT NULL,
    display_name        TEXT        NOT NULL DEFAULT '',
    first_info_item_id  TEXT        NOT NULL DEFAULT '',
    last_info_item_id   TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_asset_kind CHECK (kind IN
        ('root_domain','subdomain','ip','service','endpoint','app')),
    CONSTRAINT assets_identity_key UNIQUE (project_id, kind, asset_key)
);

CREATE INDEX IF NOT EXISTS assets_project_kind_idx
    ON assets (project_id, kind);

CREATE INDEX IF NOT EXISTS assets_last_info_item_idx
    ON assets (last_info_item_id)
    WHERE last_info_item_id <> '';

-- Deterministic structural relations only (no model-authored edges):
-- resolves_to (domain -> ip), hosts (ip -> service), serves (service ->
-- endpoint), bound_to (domain -> ip binding).
CREATE TABLE IF NOT EXISTS asset_edges (
    project_id TEXT        NOT NULL DEFAULT '',
    src_id     BIGINT      NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    rel        TEXT        NOT NULL,
    dst_id     BIGINT      NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, src_id, rel, dst_id),
    CONSTRAINT ck_asset_edge_rel CHECK (rel IN
        ('resolves_to','hosts','serves','bound_to'))
);

CREATE INDEX IF NOT EXISTS asset_edges_dst_idx
    ON asset_edges (project_id, dst_id, rel);

CREATE TABLE IF NOT EXISTS task_asset_links (
    id             BIGSERIAL   PRIMARY KEY,
    scan_id        TEXT        NOT NULL,
    task_id        TEXT        NOT NULL DEFAULT '',
    asset_id       BIGINT      NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    source         TEXT        NOT NULL DEFAULT '',
    source_node_id BIGINT      NULL REFERENCES exploration_nodes(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One link per (run, asset, source, source node). COALESCE keeps the identity
-- stable for NULL source_node_id (plain system capture).
CREATE UNIQUE INDEX IF NOT EXISTS task_asset_links_identity_idx
    ON task_asset_links (scan_id, asset_id, source, COALESCE(source_node_id, 0));

CREATE INDEX IF NOT EXISTS task_asset_links_asset_idx
    ON task_asset_links (asset_id);

CREATE INDEX IF NOT EXISTS task_asset_links_scan_idx
    ON task_asset_links (scan_id);
