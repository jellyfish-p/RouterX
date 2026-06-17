CREATE TABLE IF NOT EXISTS log_replication_outboxes (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    log_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempts BIGINT NOT NULL DEFAULT 0,
    last_error TEXT,
    next_attempt_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    completed_at DATETIME(3),
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3),
    UNIQUE INDEX idx_log_replication_outboxes_log_id (log_id),
    INDEX idx_log_replication_outboxes_status (status),
    INDEX idx_log_replication_outboxes_next_attempt_at (next_attempt_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
