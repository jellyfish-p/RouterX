-- Admin audit logs (SQLite)

CREATE TABLE IF NOT EXISTS admin_audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT,
    actor_user_id INTEGER NOT NULL REFERENCES users(id),
    actor_role INTEGER NOT NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    before_summary TEXT NOT NULL DEFAULT '',
    after_summary TEXT NOT NULL DEFAULT '',
    result TEXT NOT NULL,
    error_code TEXT NOT NULL DEFAULT '',
    ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_admin_audit_actor_created ON admin_audit_logs(actor_user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_admin_audit_resource ON admin_audit_logs(resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_admin_audit_action ON admin_audit_logs(action);
CREATE INDEX IF NOT EXISTS idx_admin_audit_request_id ON admin_audit_logs(request_id);
