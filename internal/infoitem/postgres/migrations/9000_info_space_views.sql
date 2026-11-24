-- 0031_info_space_views.sql — IG0 unified information-space read views.
--
-- The exploration graph is the aggregate information layer; info_items is the
-- atomic observation layer. These views project both onto one column set so the
-- read-only `infospace` queries can light up "the graph" without duplicating
-- rows, copying bodies, or changing the write path.
--
-- `run_id` is the canonical join key:
--   * exploration_nodes.scan_id is the RUN identity (D11 instance id);
--   * info_items.scan_id is the owning ScanContext id (one run may span
--     sibling contexts after a re-dispatch);
--   * run_journal maps an atom back to its run, with a native scan_id fallback.
--
-- IG0 is read-only: no scheduler, claim, ConcludeIntent, ledger or report
-- semantics change. `info_space_items` is deliberately non-updatable (UNION
-- ALL), so a writer cannot mistake it for an authority.
--
-- Note on numbering: 0030 is owned by the finding-evidence migration. This
-- file was renumbered from the draft's 0030/0031 pair to keep the chain
-- contiguous and avoid a duplicate version.

-- Payloads are TEXT for byte fidelity (0013). A malformed legacy row must not
-- break every information-space query, so decode defensively.
CREATE OR REPLACE FUNCTION xalgorix_safe_jsonb(raw text) RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    RETURN raw::jsonb;
EXCEPTION WHEN others THEN
    RETURN '{}'::jsonb;
END
$$;

CREATE OR REPLACE FUNCTION xalgorix_safe_timestamptz(raw text) RETURNS timestamptz
LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    RETURN NULLIF(raw, '')::timestamptz;
EXCEPTION WHEN others THEN
    RETURN NULL;
END
$$;

DROP VIEW IF EXISTS info_space_edges;
DROP VIEW IF EXISTS info_space_items;

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
        -- Prefer the explicit info block (IG2 convention); fall back to the
        -- fields today's projections already write, so IG0 is useful before
        -- every producer fills the block.
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
        COALESCE(NULLIF(j.instance_id, ''), i.scan_id)                          AS run_id,
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
    LEFT JOIN LATERAL (
        SELECT rj.instance_id
        FROM run_journal rj
        WHERE rj.scan_id = i.scan_id AND rj.seq = i.source_seq
        LIMIT 1
    ) j ON true
)
SELECT * FROM aggregate
UNION ALL
SELECT * FROM atom;

-- One edge projection for both layers. Cross-layer links do not exist until
-- IG3 (graph_info_links); this view is already the canonical shape they will
-- join into.
CREATE OR REPLACE VIEW info_space_edges AS
SELECT
    e.scan_id,
    e.scan_id       AS run_id,
    e.src_id::text  AS src_info_id,
    e.dst_id::text  AS dst_info_id,
    e.rel::text     AS rel,
    'aggregate'::text AS layer,
    e.created_at
FROM exploration_edges e
UNION ALL
SELECT
    ie.scan_id,
    COALESCE(NULLIF(j.instance_id, ''), ie.scan_id) AS run_id,
    ie.from_id      AS src_info_id,
    ie.to_id        AS dst_info_id,
    ie.type::text   AS rel,
    'atom'::text    AS layer,
    ie.created_at
FROM info_item_edges ie
LEFT JOIN LATERAL (
    SELECT rj.instance_id
    FROM run_journal rj
    WHERE rj.scan_id = ie.scan_id AND rj.seq = ie.source_seq
    LIMIT 1
) j ON true;
