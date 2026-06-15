DROP INDEX IF EXISTS idx_logs_upstream_status;
DROP INDEX IF EXISTS idx_logs_error_source;
ALTER TABLE logs DROP COLUMN upstream_status;
ALTER TABLE logs DROP COLUMN error_source;
