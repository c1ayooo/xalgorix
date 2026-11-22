-- 0011: conversation metadata on run mounts.
--
-- The chat page treats a run as a conversation: it needs a human-readable
-- title, a pin flag for the top of the sidebar, and soft-archive instead of
-- physical delete (findings and the scan journal must stay addressable).
ALTER TABLE runs ADD COLUMN IF NOT EXISTS title    TEXT    NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS pinned   BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS archived BOOLEAN NOT NULL DEFAULT FALSE;

-- Sidebar order: pinned first, then newest.
CREATE INDEX IF NOT EXISTS runs_task_sidebar_idx
    ON runs (task_id, pinned DESC, created_at DESC);
