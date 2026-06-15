CREATE TABLE IF NOT EXISTS channel_model_prices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id INTEGER NOT NULL,
    model TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    user_enabled INTEGER NOT NULL DEFAULT 1,
    price_mode TEXT NOT NULL,
    override_mode TEXT NOT NULL DEFAULT 'override',
    price_expression TEXT NOT NULL,
    variables_json JSON,
    unit_tokens INTEGER NOT NULL DEFAULT 1000,
    rule_version INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME,
    UNIQUE(channel_id, model)
);

CREATE INDEX IF NOT EXISTS idx_channel_model_prices_channel_id ON channel_model_prices(channel_id);
CREATE INDEX IF NOT EXISTS idx_channel_model_prices_enabled ON channel_model_prices(enabled);
CREATE INDEX IF NOT EXISTS idx_channel_model_prices_user_enabled ON channel_model_prices(user_enabled);
