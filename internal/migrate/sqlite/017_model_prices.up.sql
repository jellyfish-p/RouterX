CREATE TABLE IF NOT EXISTS model_prices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model TEXT NOT NULL UNIQUE,
    price_mode TEXT NOT NULL,
    price_expression TEXT NOT NULL,
    variables_json JSON,
    unit_tokens INTEGER NOT NULL DEFAULT 1000,
    rule_version INTEGER NOT NULL DEFAULT 1,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_model_prices_enabled ON model_prices(enabled);
