CREATE TABLE IF NOT EXISTS payment_disputes (
    id BIGSERIAL PRIMARY KEY,
    provider VARCHAR(32) NOT NULL,
    provider_dispute_id VARCHAR(128) NOT NULL,
    order_no VARCHAR(64) NOT NULL,
    user_id BIGINT NOT NULL,
    provider_payment_id VARCHAR(128) NOT NULL,
    amount_minor BIGINT NOT NULL,
    currency VARCHAR(16) NOT NULL,
    status VARCHAR(32) NOT NULL,
    reason VARCHAR(128) NOT NULL DEFAULT '',
    funds_status VARCHAR(32) NOT NULL DEFAULT '',
    last_event_id VARCHAR(128) NOT NULL,
    last_event_type VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ,
    UNIQUE(provider, provider_dispute_id)
);

CREATE INDEX IF NOT EXISTS idx_payment_disputes_order_no ON payment_disputes(order_no);
CREATE INDEX IF NOT EXISTS idx_payment_disputes_user_id ON payment_disputes(user_id);
CREATE INDEX IF NOT EXISTS idx_payment_disputes_provider_payment_id ON payment_disputes(provider_payment_id);
CREATE INDEX IF NOT EXISTS idx_payment_disputes_status ON payment_disputes(status);
