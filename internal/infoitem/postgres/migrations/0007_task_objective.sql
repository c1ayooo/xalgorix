
-- 0007_task_objective.sql — the structured task objective (REDESIGN §3.1
-- "验收说明").
--
-- The objective is a 1:1 extension row of a task: kind (engagement /
-- capability / finding_class / coverage), the operator's statement and the
-- decidable criteria list. A missing row means "not structured" — the
-- honest default, identical to the no-DSN file store (an absent field).
--
-- It is configuration only: it never grants authority over finding
-- acceptance or verification labels (charter §3.5 / §9). The evaluator in
-- internal/objective only observes findings the report pipeline already
-- accepted.

CREATE TABLE IF NOT EXISTS task_objectives (
    task_id     UUID        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    objective   JSONB       NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id)
);

CREATE INDEX IF NOT EXISTS task_objectives_project_idx
    ON task_objectives (project_id);
