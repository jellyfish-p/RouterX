CREATE TABLE IF NOT EXISTS alert_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'warning',
    status TEXT NOT NULL DEFAULT 'open',
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    user_id INTEGER,
    token_id INTEGER,
    title TEXT NOT NULL,
    message TEXT NOT NULL,
    details_json JSON,
    acked_at DATETIME,
    acked_by_user_id INTEGER,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_alert_events_type ON alert_events(type);
CREATE INDEX IF NOT EXISTS idx_alert_events_severity ON alert_events(severity);
CREATE INDEX IF NOT EXISTS idx_alert_events_status ON alert_events(status);
CREATE INDEX IF NOT EXISTS idx_alert_resource ON alert_events(resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_user_id ON alert_events(user_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_token_id ON alert_events(token_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_acked_by_user_id ON alert_events(acked_by_user_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_created_at ON alert_events(created_at);
