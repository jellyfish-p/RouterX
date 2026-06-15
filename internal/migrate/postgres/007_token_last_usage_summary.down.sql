DROP INDEX IF EXISTS idx_tokens_user_id_last_used_at;
ALTER TABLE tokens DROP COLUMN last_error_code;
ALTER TABLE tokens DROP COLUMN last_model;
ALTER TABLE tokens DROP COLUMN last_user_agent_hash;
ALTER TABLE tokens DROP COLUMN last_used_ip_hash;
ALTER TABLE tokens DROP COLUMN last_used_at;
