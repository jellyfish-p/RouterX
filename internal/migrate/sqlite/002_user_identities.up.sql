-- Split login identities from users (SQLite)

PRAGMA foreign_keys=OFF;

CREATE TABLE IF NOT EXISTS user_identities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    method TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'local',
    identifier TEXT NOT NULL,
    password_hash TEXT,
    verified_at DATETIME,
    last_used_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_identities_identity ON user_identities(method, provider, identifier);
CREATE INDEX IF NOT EXISTS idx_user_identities_user_id ON user_identities(user_id);
CREATE INDEX IF NOT EXISTS idx_user_identities_user_method ON user_identities(user_id, method);
CREATE INDEX IF NOT EXISTS idx_user_identities_deleted_at ON user_identities(deleted_at);

INSERT OR IGNORE INTO user_identities (
    user_id, method, provider, identifier, password_hash, created_at, updated_at
)
SELECT id, 'username', 'local', username, password_hash, created_at, updated_at
FROM users
WHERE username IS NOT NULL AND username <> '';

CREATE TABLE users_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT,
    display_name TEXT NOT NULL DEFAULT '',
    email TEXT,
    phone TEXT,
    role INTEGER NOT NULL DEFAULT 0,
    quota INTEGER NOT NULL DEFAULT 0,
    status INTEGER NOT NULL DEFAULT 1,
    group_id INTEGER REFERENCES groups(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);

INSERT INTO users_new (
    id, username, display_name, email, phone, role, quota, status, group_id, created_at, updated_at, deleted_at
)
SELECT id, username, display_name, email, NULL, role, quota, status, group_id, created_at, updated_at, deleted_at
FROM users;

DROP TABLE users;
ALTER TABLE users_new RENAME TO users;

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_phone ON users(phone);
CREATE INDEX IF NOT EXISTS idx_users_deleted_at ON users(deleted_at);

PRAGMA foreign_keys=ON;
