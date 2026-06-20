package model

import "time"

const (
	AlertTypeAPIKeyLeakReported = "api_key.leak_reported"

	AlertSeverityCritical = "critical"
	AlertSeverityWarning  = "warning"
	AlertSeverityInfo     = "info"

	AlertStatusOpen         = "open"
	AlertStatusAcknowledged = "acknowledged"
)

// AlertEvent 是运维可处理的告警收件箱事实。
// 告警只保存脱敏上下文，避免把 API Key 明文、下游密钥或用户提交的可疑原文写入库。
type AlertEvent struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Type          string     `gorm:"type:varchar(128);not null;index" json:"type"`
	Severity      string     `gorm:"type:varchar(32);not null;default:'warning';index" json:"severity"`
	Status        string     `gorm:"type:varchar(32);not null;default:'open';index" json:"status"`
	ResourceType  string     `gorm:"type:varchar(64);not null;index:idx_alert_resource,priority:1" json:"resource_type"`
	ResourceID    string     `gorm:"type:varchar(128);not null;index:idx_alert_resource,priority:2" json:"resource_id"`
	UserID        *uint      `gorm:"index" json:"user_id,omitempty"`
	TokenID       *uint      `gorm:"index" json:"token_id,omitempty"`
	Title         string     `gorm:"type:varchar(160);not null" json:"title"`
	Message       string     `gorm:"type:text;not null" json:"message"`
	DetailsJSON   JSONValue  `gorm:"type:json" json:"details_json,omitempty"`
	AckedAt       *time.Time `json:"acked_at,omitempty"`
	AckedByUserID *uint      `gorm:"index" json:"acked_by_user_id,omitempty"`
	CreatedAt     time.Time  `gorm:"not null;index" json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
