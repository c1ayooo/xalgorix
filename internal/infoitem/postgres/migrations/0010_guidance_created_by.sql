-- 0010_guidance_created_by.sql — attribute human guidance to its verified
-- dashboard session so conversation views can filter to the current operator.
-- Empty means a legacy row or open-mode request, never an inferred identity.

ALTER TABLE guidance
    ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';
