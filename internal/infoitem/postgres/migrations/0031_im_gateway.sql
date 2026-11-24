-- 0031_im_gateway.sql — inbound IM gateway state.
--
-- Chat→task bindings, message dedupe (platform re-delivery is common) and the
-- long-poll offsets (Telegram) / cursors the adapters need across restarts.
-- Additive only.

CREATE TABLE IF NOT EXISTS im_sessions (
    platform         TEXT NOT NULL,
    chat_id          TEXT NOT NULL,
    user_id          TEXT NOT NULL DEFAULT '',
    task_id          TEXT NOT NULL,
    run_id           TEXT NOT NULL DEFAULT '',
    instance_id      TEXT NOT NULL DEFAULT '',
    label            TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (platform, chat_id)
);

CREATE TABLE IF NOT EXISTS im_messages (
    platform         TEXT NOT NULL,
    message_id       TEXT NOT NULL,
    chat_id          TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (platform, message_id)
);

CREATE TABLE IF NOT EXISTS im_offsets (
    platform         TEXT NOT NULL,
    key              TEXT NOT NULL,
    value            TEXT NOT NULL DEFAULT '',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (platform, key)
);
