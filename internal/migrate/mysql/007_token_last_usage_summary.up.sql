ALTER TABLE tokens
    ADD COLUMN last_used_at DATETIME(3),
    ADD COLUMN last_used_ip_hash VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN last_user_agent_hash VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN last_model VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN last_error_code VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX idx_tokens_user_id_last_used_at ON tokens(user_id, last_used_at);
