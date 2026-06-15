ALTER TABLE logs ADD COLUMN usage_source VARCHAR(32) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_logs_usage_source ON logs(usage_source);
