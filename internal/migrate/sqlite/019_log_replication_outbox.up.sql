CREATE TABLE IF NOT EXISTS log_replication_outboxes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    log_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME,
    UNIQUE(log_id)
);

CREATE INDEX IF NOT EXISTS idx_log_replication_outboxes_status ON log_replication_outboxes(status);
CREATE INDEX IF NOT EXISTS idx_log_replication_outboxes_next_attempt_at ON log_replication_outboxes(next_attempt_at);
