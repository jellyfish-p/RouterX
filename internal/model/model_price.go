package model

import "time"

// ModelPrice 是系统级模型价格表。
// 现阶段它负责告诉用户侧价格规则是否已准备好，后续计费表达式会复用这里的版本化规则。
type ModelPrice struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Model           string    `gorm:"type:varchar(128);not null;uniqueIndex" json:"model"`
	PriceMode       string    `gorm:"type:varchar(32);not null" json:"price_mode"`
	PriceExpression string    `gorm:"type:text;not null" json:"price_expression"`
	VariablesJSON   JSONValue `gorm:"type:json" json:"variables_json,omitempty"`
	UnitTokens      int64     `gorm:"not null;default:1000" json:"unit_tokens"`
	RuleVersion     int64     `gorm:"not null;default:1" json:"rule_version"`
	Enabled         bool      `gorm:"not null;default:true;index" json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
