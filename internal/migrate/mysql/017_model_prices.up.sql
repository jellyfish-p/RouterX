CREATE TABLE IF NOT EXISTS model_prices (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    model VARCHAR(128) NOT NULL,
    price_mode VARCHAR(32) NOT NULL,
    price_expression TEXT NOT NULL,
    variables_json JSON,
    unit_tokens BIGINT NOT NULL DEFAULT 1000,
    rule_version BIGINT NOT NULL DEFAULT 1,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3),
    UNIQUE INDEX idx_model_prices_model (model),
    INDEX idx_model_prices_enabled (enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
