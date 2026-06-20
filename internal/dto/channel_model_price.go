package dto

import (
	"time"

	"routerx/internal/model"
)

type ChannelModelPriceListRequest struct {
	Page        int    `form:"page"`
	PageSize    int    `form:"page_size"`
	Keyword     string `form:"keyword"`
	ChannelID   *uint  `form:"channel_id"`
	Enabled     *bool  `form:"enabled"`
	UserEnabled *bool  `form:"user_enabled"`
}

type UpsertChannelModelPriceRequest struct {
	ChannelID       uint            `json:"channel_id" binding:"required"`
	Model           string          `json:"model" binding:"required"`
	Enabled         *bool           `json:"enabled"`
	UserEnabled     *bool           `json:"user_enabled"`
	PriceMode       string          `json:"price_mode" binding:"required"`
	OverrideMode    string          `json:"override_mode"`
	PriceExpression string          `json:"price_expression" binding:"required"`
	VariablesJSON   model.JSONValue `json:"variables_json"`
	UnitTokens      int64           `json:"unit_tokens"`
}

type ChannelModelPriceAdminInfo struct {
	ID              uint            `json:"id"`
	ChannelID       uint            `json:"channel_id"`
	Model           string          `json:"model"`
	Enabled         bool            `json:"enabled"`
	UserEnabled     bool            `json:"user_enabled"`
	PriceMode       string          `json:"price_mode"`
	OverrideMode    string          `json:"override_mode"`
	PriceExpression string          `json:"price_expression"`
	VariablesJSON   model.JSONValue `json:"variables_json,omitempty"`
	UnitTokens      int64           `json:"unit_tokens"`
	RuleVersion     int64           `json:"rule_version"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func ChannelModelPriceAdminInfoFromModel(price *model.ChannelModelPrice) ChannelModelPriceAdminInfo {
	if price == nil {
		return ChannelModelPriceAdminInfo{}
	}
	return ChannelModelPriceAdminInfo{
		ID:              price.ID,
		ChannelID:       price.ChannelID,
		Model:           price.Model,
		Enabled:         price.Enabled,
		UserEnabled:     price.UserEnabled,
		PriceMode:       price.PriceMode,
		OverrideMode:    price.OverrideMode,
		PriceExpression: price.PriceExpression,
		VariablesJSON:   price.VariablesJSON,
		UnitTokens:      price.UnitTokens,
		RuleVersion:     price.RuleVersion,
		CreatedAt:       price.CreatedAt,
		UpdatedAt:       price.UpdatedAt,
	}
}

func ChannelModelPriceAdminInfosFromModels(prices []model.ChannelModelPrice) []ChannelModelPriceAdminInfo {
	items := make([]ChannelModelPriceAdminInfo, 0, len(prices))
	for i := range prices {
		items = append(items, ChannelModelPriceAdminInfoFromModel(&prices[i]))
	}
	return items
}
