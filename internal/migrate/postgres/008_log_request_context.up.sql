ALTER TABLE logs ADD COLUMN request_id VARCHAR(128);
ALTER TABLE logs ADD COLUMN error_code VARCHAR(128) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_logs_request_id ON logs(request_id);
CREATE INDEX IF NOT EXISTS idx_logs_error_code ON logs(error_code);
