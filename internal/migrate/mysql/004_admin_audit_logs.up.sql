-- Admin audit logs (MySQL)

CREATE TABLE IF NOT EXISTS admin_audit_logs (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    request_id VARCHAR(64),
    actor_user_id INT UNSIGNED NOT NULL,
    actor_role INT NOT NULL,
    action VARCHAR(128) NOT NULL,
    resource_type VARCHAR(64) NOT NULL,
    resource_id VARCHAR(128) NOT NULL,
    before_summary TEXT NOT NULL,
    after_summary TEXT NOT NULL,
    result VARCHAR(32) NOT NULL,
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    ip VARCHAR(64) NOT NULL DEFAULT '',
    user_agent VARCHAR(256) NOT NULL DEFAULT '',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    INDEX idx_admin_audit_actor_created (actor_user_id, created_at),
    INDEX idx_admin_audit_resource (resource_type, resource_id),
    INDEX idx_admin_audit_action (action),
    INDEX idx_admin_audit_request_id (request_id),
    FOREIGN KEY (actor_user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
