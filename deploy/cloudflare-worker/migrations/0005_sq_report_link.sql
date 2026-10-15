-- An SQ link contains the current measurement and at most one original SQ.
-- Source reports are immutable and retain their original expiration dates.
ALTER TABLE reports ADD COLUMN source_identity_hashes TEXT NOT NULL DEFAULT '[]';
ALTER TABLE reports ADD COLUMN sq_source_id TEXT NOT NULL DEFAULT '';
ALTER TABLE reports ADD COLUMN sq_source_tested_at INTEGER;
