-- Split login identities from users (PostgreSQL)

CREATE TABLE IF NOT EXISTS user_identities (
    id SERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    method VARCHAR(32) NOT NULL,
    provider VARCHAR(64) NOT NULL DEFAULT 'local',
    identifier VARCHAR(256) NOT NULL,
    password_hash VARCHAR(256),
    verified_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_identities_identity ON user_identities(method, provider, identifier);
CREATE INDEX IF NOT EXISTS idx_user_identities_user_id ON user_identities(user_id);
CREATE INDEX IF NOT EXISTS idx_user_identities_user_method ON user_identities(user_id, method);
CREATE INDEX IF NOT EXISTS idx_user_identities_deleted_at ON user_identities(deleted_at);

INSERT INTO user_identities (
    user_id, method, provider, identifier, password_hash, created_at, updated_at
)
SELECT id, 'username', 'local', username, password_hash, created_at, updated_at
FROM users
WHERE username IS NOT NULL AND username <> ''
ON CONFLICT (method, provider, identifier) DO NOTHING;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_username_key;
ALTER TABLE users ALTER COLUMN username DROP NOT NULL;
ALTER TABLE users ADD COLUMN phone VARCHAR(32);
ALTER TABLE users DROP COLUMN password_hash;
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_phone ON users(phone);
