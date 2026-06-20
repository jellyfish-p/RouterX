CREATE TABLE IF NOT EXISTS alert_events (
    id BIGSERIAL PRIMARY KEY,
    type VARCHAR(128) NOT NULL,
    severity VARCHAR(32) NOT NULL DEFAULT 'warning',
    status VARCHAR(32) NOT NULL DEFAULT 'open',
    resource_type VARCHAR(64) NOT NULL,
    resource_id VARCHAR(128) NOT NULL,
    user_id BIGINT,
    token_id BIGINT,
    title VARCHAR(160) NOT NULL,
    message TEXT NOT NULL,
    details_json JSON,
    acked_at TIMESTAMPTZ,
    acked_by_user_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_alert_events_type ON alert_events(type);
CREATE INDEX IF NOT EXISTS idx_alert_events_severity ON alert_events(severity);
CREATE INDEX IF NOT EXISTS idx_alert_events_status ON alert_events(status);
CREATE INDEX IF NOT EXISTS idx_alert_resource ON alert_events(resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_user_id ON alert_events(user_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_token_id ON alert_events(token_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_acked_by_user_id ON alert_events(acked_by_user_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_created_at ON alert_events(created_at);
