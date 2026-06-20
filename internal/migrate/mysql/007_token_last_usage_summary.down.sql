DROP INDEX idx_tokens_user_id_last_used_at ON tokens;

ALTER TABLE tokens
    DROP COLUMN last_error_code,
    DROP COLUMN last_model,
    DROP COLUMN last_user_agent_hash,
    DROP COLUMN last_used_ip_hash,
    DROP COLUMN last_used_at;
