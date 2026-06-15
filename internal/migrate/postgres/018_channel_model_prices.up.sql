CREATE TABLE IF NOT EXISTS channel_model_prices (
    id BIGSERIAL PRIMARY KEY,
    channel_id BIGINT NOT NULL,
    model VARCHAR(128) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    user_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    price_mode VARCHAR(32) NOT NULL,
    override_mode VARCHAR(32) NOT NULL DEFAULT 'override',
    price_expression TEXT NOT NULL,
    variables_json JSON,
    unit_tokens BIGINT NOT NULL DEFAULT 1000,
    rule_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ,
    UNIQUE(channel_id, model)
);

CREATE INDEX IF NOT EXISTS idx_channel_model_prices_channel_id ON channel_model_prices(channel_id);
CREATE INDEX IF NOT EXISTS idx_channel_model_prices_enabled ON channel_model_prices(enabled);
CREATE INDEX IF NOT EXISTS idx_channel_model_prices_user_enabled ON channel_model_prices(user_enabled);
