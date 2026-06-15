package model

import (
	"time"
)

// Log 请求日志表。
// 记录每次 API 调用的完整上下游信息，用于审计与计费统计。
type Log struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	UserID           uint      `gorm:"not null;index" json:"user_id"`
	User             *User     `gorm:"foreignKey:UserID" json:"user,omitempty"`
	TokenID          *uint     `gorm:"index" json:"token_id"`
	Token            *Token    `gorm:"foreignKey:TokenID" json:"token,omitempty"`
	ChannelID        *uint     `gorm:"index" json:"channel_id"`
	Channel          *Channel  `gorm:"foreignKey:ChannelID" json:"channel,omitempty"`
	Model            string    `gorm:"type:varchar(128);not null" json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	QuotaUsed        int64     `gorm:"not null;default:0" json:"quota_used"` // 消耗额度 (分)
	TotalTokens      int       `json:"total_tokens"`
	Status           int       `gorm:"not null;default:0" json:"status"`    // 0=未知, 1=成功, 2=失败
	Content          string    `gorm:"type:text" json:"content,omitempty"`  // 请求体 (截断)
	Response         string    `gorm:"type:text" json:"response,omitempty"` // 响应体 (截断)
	ErrorMsg         string    `gorm:"type:text" json:"error_msg,omitempty"`
	ErrorCode        string    `gorm:"type:varchar(128);not null;default:''" json:"error_code,omitempty"`
	IP               string    `gorm:"type:varchar(64)" json:"ip"`
	UserAgent        string    `gorm:"-" json:"-"` // 仅用于生成 Token 上的脱敏 UA 摘要，不持久化原文。
	RequestID        string    `gorm:"type:varchar(128);index" json:"request_id,omitempty"`
	CreatedAt        time.Time `gorm:"not null;index" json:"created_at"`
}
