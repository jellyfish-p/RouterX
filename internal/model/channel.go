package model

import (
	"time"

	"gorm.io/gorm"
)

// Channel 下游大模型通道表。
// 存储对接的各厂商 API 配置，支持优先级与健康检查。
type Channel struct {
	ID               uint           `gorm:"primaryKey" json:"id"`
	Idx              int            `gorm:"not null;default:0;index" json:"idx"` // 前端排序序号, 值越小越靠前
	Type             int            `gorm:"not null;index" json:"type"`          // 上游类型: OpenAI/Claude/Gemini/xAI/RouterX...
	Name             string         `gorm:"type:varchar(64);not null" json:"name"`
	Models           string         `gorm:"type:text;not null" json:"models"`     // 逗号分隔模型列表, * 表示全部
	BaseURL          string         `gorm:"type:varchar(512)" json:"base_url"`    // 单上游地址, 为空时使用类型默认地址
	BaseURLs         JSONValue      `gorm:"type:json" json:"base_urls,omitempty"` // 多上游地址 JSON 数组
	APIKey           string         `gorm:"type:text" json:"-"`                   // 单密钥, JSON 序列化时隐藏
	APIKeys          JSONValue      `gorm:"type:json" json:"-"`                   // 多密钥 JSON 数组, 存储加密值
	KeySelectionMode string         `gorm:"type:varchar(16);not null;default:'round_robin'" json:"key_selection_mode"`
	KeyCursor        int            `gorm:"not null;default:0" json:"-"`               // 多密钥轮询游标
	Upstreams        JSONValue      `gorm:"type:json" json:"-"`                        // base_url/api_key 键值对数组, 存储加密 key
	ModelRewrites    JSONValue      `gorm:"type:json" json:"model_rewrites,omitempty"` // 模型名重写 JSON 对象
	ChannelGroup     string         `gorm:"column:channel_group;type:varchar(64);not null;default:''" json:"group"`
	UpstreamOptions  JSONValue      `gorm:"type:json" json:"upstream_options,omitempty"` // 适配器扩展配置
	Priority         int            `gorm:"not null;default:0;index" json:"priority"`    // 优先级, 值越大越优先
	Weight           int            `gorm:"not null;default:1" json:"weight"`            // 负载权重
	Status           int            `gorm:"not null;default:1;index" json:"status"`      // 0=禁用, 1=启用, 2=手动维护
	ResponseMs       int            `gorm:"not null;default:0" json:"response_ms"`       // 平均响应时间 (ms)
	Balance          int64          `gorm:"not null;default:0" json:"balance"`           // 余额 (分)
	ErrorCount       int            `gorm:"not null;default:0" json:"error_count"`       // 连续失败次数
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`
	Logs             []Log          `gorm:"foreignKey:ChannelID" json:"-"`
}
