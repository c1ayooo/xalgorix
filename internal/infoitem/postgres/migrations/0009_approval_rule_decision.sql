-- 0009_approval_rule_decision.sql — scope observer decisions on approval rules.
--
-- Before this migration every approval_rules row was an exemption ("approve &
-- remember"). The scope observer (internal/web/scope_observer.go) needs the
-- inverse too: a rule that AUTO-REJECTS a matching call — bulk data deletion,
-- file uploads and web/OSS control-plane writes against the bound project.
--
-- decision is NOT NULL with DEFAULT 'allow' so every pre-existing rule keeps
-- its exact historical meaning (no backfill, no behavior change), and the
-- CHECK pins the vocabulary to the two values platform.NormalizeApprovalRuleDecision
-- understands.
--
-- Applied by the info-model migrator inside the same advisory-locked
-- transaction as 0001–0008.

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
