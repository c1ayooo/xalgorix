-- 0032_info_space_indexes.sql — IG1b query-plan support.
--
-- The unified views read exploration_nodes payload coordinates and info_items
-- time order. Expression indexes may use the IMMUTABLE safe decoders added in
-- 0031; a malformed legacy payload still cannot break the index build.

CREATE INDEX IF NOT EXISTS exploration_nodes_info_subject_idx
    ON exploration_nodes (
        scan_id,
        (xalgorix_safe_jsonb(payload) -> 'info' -> 'subject' ->> 'identifier'),
        (xalgorix_safe_jsonb(payload) -> 'info' ->> 'purpose'),
        created_at DESC
    );

CREATE INDEX IF NOT EXISTS exploration_nodes_info_purpose_idx
    ON exploration_nodes (
        scan_id,
        (xalgorix_safe_jsonb(payload) -> 'info' ->> 'purpose'),
        created_at DESC
    );

-- IG0 falls back to these fields until every producer writes the info block.
CREATE INDEX IF NOT EXISTS exploration_nodes_legacy_target_idx
    ON exploration_nodes (
        scan_id,
        (xalgorix_safe_jsonb(payload) ->> 'target'),
        (xalgorix_safe_jsonb(payload) ->> 'vuln_class'),
        created_at DESC
    );

-- Region/context ordering is observed_at DESC, info_id ASC.
CREATE INDEX IF NOT EXISTS info_items_scan_observed_idx
    ON info_items (scan_id, observed_at DESC, id);
