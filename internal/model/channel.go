package model

import (
	"time"

	"gorm.io/gorm"
)

// Channel 下游大模型通道表。
// 存储对接的各厂商 API 配置，支持优先级与健康检查。
type Channel struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	Type        int            `gorm:"not null" json:"type"`                          // 厂商类型: 1=OpenAI, 2=Azure, ...
	Name        string         `gorm:"type:varchar(64);not null" json:"name"`
	Models      string         `gorm:"type:text;not null" json:"models"`               // 逗号分隔模型列表, * 表示全部
	BaseURL     string         `gorm:"type:varchar(256)" json:"base_url"`             // API 地址
	APIKey      string         `gorm:"type:text;not null" json:"-"`                   // 下游 API Key (JSON 序列化时隐藏)
	Priority    int            `gorm:"not null;default:0" json:"priority"`            // 优先级, 值越大越优先
	Weight      int            `gorm:"not null;default:1" json:"weight"`              // 负载权重
	Status      int            `gorm:"not null;default:1" json:"status"`              // 0=禁用, 1=启用, 2=手动维护
	ResponseMs  int            `gorm:"not null;default:0" json:"response_ms"`         // 平均响应时间 (ms)
	Balance     int64          `gorm:"not null;default:0" json:"balance"`             // 余额 (分)
	ErrorCount  int            `gorm:"not null;default:0" json:"error_count"`         // 连续失败次数
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
	Logs        []Log          `gorm:"foreignKey:ChannelID" json:"-"`
}
