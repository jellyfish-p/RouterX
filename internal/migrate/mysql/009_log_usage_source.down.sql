DROP INDEX idx_logs_usage_source ON logs;
ALTER TABLE logs DROP COLUMN usage_source;
