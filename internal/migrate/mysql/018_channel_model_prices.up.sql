CREATE TABLE IF NOT EXISTS channel_model_prices (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    channel_id BIGINT UNSIGNED NOT NULL,
    model VARCHAR(128) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    user_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    price_mode VARCHAR(32) NOT NULL,
    override_mode VARCHAR(32) NOT NULL DEFAULT 'override',
    price_expression TEXT NOT NULL,
    variables_json JSON,
    unit_tokens BIGINT NOT NULL DEFAULT 1000,
    rule_version BIGINT NOT NULL DEFAULT 1,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3),
    UNIQUE INDEX idx_channel_model_prices_channel_model (channel_id, model),
    INDEX idx_channel_model_prices_channel_id (channel_id),
    INDEX idx_channel_model_prices_enabled (enabled),
    INDEX idx_channel_model_prices_user_enabled (user_enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
