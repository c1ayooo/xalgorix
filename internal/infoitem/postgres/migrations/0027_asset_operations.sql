-- 0027_asset_operations.sql — asset operations (archive / merge tombstones).
--
-- Asset identity stays (project, kind, asset_key). Operators can archive an
-- asset (hide it from the default list, keep provenance) and merge a duplicate
-- into a canonical target: links/edges move to the target, while the source row
-- remains as a tombstone (archived_at + merged_into_id) so history is not
-- silently destroyed.
ALTER TABLE assets ADD COLUMN IF NOT EXISTS archived_at   TIMESTAMPTZ NULL;
ALTER TABLE assets ADD COLUMN IF NOT EXISTS archived_by   TEXT        NOT NULL DEFAULT '';
ALTER TABLE assets ADD COLUMN IF NOT EXISTS merged_into_id BIGINT     NULL
    REFERENCES assets(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS assets_project_active_idx
    ON assets (project_id, kind, asset_key)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS assets_merged_into_idx
    ON assets (merged_into_id)
    WHERE merged_into_id IS NOT NULL;
