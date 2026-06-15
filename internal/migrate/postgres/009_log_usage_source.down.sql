DROP INDEX IF EXISTS idx_logs_usage_source;
ALTER TABLE logs DROP COLUMN usage_source;
