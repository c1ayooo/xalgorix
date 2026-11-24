-- 0026_plan_target.sql — expose the intent's dispatch target in v_plan_tasks.
--
-- Intent identity is (target, class, title); endpoint alone is not enough when
-- the same route exists on several hosts. The planner dedupe key now uses
-- target when present, so the read view must carry it. New column is appended
-- at the end (CREATE OR REPLACE VIEW rule); no stored state changes.
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
    n.completed_at,
    COALESCE(n.payload::jsonb ->> 'target', '') AS target
FROM exploration_nodes n
WHERE n.kind = 'intent';
