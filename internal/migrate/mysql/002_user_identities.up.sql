-- Split login identities from users (MySQL)

CREATE TABLE IF NOT EXISTS user_identities (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id INT UNSIGNED NOT NULL,
    method VARCHAR(32) NOT NULL,
    provider VARCHAR(64) NOT NULL DEFAULT 'local',
    identifier VARCHAR(256) NOT NULL,
    password_hash VARCHAR(256),
    verified_at DATETIME(3),
    last_used_at DATETIME(3),
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    deleted_at DATETIME(3),
    UNIQUE INDEX idx_user_identities_identity (method, provider, identifier),
    INDEX idx_user_identities_user_id (user_id),
    INDEX idx_user_identities_user_method (user_id, method),
    INDEX idx_user_identities_deleted_at (deleted_at),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT IGNORE INTO user_identities (
    user_id, method, provider, identifier, password_hash, created_at, updated_at
)
SELECT id, 'username', 'local', username, password_hash, created_at, updated_at
FROM users
WHERE username IS NOT NULL AND username <> '';

ALTER TABLE users DROP INDEX idx_username;
ALTER TABLE users MODIFY username VARCHAR(64) NULL;
ALTER TABLE users ADD COLUMN phone VARCHAR(32) AFTER email;
ALTER TABLE users DROP COLUMN password_hash;
CREATE INDEX idx_users_username ON users(username);
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_phone ON users(phone);
