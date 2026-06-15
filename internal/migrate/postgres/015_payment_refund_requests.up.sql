CREATE TABLE IF NOT EXISTS payment_refund_requests (
    id BIGSERIAL PRIMARY KEY,
    order_no VARCHAR(64) NOT NULL,
    user_id BIGINT NOT NULL,
    provider VARCHAR(32) NOT NULL,
    provider_refund_id VARCHAR(128) NOT NULL,
    amount VARCHAR(32) NOT NULL,
    amount_minor BIGINT NOT NULL,
    currency VARCHAR(16) NOT NULL,
    refund_quota BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL,
    idempotency_key VARCHAR(191) NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    actor_user_id BIGINT NOT NULL,
    request_id VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_order_no ON payment_refund_requests(order_no);
CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_user_id ON payment_refund_requests(user_id);
CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_provider ON payment_refund_requests(provider);
CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_provider_refund_id ON payment_refund_requests(provider_refund_id);
CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_status ON payment_refund_requests(status);
CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_actor_user_id ON payment_refund_requests(actor_user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_refund_requests_idempotency_key ON payment_refund_requests(idempotency_key);
