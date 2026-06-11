-- Merge login identities back into users (SQLite)

PRAGMA foreign_keys=OFF;

CREATE TABLE users_old (
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

INSERT INTO users_old (
    id, username, password_hash, display_name, email, role, quota, status, group_id, created_at, updated_at, deleted_at
)
SELECT
    u.id,
    COALESCE(NULLIF(u.username, ''), ui.identifier, 'user_' || u.id),
    COALESCE(ui.password_hash, ''),
    u.display_name,
    u.email,
    u.role,
    u.quota,
    u.status,
    u.group_id,
    u.created_at,
    u.updated_at,
    u.deleted_at
FROM users u
LEFT JOIN (
    SELECT user_id, MIN(identifier) AS identifier, MIN(password_hash) AS password_hash
    FROM user_identities
    WHERE method = 'username' AND provider = 'local' AND deleted_at IS NULL
    GROUP BY user_id
) ui ON ui.user_id = u.id;

DROP TABLE IF EXISTS user_identities;
DROP TABLE users;
ALTER TABLE users_old RENAME TO users;

CREATE INDEX IF NOT EXISTS idx_users_deleted_at ON users(deleted_at);

PRAGMA foreign_keys=ON;
