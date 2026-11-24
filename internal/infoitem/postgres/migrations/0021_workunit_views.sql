-- 0021_workunit_views.sql — PlanTask / Units read views over the exploration
-- graph authority (R7/M1).
--
-- These are READ VIEWS. They do not store state and must never be written
-- through. PlanTask tools (build_plan / update_plan / claim_next_task) write
-- exploration_nodes(kind='intent'); /plan and /units read these views.
--
-- status maps the graph state onto the legacy PlanTask status vocabulary so
-- existing finish gates and panels keep their contract:
--   open      -> pending
--   running   -> active
--   paused    -> active   (claimed but suspended)
--   done      -> completed
--   blocked   -> skipped  (+ disposition in payload)
--   exhausted -> skipped
--   stopped   -> skipped
--   deleted   -> skipped

CREATE OR REPLACE VIEW v_plan_tasks AS
SELECT
    n.scan_id,
    n.id                                        AS node_id,
    n.instance_id,
    COALESCE(NULLIF(n.payload::jsonb ->> 'task_id', ''), n.id::text)
                                                AS task_id,
    COALESCE(n.payload::jsonb ->> 'title', '')  AS title,
    COALESCE(NULLIF(n.payload::jsonb ->> 'phase', '')::int, 0)
                                                AS phase,
    COALESCE(n.payload::jsonb ->> 'vuln_class', '') AS vuln_class,
    COALESCE(n.payload::jsonb ->> 'endpoint', '')   AS endpoint,
    n.state                                     AS graph_state,
    CASE n.state
        WHEN 'open'      THEN 'pending'
        WHEN 'running'   THEN 'active'
        WHEN 'paused'    THEN 'active'
        WHEN 'done'      THEN 'completed'
        WHEN 'blocked'   THEN 'skipped'
        WHEN 'exhausted' THEN 'skipped'
        WHEN 'stopped'   THEN 'skipped'
        WHEN 'deleted'   THEN 'skipped'
        ELSE n.state
    END                                         AS status,
    COALESCE(n.payload::jsonb ->> 'disposition', '') AS disposition,
    COALESCE(NULLIF(n.payload::jsonb ->> 'origin', ''), n.origin, '')
                                                AS origin,
    COALESCE(n.payload::jsonb -> 'depends_on', '[]'::jsonb)
                                                AS depends_on,
    COALESCE(n.payload::jsonb ->> 'notes', '')  AS notes,
    COALESCE((n.payload::jsonb ->> 'prerequisite')::boolean, false)
                                                AS prerequisite,
    COALESCE((n.payload::jsonb ->> 'whole_target')::boolean, false)
                                                AS whole_target,
    COALESCE(n.payload::jsonb ->> 'objective_ref', '') AS objective_ref,
    n.owner,
    n.lease_until,
    n.priority,
    n.origin                                    AS graph_origin,
    n.created_at,
    n.updated_at,
    n.completed_at
FROM exploration_nodes n
WHERE n.kind = 'intent';

CREATE OR REPLACE VIEW v_units AS
SELECT
    scan_id,
    'intent'::text                              AS kind,
    node_id,
    task_id                                     AS unit_id,
    CASE
        WHEN endpoint <> '' THEN 'endpoint:' || lower(endpoint)
        WHEN vuln_class <> '' THEN 'phase:' || phase::text || ':' || lower(vuln_class)
        ELSE 'phase:' || phase::text
    END                                         AS subject,
    graph_state                                 AS state,
    owner,
    lease_until,
    vuln_class,
    phase,
    (graph_state IN ('running','paused') AND lease_until IS NOT NULL AND lease_until < now())
                                                AS expired
FROM v_plan_tasks
UNION ALL
SELECT
    n.scan_id,
    'belief'::text                              AS kind,
    n.id                                        AS node_id,
    COALESCE(NULLIF(n.payload::jsonb ->> 'hypothesis_id', ''), n.id::text)
                                                AS unit_id,
    COALESCE(NULLIF(n.payload::jsonb ->> 'subject', ''),
             NULLIF(n.payload::jsonb ->> 'hypothesis_id', ''),
             n.id::text)                        AS subject,
    n.state,
    n.owner,
    n.lease_until,
    COALESCE(n.payload::jsonb ->> 'vuln_class', '') AS vuln_class,
    COALESCE(NULLIF(n.payload::jsonb ->> 'phase', '')::int, 0) AS phase,
    (n.state = 'testing' AND n.lease_until IS NOT NULL AND n.lease_until < now())
                                                AS expired
FROM exploration_nodes n
WHERE n.kind = 'belief';
