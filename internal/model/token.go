package model

import (
	"time"

	"gorm.io/gorm"
)

// Token API 令牌表。
// 用户通过 Bearer Token 调用 /v1/* 接口时鉴权。
type Token struct {
	ID                uint           `gorm:"primaryKey" json:"id"`
	UserID            uint           `gorm:"not null;index" json:"user_id"`
	User              *User          `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Name              string         `gorm:"type:varchar(64);not null" json:"name"`          // 令牌备注名
	Key               string         `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"` // SHA256(sk-xxxxxx)
	Status            int            `gorm:"not null;default:1" json:"status"`               // 0=禁用, 1=启用
	ExpiredAt         *time.Time     `json:"expired_at"`                                     // 过期时间, nil=永不过期
	RemainQuota       int64          `gorm:"not null;default:0" json:"remain_quota"`         // 剩余额度, -1=无限制
	Unlimited         bool           `gorm:"not null;default:false" json:"unlimited"`
	RotatedFromID     *uint          `gorm:"index" json:"rotated_from_id,omitempty"` // 轮换来源 Token ID
	RevokedReason     string         `gorm:"type:varchar(128);not null;default:''" json:"revoked_reason,omitempty"`
	ScopeJSON         JSONValue      `gorm:"type:json" json:"scope_json,omitempty"`                                      // API Key 能力范围, 例如模型 allow-list
	LastUsedAt        *time.Time     `gorm:"index" json:"last_used_at,omitempty"`                                        // 最近一次成功或失败调用时间
	LastUsedIPHash    string         `gorm:"type:varchar(64);not null;default:''" json:"last_used_ip_hash,omitempty"`    // 最近来源 IP 的 SHA-256 摘要
	LastUserAgentHash string         `gorm:"type:varchar(64);not null;default:''" json:"last_user_agent_hash,omitempty"` // 最近 User-Agent 的 SHA-256 摘要
	LastModel         string         `gorm:"type:varchar(128);not null;default:''" json:"last_model,omitempty"`          // 最近请求模型
	LastErrorCode     string         `gorm:"type:varchar(64);not null;default:''" json:"last_error_code,omitempty"`      // 最近失败的协议化错误 code
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"-"`
	Logs              []Log          `gorm:"foreignKey:TokenID" json:"-"`
}
