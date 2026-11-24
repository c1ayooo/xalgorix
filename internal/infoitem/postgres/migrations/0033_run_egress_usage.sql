-- 0032_run_egress_usage.sql — aggregated egress-proxy usage per run.
--
-- Kept even when traffic recording (MITM) is OFF: the platform still routes
-- scan traffic through the task-selected egress, and operators need to know
-- which run left through which proxy (troubleshooting / attribution of egress
-- quota). Only masked proxy labels and counters are stored — no packets, no
-- credentials. Aggregated per (scan_id, proxy_label), flushed by the router.
-- Additive only.

CREATE TABLE IF NOT EXISTS run_egress_usage (
    scan_id       TEXT NOT NULL,
    proxy_label   TEXT NOT NULL,
    instance_id   TEXT NOT NULL DEFAULT '',
    connections   BIGINT NOT NULL DEFAULT 0,
    requests      BIGINT NOT NULL DEFAULT 0,
    first_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scan_id, proxy_label)
);

CREATE INDEX IF NOT EXISTS run_egress_usage_instance_idx
    ON run_egress_usage (instance_id, last_at DESC);
