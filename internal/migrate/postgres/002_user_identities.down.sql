-- Merge login identities back into users (PostgreSQL)

ALTER TABLE users ADD COLUMN password_hash VARCHAR(256) NOT NULL DEFAULT '';

UPDATE users u
SET
    username = COALESCE(NULLIF(u.username, ''), ui.identifier),
    password_hash = COALESCE(ui.password_hash, '')
FROM (
    SELECT user_id, MIN(identifier) AS identifier, MIN(password_hash) AS password_hash
    FROM user_identities
    WHERE method = 'username' AND provider = 'local' AND deleted_at IS NULL
    GROUP BY user_id
) ui
WHERE ui.user_id = u.id;

UPDATE users
SET username = 'user_' || id::text
WHERE username IS NULL OR username = '';

DROP INDEX IF EXISTS idx_users_username;
DROP INDEX IF EXISTS idx_users_email;
DROP INDEX IF EXISTS idx_users_phone;
ALTER TABLE users ALTER COLUMN username SET NOT NULL;
ALTER TABLE users ALTER COLUMN password_hash DROP DEFAULT;
ALTER TABLE users DROP COLUMN phone;
ALTER TABLE users ADD CONSTRAINT users_username_key UNIQUE (username);
DROP TABLE IF EXISTS user_identities;
