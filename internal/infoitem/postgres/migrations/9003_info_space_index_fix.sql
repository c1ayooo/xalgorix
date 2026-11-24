-- 9003_info_space_index_fix.sql — match the view's exact COALESCE expressions.
--
-- 9001 indexed only the info-block expression, but the IG0 view exposes the
-- COALESCE(info block, legacy target/endpoint, '') as `subject`. A predicate on
-- the view can only use an expression index when the expression matches, so
-- recreate the indexes with the same shape the view inlines.

DROP INDEX IF EXISTS exploration_nodes_info_subject_idx;
DROP INDEX IF EXISTS exploration_nodes_info_purpose_idx;
DROP INDEX IF EXISTS exploration_nodes_legacy_target_idx;

CREATE INDEX IF NOT EXISTS exploration_nodes_info_subject_idx
    ON exploration_nodes (
        scan_id,
        (COALESCE(
            xalgorix_safe_jsonb(payload) -> 'info' -> 'subject' ->> 'identifier',
            xalgorix_safe_jsonb(payload) ->> 'target',
            xalgorix_safe_jsonb(payload) ->> 'endpoint',
            ''
        )),
        (COALESCE(
            xalgorix_safe_jsonb(payload) -> 'info' ->> 'purpose',
            xalgorix_safe_jsonb(payload) ->> 'vuln_class',
            xalgorix_safe_jsonb(payload) ->> 'objective_ref',
            ''
        )),
        created_at DESC
    );

CREATE INDEX IF NOT EXISTS exploration_nodes_info_purpose_idx
    ON exploration_nodes (
        scan_id,
        (COALESCE(
            xalgorix_safe_jsonb(payload) -> 'info' ->> 'purpose',
            xalgorix_safe_jsonb(payload) ->> 'vuln_class',
            xalgorix_safe_jsonb(payload) ->> 'objective_ref',
            ''
        )),
        created_at DESC
    );
