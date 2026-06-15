DROP INDEX IF EXISTS idx_logs_error_code;
DROP INDEX IF EXISTS idx_logs_request_id;
ALTER TABLE logs DROP COLUMN error_code;
ALTER TABLE logs DROP COLUMN request_id;
