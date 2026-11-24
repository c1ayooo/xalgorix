-- 0024_task_audit_mode.sql — task-level HITL/audit switch.
--
-- The task owns the audit posture of the runs it dispatches:
--   inherit (default) -> follow XALGORIX_APPROVAL_MODE
--   off               -> observe-only (no approval rows) for this task
--   enforce           -> command + high-risk MITM HTTP calls create approval
--                        rows and block until the operator decides.
-- Existing tasks inherit the process default, so the column default keeps the
-- pre-feature posture byte-for-byte.

ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS audit_mode TEXT NOT NULL DEFAULT 'inherit';
