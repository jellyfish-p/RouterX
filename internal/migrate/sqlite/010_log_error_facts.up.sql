ALTER TABLE logs ADD COLUMN error_source TEXT NOT NULL DEFAULT '';
ALTER TABLE logs ADD COLUMN upstream_status INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_logs_error_source ON logs(error_source);
CREATE INDEX IF NOT EXISTS idx_logs_upstream_status ON logs(upstream_status);
