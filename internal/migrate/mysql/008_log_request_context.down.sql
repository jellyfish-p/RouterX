DROP INDEX idx_logs_error_code ON logs;
DROP INDEX idx_logs_request_id ON logs;

ALTER TABLE logs
    DROP COLUMN error_code,
    DROP COLUMN request_id;
