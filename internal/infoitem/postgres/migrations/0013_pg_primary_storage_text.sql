-- 0013_pg_primary_storage_text.sql — R6.0 hotfix for 0012 as first shipped.
--
-- 0012 briefly shipped its opaque payload columns as JSONB. Engine content
-- (tool output text embedded in events/journal/ledger/dispatch documents)
-- can carry NUL-byte escapes that JSONB rejects at insert time
-- (SQLSTATE 22P05, observed on a real .dispatches tombstone). These documents
-- are decoded wholesale, so TEXT is the correct type and preserves byte
-- fidelity exactly. The jsonb→text USING cast is a no-op on databases where
-- 0012 already shipped as TEXT.
--
-- Structured, genuinely-queryable documents (info_items/edges) keep JSONB;
-- only the opaque payload families from 0012 change type here.

ALTER TABLE runs ALTER COLUMN scan_detail TYPE TEXT USING scan_detail::text;
ALTER TABLE run_events ALTER COLUMN payload TYPE TEXT USING payload::text;
ALTER TABLE run_journal ALTER COLUMN record TYPE TEXT USING record::text;
ALTER TABLE run_ledger_snapshots ALTER COLUMN data TYPE TEXT USING data::text;
ALTER TABLE run_state_files ALTER COLUMN data TYPE TEXT USING data::text;
ALTER TABLE dispatch_snapshots ALTER COLUMN data TYPE TEXT USING data::text;
