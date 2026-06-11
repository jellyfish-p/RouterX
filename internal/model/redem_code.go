package model

import (
	"time"
)

// RedemCode 充值码表。
// 管理员批量生成充值码，用户可通过兑换码给自己的账户充值。
type RedemCode struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Code      string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"code"` // 充值码
	Quota     int64     `gorm:"not null" json:"quota"`                              // 额度 (分)
	Status    int       `gorm:"not null;default:0" json:"status"`                   // 0=未使用, 1=已使用
	UsedBy    *uint     `json:"used_by"`
	UsedUser  *User     `gorm:"foreignKey:UsedBy" json:"used_user,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UsedAt    *time.Time `json:"used_at"`
}
