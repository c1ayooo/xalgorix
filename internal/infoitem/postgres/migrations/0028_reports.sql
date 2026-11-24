-- 0028_reports.sql — manual report delivery (REPORT_DELIVERY_SPEC §3).
--
-- Report metadata is the status/version authority; rendered PDF/MD/HTML files
-- are byte artifacts on disk (D12). The partial unique index enforces at most
-- one active generation per task at the database level, complementing the web
-- layer's job guard.

CREATE TABLE IF NOT EXISTS reports (
    id             UUID PRIMARY KEY,
    project_id     UUID NULL,
    task_id        UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    revision       INT NOT NULL,
    run_ids        JSONB NOT NULL DEFAULT '[]'::jsonb,
    scan_ids       JSONB NOT NULL DEFAULT '[]'::jsonb,
    status         TEXT NOT NULL,
    template       TEXT NOT NULL DEFAULT 'standard',
    language       TEXT NOT NULL DEFAULT 'zh-CN',
    mode           TEXT NOT NULL DEFAULT 'template',
    model          TEXT NOT NULL DEFAULT '',
    prompt_version TEXT NOT NULL DEFAULT '',
    formats        JSONB NOT NULL DEFAULT '[]'::jsonb,
    error          TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at     TIMESTAMPTZ NULL,
    finished_at    TIMESTAMPTZ NULL,
    UNIQUE (task_id, revision)
);

CREATE INDEX IF NOT EXISTS reports_task_idx
    ON reports (task_id, revision DESC);

CREATE UNIQUE INDEX IF NOT EXISTS uq_reports_active_task
    ON reports (task_id)
    WHERE status IN ('queued', 'generating');

CREATE TABLE IF NOT EXISTS report_artifacts (
    report_id  UUID NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    scan_id    TEXT NOT NULL DEFAULT '',
    target     TEXT NOT NULL DEFAULT '',
    format     TEXT NOT NULL,
    path       TEXT NOT NULL,
    bytes      BIGINT NOT NULL DEFAULT 0,
    sha256     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (report_id, kind, scan_id, format)
);
