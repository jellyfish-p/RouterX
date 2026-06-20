DROP INDEX idx_logs_upstream_status ON logs;
DROP INDEX idx_logs_error_source ON logs;

ALTER TABLE logs
    DROP COLUMN upstream_status,
    DROP COLUMN error_source;
