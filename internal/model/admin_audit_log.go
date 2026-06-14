package model

import "time"

// AdminAuditLog 记录管理端高风险操作的可复核摘要。
// 它只保存脱敏摘要，不保存完整请求体或密钥明文。
type AdminAuditLog struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	RequestID     *string   `gorm:"type:varchar(64);index" json:"request_id,omitempty"`
	ActorUserID   uint      `gorm:"not null;index:idx_admin_audit_actor_created,priority:1" json:"actor_user_id"`
	ActorRole     int       `gorm:"not null" json:"actor_role"`
	Action        string    `gorm:"type:varchar(128);not null;index" json:"action"`
	ResourceType  string    `gorm:"type:varchar(64);not null;index:idx_admin_audit_resource,priority:1" json:"resource_type"`
	ResourceID    string    `gorm:"type:varchar(128);not null;index:idx_admin_audit_resource,priority:2" json:"resource_id"`
	BeforeSummary string    `gorm:"type:text" json:"before_summary,omitempty"`
	AfterSummary  string    `gorm:"type:text" json:"after_summary,omitempty"`
	Result        string    `gorm:"type:varchar(32);not null;index" json:"result"`
	ErrorCode     string    `gorm:"type:varchar(128)" json:"error_code,omitempty"`
	IP            string    `gorm:"type:varchar(64)" json:"ip,omitempty"`
	UserAgent     string    `gorm:"type:varchar(256)" json:"user_agent,omitempty"`
	CreatedAt     time.Time `gorm:"not null;index:idx_admin_audit_actor_created,priority:2" json:"created_at"`
}
