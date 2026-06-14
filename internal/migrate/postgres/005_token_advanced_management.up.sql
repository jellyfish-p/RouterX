ALTER TABLE tokens ADD COLUMN rotated_from_id INT REFERENCES tokens(id);
ALTER TABLE tokens ADD COLUMN revoked_reason VARCHAR(128) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_tokens_rotated_from_id ON tokens(rotated_from_id);
