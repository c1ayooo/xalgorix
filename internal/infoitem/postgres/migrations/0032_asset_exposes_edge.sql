-- 0032_asset_exposes_edge.sql — host → exposed endpoint relation.
--
-- Observed URL ingestion already creates two asset identities from one URL:
-- the host (root_domain/subdomain/ip/service) and the path (endpoint). Without
-- an edge the dashboard can only show them as one flat list, so the asset page
-- cannot drill from a target into its paths. `exposes` is deterministic
-- (derived from the same observation), never model-authored.
--
-- Additive: existing rel values stay valid; no data rewrite.

ALTER TABLE asset_edges
    DROP CONSTRAINT IF EXISTS ck_asset_edge_rel;

ALTER TABLE asset_edges
    ADD CONSTRAINT ck_asset_edge_rel CHECK (rel IN
        ('resolves_to','hosts','serves','bound_to','exposes'));
