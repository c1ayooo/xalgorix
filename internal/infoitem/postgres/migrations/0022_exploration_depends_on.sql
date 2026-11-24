-- 0022_exploration_depends_on.sql — task-dependency edge relation.
--
-- PlanTask.DependsOn needs a first-class lineage relation so the PG claim
-- query can block a child intent until its parent intent/belief is terminal.
-- spawns/derived_from remain the provenance edges; depends_on is the
-- scheduling edge. The constraint is dropped and re-added because ADD
-- CONSTRAINT cannot widen an existing CHECK in place.

ALTER TABLE exploration_edges
    DROP CONSTRAINT IF EXISTS ck_exploration_edge_rel;

ALTER TABLE exploration_edges
    ADD CONSTRAINT ck_exploration_edge_rel CHECK (rel IN
        ('spawns','derived_from','yields','proves','supports','covers','depends_on'));
