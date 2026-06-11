-- Merge login identities back into users (MySQL)

ALTER TABLE users ADD COLUMN password_hash VARCHAR(256) NOT NULL DEFAULT '' AFTER username;

UPDATE users u
LEFT JOIN (
    SELECT user_id, MIN(identifier) AS identifier, MIN(password_hash) AS password_hash
    FROM user_identities
    WHERE method = 'username' AND provider = 'local' AND deleted_at IS NULL
    GROUP BY user_id
) ui ON ui.user_id = u.id
SET
    u.username = COALESCE(NULLIF(u.username, ''), ui.identifier, CONCAT('user_', u.id)),
    u.password_hash = COALESCE(ui.password_hash, '');

ALTER TABLE users DROP INDEX idx_users_username;
ALTER TABLE users DROP INDEX idx_users_email;
ALTER TABLE users DROP INDEX idx_users_phone;
ALTER TABLE users MODIFY username VARCHAR(64) NOT NULL;
ALTER TABLE users DROP COLUMN phone;
ALTER TABLE users ADD UNIQUE INDEX idx_username (username);
DROP TABLE IF EXISTS user_identities;
