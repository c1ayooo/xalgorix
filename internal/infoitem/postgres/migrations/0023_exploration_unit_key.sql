-- 0023_exploration_unit_key.sql — stable business key for graph projections.
--
-- Every authoritative node carries a run-scoped unit key:
--   intent  -> PlanTask id
--   belief  -> Hypothesis id
--   fact    -> Evidence id
--   finding -> accepted finding id
--   goal    -> objective criterion id
--   hint    -> guidance id
-- The partial unique index makes projection writes idempotent without
-- changing the lineage id (BIGSERIAL remains the edge endpoint).

ALTER TABLE exploration_nodes
    ADD COLUMN IF NOT EXISTS unit_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS exploration_nodes_unit_key_idx
    ON exploration_nodes (scan_id, kind, unit_key)
    WHERE unit_key <> '';
