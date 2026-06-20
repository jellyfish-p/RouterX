DROP INDEX IF EXISTS idx_tokens_rotated_from_id;
ALTER TABLE tokens DROP COLUMN revoked_reason;
ALTER TABLE tokens DROP COLUMN rotated_from_id;
