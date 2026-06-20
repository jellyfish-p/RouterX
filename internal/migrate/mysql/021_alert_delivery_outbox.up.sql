CREATE TABLE IF NOT EXISTS alert_delivery_outboxes (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    alert_id BIGINT UNSIGNED NOT NULL,
    target VARCHAR(32) NOT NULL DEFAULT 'webhook',
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempts BIGINT NOT NULL DEFAULT 0,
    last_error TEXT,
    next_attempt_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    completed_at DATETIME(3),
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3),
    UNIQUE INDEX idx_alert_delivery_target_alert (target, alert_id),
    INDEX idx_alert_delivery_outboxes_alert_id (alert_id),
    INDEX idx_alert_delivery_outboxes_target (target),
    INDEX idx_alert_delivery_outboxes_status (status),
    INDEX idx_alert_delivery_outboxes_next_attempt_at (next_attempt_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
