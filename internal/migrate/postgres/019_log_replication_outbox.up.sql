CREATE TABLE IF NOT EXISTS log_replication_outboxes (
    id BIGSERIAL PRIMARY KEY,
    log_id BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempts BIGINT NOT NULL DEFAULT 0,
    last_error TEXT,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ,
    UNIQUE(log_id)
);

CREATE INDEX IF NOT EXISTS idx_log_replication_outboxes_status ON log_replication_outboxes(status);
CREATE INDEX IF NOT EXISTS idx_log_replication_outboxes_next_attempt_at ON log_replication_outboxes(next_attempt_at);
