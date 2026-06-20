CREATE TABLE IF NOT EXISTS payment_disputes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    provider_dispute_id TEXT NOT NULL,
    order_no TEXT NOT NULL,
    user_id INTEGER NOT NULL,
    provider_payment_id TEXT NOT NULL,
    amount_minor INTEGER NOT NULL,
    currency TEXT NOT NULL,
    status TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    funds_status TEXT NOT NULL DEFAULT '',
    last_event_id TEXT NOT NULL,
    last_event_type TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME,
    UNIQUE(provider, provider_dispute_id)
);

CREATE INDEX IF NOT EXISTS idx_payment_disputes_order_no ON payment_disputes(order_no);
CREATE INDEX IF NOT EXISTS idx_payment_disputes_user_id ON payment_disputes(user_id);
CREATE INDEX IF NOT EXISTS idx_payment_disputes_provider_payment_id ON payment_disputes(provider_payment_id);
CREATE INDEX IF NOT EXISTS idx_payment_disputes_status ON payment_disputes(status);
