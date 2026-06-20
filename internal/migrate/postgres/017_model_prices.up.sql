CREATE TABLE IF NOT EXISTS model_prices (
    id BIGSERIAL PRIMARY KEY,
    model VARCHAR(128) NOT NULL UNIQUE,
    price_mode VARCHAR(32) NOT NULL,
    price_expression TEXT NOT NULL,
    variables_json JSON,
    unit_tokens BIGINT NOT NULL DEFAULT 1000,
    rule_version BIGINT NOT NULL DEFAULT 1,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_model_prices_enabled ON model_prices(enabled);
