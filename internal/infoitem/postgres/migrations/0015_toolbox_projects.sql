-- 0015_toolbox_projects.sql — desired-state bookkeeping for the containerised
-- tool runtime (toolbox, "预构建统一工具容器镜像 + 平台按项目调用").
--
-- Authority split (deliberate, documented in docs/ARCHITECTURE.md):
--   * the ACTUAL state of a container (does it exist, is it running) is owned by
--     the container runtime and read back through labels;
--   * the DESIRED state and the reconciliation watermark live here, because a
--     crash between "container created" and "state recorded" is only recoverable
--     from a durable intent.
--
-- The watermark protocol mirrors the InfoItem cold path (desired_gen vs
-- applied_gen, status pending/applied/gap/failed) so a single reconciliation
-- model covers both pipelines (charter §8: River only drives cold-path
-- catch-up; it never owns the scan lifecycle).
--
-- Toolbox containers are ephemeral: rows are advisory bookkeeping, not scan
-- state. Removing a row never deletes evidence — findings, ledger and tool
-- output archives live in the scan directory and PostgreSQL tables owned by
-- their original subsystems.

CREATE TABLE IF NOT EXISTS toolbox_projects (
    project_id    TEXT        PRIMARY KEY,
    scan_id       TEXT        NOT NULL,
    instance_id   TEXT        NOT NULL,
    image_digest  TEXT        NOT NULL,
    profile       TEXT        NOT NULL DEFAULT 'core',
    net_profile   TEXT        NOT NULL DEFAULT 'proxy-only',
    desired_state TEXT        NOT NULL DEFAULT 'pending'
                              CHECK (desired_state IN ('pending', 'ready', 'stopping', 'removed')),
    desired_gen   BIGINT      NOT NULL DEFAULT 1,
    applied_gen   BIGINT      NOT NULL DEFAULT 0,
    status        TEXT        NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending', 'applied', 'gap', 'failed')),
    container_id  TEXT        NOT NULL DEFAULT '',
    last_error    TEXT        NOT NULL DEFAULT '',
    -- Serialised ProjectSpec so a reconciler can rebuild the exact container
    -- after a restart without consulting live memory.
    spec          TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS toolbox_projects_scan_idx
    ON toolbox_projects (scan_id);

CREATE INDEX IF NOT EXISTS toolbox_projects_instance_status_idx
    ON toolbox_projects (instance_id, status);

-- Pending reconciliation work is the hot query (reconciler tick): keep it cheap
-- and partial so the index stays small even with a long scan history.
CREATE INDEX IF NOT EXISTS toolbox_projects_unreconciled_idx
    ON toolbox_projects (updated_at)
    WHERE applied_gen < desired_gen OR status IN ('pending', 'gap', 'failed');

-- Read-only projection consumed by the dashboard's runtime status card.
-- One row per image digest; the panel shows passed/failed from the last run.
CREATE TABLE IF NOT EXISTS toolbox_image_health (
    image_digest TEXT        PRIMARY KEY,
    profile      TEXT        NOT NULL,
    passed       INT         NOT NULL DEFAULT 0,
    failed       INT         NOT NULL DEFAULT 0,
    report       TEXT        NOT NULL DEFAULT '',
    checked_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
