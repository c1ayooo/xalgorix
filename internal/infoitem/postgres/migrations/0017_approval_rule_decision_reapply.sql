-- 0017_approval_rule_decision_reapply.sql — heal the 0009 drift.
--
-- 0009_approval_rule_decision.sql was edited AFTER it had been applied to
-- production databases (the versions table already held 9, so the added
-- ALTER never re-ran). The column is missing wherever 0009 applied in its
-- earlier form, and the scope observer's preset seed fails with
-- SQLSTATE 42703 (column "decision" does not exist).
--
-- This migration re-applies the intended, idempotent shape under a NEW
-- version number. Never edit an applied migration in place.

ALTER TABLE approval_rules
    ADD COLUMN IF NOT EXISTS decision TEXT NOT NULL DEFAULT 'allow';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'approval_rules_decision_check'
    ) THEN
        ALTER TABLE approval_rules
            ADD CONSTRAINT approval_rules_decision_check
            CHECK (decision IN ('allow', 'deny'));
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS approval_rules_project_decision_idx
    ON approval_rules (project_id, decision);
