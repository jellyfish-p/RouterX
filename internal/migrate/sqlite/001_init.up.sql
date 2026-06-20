-- RouterX Initial Schema (SQLite)

CREATE TABLE IF NOT EXISTS groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    ratio REAL NOT NULL DEFAULT 1.0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    email TEXT,
    role INTEGER NOT NULL DEFAULT 0,
    quota INTEGER NOT NULL DEFAULT 0,
    status INTEGER NOT NULL DEFAULT 1,
    group_id INTEGER REFERENCES groups(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_users_deleted_at ON users(deleted_at);

CREATE TABLE IF NOT EXISTS tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id),
    name TEXT NOT NULL,
    key TEXT NOT NULL UNIQUE,
    status INTEGER NOT NULL DEFAULT 1,
    expired_at DATETIME,
    remain_quota INTEGER NOT NULL DEFAULT 0,
    unlimited INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_tokens_user_id ON tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_tokens_deleted_at ON tokens(deleted_at);

CREATE TABLE IF NOT EXISTS channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type INTEGER NOT NULL,
    name TEXT NOT NULL,
    models TEXT NOT NULL,
    base_url TEXT,
    api_key TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0,
    weight INTEGER NOT NULL DEFAULT 1,
    status INTEGER NOT NULL DEFAULT 1,
    response_ms INTEGER NOT NULL DEFAULT 0,
    balance INTEGER NOT NULL DEFAULT 0,
    error_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_channels_deleted_at ON channels(deleted_at);

CREATE TABLE IF NOT EXISTS logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id),
    token_id INTEGER REFERENCES tokens(id),
    channel_id INTEGER REFERENCES channels(id),
    model TEXT NOT NULL,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    quota_used INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    status INTEGER NOT NULL DEFAULT 0,
    content TEXT,
    response TEXT,
    error_msg TEXT,
    ip TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_logs_user_id ON logs(user_id);
CREATE INDEX IF NOT EXISTS idx_logs_token_id ON logs(token_id);
CREATE INDEX IF NOT EXISTS idx_logs_channel_id ON logs(channel_id);
CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);

CREATE TABLE IF NOT EXISTS redem_codes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code TEXT NOT NULL UNIQUE,
    quota INTEGER NOT NULL,
    status INTEGER NOT NULL DEFAULT 0,
    batch_no TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    expired_at DATETIME,
    used_by INTEGER REFERENCES users(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    used_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_redem_codes_batch_no ON redem_codes(batch_no);

CREATE TABLE IF NOT EXISTS quota_transactions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id),
    type TEXT NOT NULL,
    amount INTEGER NOT NULL,
    balance_before INTEGER NOT NULL,
    balance_after INTEGER NOT NULL,
    source_type TEXT NOT NULL,
    source_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    reason TEXT NOT NULL DEFAULT '',
    actor_user_id INTEGER REFERENCES users(id),
    request_id TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_quota_transactions_user_id_created_at ON quota_transactions(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_quota_transactions_source ON quota_transactions(source_type, source_id);

CREATE TABLE IF NOT EXISTS payment_products (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    product_id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    amount TEXT NOT NULL,
    currency TEXT NOT NULL,
    quota INTEGER NOT NULL,
    bonus_quota INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT 1,
    provider_config_json JSON,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_payment_products_enabled ON payment_products(enabled);

CREATE TABLE IF NOT EXISTS payment_orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    order_no TEXT NOT NULL UNIQUE,
    user_id INTEGER NOT NULL REFERENCES users(id),
    product_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    amount TEXT NOT NULL,
    currency TEXT NOT NULL,
    quota INTEGER NOT NULL,
    status TEXT NOT NULL,
    provider_order_id TEXT,
    provider_payment_id TEXT,
    checkout_url TEXT,
    paid_at DATETIME,
    expired_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_payment_orders_user_id_created_at ON payment_orders(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_payment_orders_provider_order_id ON payment_orders(provider_order_id);
CREATE INDEX IF NOT EXISTS idx_payment_orders_status ON payment_orders(status);

CREATE TABLE IF NOT EXISTS payment_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    provider_event_id TEXT NOT NULL,
    order_no TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '',
    signature_valid BOOLEAN NOT NULL DEFAULT 0,
    processed BOOLEAN NOT NULL DEFAULT 0,
    processed_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, provider_event_id)
);
CREATE INDEX IF NOT EXISTS idx_payment_events_order_no ON payment_events(order_no);

CREATE TABLE IF NOT EXISTS settings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    value TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'general',
    description TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
