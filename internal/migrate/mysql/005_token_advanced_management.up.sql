ALTER TABLE tokens
    ADD COLUMN rotated_from_id INT UNSIGNED,
    ADD COLUMN revoked_reason VARCHAR(128) NOT NULL DEFAULT '';

CREATE INDEX idx_tokens_rotated_from_id ON tokens(rotated_from_id);
