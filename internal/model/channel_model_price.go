package model

import "time"

// ChannelModelPrice 是通道级模型价格覆盖。
// 它只描述某个通道下某个模型的价格和普通用户可见性，优先级高于系统模型价格。
type ChannelModelPrice struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	ChannelID       uint      `gorm:"not null;uniqueIndex:idx_channel_model_prices_channel_model,priority:1;index" json:"channel_id"`
	Channel         *Channel  `gorm:"foreignKey:ChannelID" json:"channel,omitempty"`
	Model           string    `gorm:"type:varchar(128);not null;uniqueIndex:idx_channel_model_prices_channel_model,priority:2" json:"model"`
	Enabled         bool      `gorm:"not null;index" json:"enabled"`
	UserEnabled     bool      `gorm:"not null;index" json:"user_enabled"`
	PriceMode       string    `gorm:"type:varchar(32);not null" json:"price_mode"`
	OverrideMode    string    `gorm:"type:varchar(32);not null;default:override" json:"override_mode"`
	PriceExpression string    `gorm:"type:text;not null" json:"price_expression"`
	VariablesJSON   JSONValue `gorm:"type:json" json:"variables_json,omitempty"`
	UnitTokens      int64     `gorm:"not null;default:1000" json:"unit_tokens"`
	RuleVersion     int64     `gorm:"not null;default:1" json:"rule_version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
