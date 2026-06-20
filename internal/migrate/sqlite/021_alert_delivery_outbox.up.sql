CREATE TABLE IF NOT EXISTS alert_delivery_outboxes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    alert_id INTEGER NOT NULL,
    target TEXT NOT NULL DEFAULT 'webhook',
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME,
    UNIQUE(target, alert_id)
);

CREATE INDEX IF NOT EXISTS idx_alert_delivery_outboxes_alert_id ON alert_delivery_outboxes(alert_id);
CREATE INDEX IF NOT EXISTS idx_alert_delivery_outboxes_target ON alert_delivery_outboxes(target);
CREATE INDEX IF NOT EXISTS idx_alert_delivery_outboxes_status ON alert_delivery_outboxes(status);
CREATE INDEX IF NOT EXISTS idx_alert_delivery_outboxes_next_attempt_at ON alert_delivery_outboxes(next_attempt_at);
