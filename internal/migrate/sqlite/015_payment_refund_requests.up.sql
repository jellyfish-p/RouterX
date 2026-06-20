CREATE TABLE IF NOT EXISTS payment_refund_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    order_no TEXT NOT NULL,
    user_id INTEGER NOT NULL,
    provider TEXT NOT NULL,
    provider_refund_id TEXT NOT NULL,
    amount TEXT NOT NULL,
    amount_minor INTEGER NOT NULL,
    currency TEXT NOT NULL,
    refund_quota INTEGER NOT NULL,
    status TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    actor_user_id INTEGER NOT NULL,
    request_id TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_order_no ON payment_refund_requests(order_no);
CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_user_id ON payment_refund_requests(user_id);
CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_provider ON payment_refund_requests(provider);
CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_provider_refund_id ON payment_refund_requests(provider_refund_id);
CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_status ON payment_refund_requests(status);
CREATE INDEX IF NOT EXISTS idx_payment_refund_requests_actor_user_id ON payment_refund_requests(actor_user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_refund_requests_idempotency_key ON payment_refund_requests(idempotency_key);
