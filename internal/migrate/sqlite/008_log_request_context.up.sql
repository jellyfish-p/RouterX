ALTER TABLE logs ADD COLUMN request_id TEXT;
ALTER TABLE logs ADD COLUMN error_code TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_logs_request_id ON logs(request_id);
CREATE INDEX IF NOT EXISTS idx_logs_error_code ON logs(error_code);
