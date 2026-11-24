-- 9004_info_items_instance.sql — indexable run identity on the atom layer.
--
-- 9000 normalized atom run_id through a run_journal lateral join. That is
-- correct but not sargable: a run-scoped query could not use an index on the
-- view's COALESCE expression. Stamp instance_id on the atom row (write path
-- resolves it from run_journal), backfill existing rows, index the exact view
-- expression, and drop the join from the view.
--
-- No write authority changes: instance_id is provenance metadata, not state.

ALTER TABLE info_items
    ADD COLUMN IF NOT EXISTS instance_id TEXT NOT NULL DEFAULT '';

UPDATE info_items i
SET instance_id = j.instance_id
FROM run_journal j
WHERE i.instance_id = ''
  AND j.scan_id = i.scan_id
  AND j.seq = i.source_seq
  AND j.instance_id <> '';

CREATE INDEX IF NOT EXISTS info_items_run_observed_idx
    ON info_items ((COALESCE(NULLIF(instance_id, ''), scan_id)), observed_at DESC, id);

CREATE OR REPLACE VIEW info_space_items AS
WITH aggregate AS (
    SELECT
        n.scan_id,
        n.scan_id                                                               AS run_id,
        n.id::text                                                              AS info_id,
        'aggregate'::text                                                       AS layer,
        n.kind::text                                                            AS kind,
        n.state::text                                                           AS state,
        n.unit_key::text                                                        AS unit_key,
        n.priority,
        n.origin::text                                                          AS origin,
        COALESCE(n.info->'info'->'subject'->>'identifier', n.info->>'target', n.info->>'endpoint', '') AS subject,
        COALESCE(n.info->'info'->'subject'->>'kind', n.info->>'target_kind', '')                       AS subject_kind,
        COALESCE(n.info->'info'->>'purpose', n.info->>'vuln_class', n.info->>'objective_ref', '')      AS purpose,
        COALESCE(n.info->'info'->>'nature', '')                                                        AS nature,
        COALESCE(xalgorix_safe_timestamptz(n.info->'info'->>'observed_at'), n.created_at)              AS observed_at,
        COALESCE(n.info->'info'->'context', '{}'::jsonb)                                               AS context,
        n.info                                                                                          AS payload,
        n.created_at
    FROM (
        SELECT e.*, xalgorix_safe_jsonb(e.payload) AS info
        FROM exploration_nodes e
    ) n
),
atom AS (
    SELECT
        i.scan_id,
        COALESCE(NULLIF(i.instance_id, ''), i.scan_id)                          AS run_id,
        i.id                                                                    AS info_id,
        'atom'::text                                                            AS layer,
        'info_item'::text                                                       AS kind,
        ''::text                                                                AS state,
        ''::text                                                                AS unit_key,
        0                                                                       AS priority,
        COALESCE(i.source->>'kind', '')                                         AS origin,
        COALESCE(i.subject->>'identifier', '')                                  AS subject,
        COALESCE(i.subject->>'kind', '')                                        AS subject_kind,
        i.purpose                                                               AS purpose,
        i.nature                                                                AS nature,
        i.observed_at                                                           AS observed_at,
        COALESCE(i.context, '{}'::jsonb)                                        AS context,
        jsonb_build_object(
            'content',    i.content,
            'coordinates', i.coordinates,
            'source',     i.source,
            'nature',     i.nature,
            'confidence', i.confidence,
            'supports',   i.supports
        )                                                                       AS payload,
        i.created_at
    FROM info_items i
)
SELECT * FROM aggregate
UNION ALL
SELECT * FROM atom;
