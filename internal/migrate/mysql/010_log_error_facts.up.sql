ALTER TABLE logs
    ADD COLUMN error_source VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN upstream_status INT NOT NULL DEFAULT 0;

CREATE INDEX idx_logs_error_source ON logs(error_source);
CREATE INDEX idx_logs_upstream_status ON logs(upstream_status);
