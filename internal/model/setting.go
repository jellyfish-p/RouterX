package model

import (
	"time"
)

// Setting 系统设置表。
// 全量配置存库，零配置文件。通过 Admin 管理端读写。
type Setting struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Key         string    `gorm:"type:varchar(128);not null;uniqueIndex" json:"key"` // 配置键
	Value       string    `gorm:"type:text;not null" json:"value"`                    // 配置值 (JSON 或标量)
	Category    string    `gorm:"type:varchar(64);not null;default:'general'" json:"category"` // 分类
	Description string    `gorm:"type:varchar(256);not null;default:''" json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
