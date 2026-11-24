-- 0033_graph_info_links.sql — IG3 aggregate↔atom linkage.
--
-- A graph aggregate (fact/belief/finding) is the decision-grained summary; the
-- atom is the InfoItem instance behind it. The link table lets association
-- lighting cross layers without turning every observation into a graph node.
--
-- Additive only. `materializes` is a read-model edge: it is projected by the
-- info_space_edges view, never written into exploration_edges.

CREATE TABLE IF NOT EXISTS graph_info_links (
    run_id       TEXT        NOT NULL,
    node_id      BIGINT      NOT NULL,
    info_item_id TEXT        NOT NULL,
    role         TEXT        NOT NULL DEFAULT 'provenance'
                             CHECK (role IN ('provenance', 'supports', 'contradicts', 'materializes')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, node_id, info_item_id, role),
    CONSTRAINT graph_info_links_node_fk FOREIGN KEY (run_id, node_id)
        REFERENCES exploration_nodes (scan_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS graph_info_links_info_idx
    ON graph_info_links (run_id, info_item_id);

-- Backfill the scalar linkage the fact projection already carries.
INSERT INTO graph_info_links (run_id, node_id, info_item_id, role)
SELECT n.scan_id, n.id, xalgorix_safe_jsonb(n.payload) ->> 'info_item_id', 'provenance'
FROM exploration_nodes n
WHERE n.kind IN ('fact', 'finding')
  AND COALESCE(xalgorix_safe_jsonb(n.payload) ->> 'info_item_id', '') <> ''
ON CONFLICT DO NOTHING;

-- Backfill the array form (findings/projections may carry several atoms).
INSERT INTO graph_info_links (run_id, node_id, info_item_id, role)
SELECT n.scan_id, n.id, atom_id, 'provenance'
FROM exploration_nodes n
CROSS JOIN LATERAL jsonb_array_elements_text(
    CASE
        WHEN jsonb_typeof(xalgorix_safe_jsonb(n.payload) -> 'info_item_ids') = 'array'
        THEN xalgorix_safe_jsonb(n.payload) -> 'info_item_ids'
        ELSE '[]'::jsonb
    END
) AS atom_id
WHERE n.kind IN ('fact', 'finding')
  AND atom_id <> ''
ON CONFLICT DO NOTHING;

-- Project links as a third edge branch. Column set is identical to 0031.
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
) j ON true
UNION ALL
SELECT
    l.run_id,
    l.run_id        AS run_id,
    l.node_id::text AS src_info_id,
    l.info_item_id  AS dst_info_id,
    'materializes'::text AS rel,
    'link'::text    AS layer,
    l.created_at
FROM graph_info_links l;
