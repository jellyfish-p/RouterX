-- Admin audit logs (PostgreSQL)

CREATE TABLE IF NOT EXISTS admin_audit_logs (
    id SERIAL PRIMARY KEY,
    request_id VARCHAR(64),
    actor_user_id INT NOT NULL REFERENCES users(id),
    actor_role INT NOT NULL,
    action VARCHAR(128) NOT NULL,
    resource_type VARCHAR(64) NOT NULL,
    resource_id VARCHAR(128) NOT NULL,
    before_summary TEXT NOT NULL DEFAULT '',
    after_summary TEXT NOT NULL DEFAULT '',
    result VARCHAR(32) NOT NULL,
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    ip VARCHAR(64) NOT NULL DEFAULT '',
    user_agent VARCHAR(256) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_admin_audit_actor_created ON admin_audit_logs(actor_user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_admin_audit_resource ON admin_audit_logs(resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_admin_audit_action ON admin_audit_logs(action);
CREATE INDEX IF NOT EXISTS idx_admin_audit_request_id ON admin_audit_logs(request_id);
