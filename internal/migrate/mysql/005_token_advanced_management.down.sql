DROP INDEX idx_tokens_rotated_from_id ON tokens;

ALTER TABLE tokens
    DROP COLUMN revoked_reason,
    DROP COLUMN rotated_from_id;
