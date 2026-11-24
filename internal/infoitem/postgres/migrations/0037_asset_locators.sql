-- 0037_asset_locators.sql — locator history for stable asset identities.
--
-- Asset identity remains (project_id, kind, asset_key). A locator is a
-- time-bound way to reach that identity: host, host:port, URL or path. The same
-- asset can have many locators over time (container rebuild, IP reuse, port
-- change) and an old locator is never held out as current.
--
-- This table is a projection over observations: source_info_item_id points at
-- the InfoItem that observed the locator. It does not authorize anything; the
-- existing scope guard still decides whether the current locator may be used.
--
-- Rollback: DROP TABLE IF EXISTS asset_locators;

CREATE TABLE IF NOT EXISTS asset_locators (
    id                   BIGSERIAL   PRIMARY KEY,
    project_id           TEXT        NOT NULL DEFAULT '',
    asset_id             BIGINT      NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    locator              TEXT        NOT NULL,
    instance_id          TEXT        NOT NULL DEFAULT '',
    observed_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_from           TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_until          TIMESTAMPTZ,
    source_info_item_id  TEXT        NOT NULL DEFAULT '',
    CONSTRAINT ck_asset_locator_nonempty CHECK (locator <> '')
);

-- The same observation is idempotent: re-ingesting one URL does not append a
-- duplicate locator row at the same instant.
CREATE UNIQUE INDEX IF NOT EXISTS asset_locators_identity_idx
    ON asset_locators (project_id, asset_id, locator, observed_at);

CREATE INDEX IF NOT EXISTS asset_locators_asset_idx
    ON asset_locators (project_id, asset_id, observed_at DESC);

CREATE INDEX IF NOT EXISTS asset_locators_locator_idx
    ON asset_locators (project_id, locator);
