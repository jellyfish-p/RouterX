package dto

import (
	"time"

	"routerx/internal/model"
)

type ModelPriceListRequest struct {
	Page     int    `form:"page"`
	PageSize int    `form:"page_size"`
	Keyword  string `form:"keyword"`
	Enabled  *bool  `form:"enabled"`
}

type UpsertModelPriceRequest struct {
	Model           string          `json:"model" binding:"required"`
	PriceMode       string          `json:"price_mode" binding:"required"`
	PriceExpression string          `json:"price_expression" binding:"required"`
	VariablesJSON   model.JSONValue `json:"variables_json"`
	UnitTokens      int64           `json:"unit_tokens"`
	Enabled         *bool           `json:"enabled"`
}

type ModelPriceAdminInfo struct {
	ID              uint            `json:"id"`
	Model           string          `json:"model"`
	PriceMode       string          `json:"price_mode"`
	PriceExpression string          `json:"price_expression"`
	VariablesJSON   model.JSONValue `json:"variables_json,omitempty"`
	UnitTokens      int64           `json:"unit_tokens"`
	RuleVersion     int64           `json:"rule_version"`
	Enabled         bool            `json:"enabled"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func ModelPriceAdminInfoFromModel(price *model.ModelPrice) ModelPriceAdminInfo {
	if price == nil {
		return ModelPriceAdminInfo{}
	}
	return ModelPriceAdminInfo{
		ID:              price.ID,
		Model:           price.Model,
		PriceMode:       price.PriceMode,
		PriceExpression: price.PriceExpression,
		VariablesJSON:   price.VariablesJSON,
		UnitTokens:      price.UnitTokens,
		RuleVersion:     price.RuleVersion,
		Enabled:         price.Enabled,
		CreatedAt:       price.CreatedAt,
		UpdatedAt:       price.UpdatedAt,
	}
}

func ModelPriceAdminInfosFromModels(prices []model.ModelPrice) []ModelPriceAdminInfo {
	items := make([]ModelPriceAdminInfo, 0, len(prices))
	for i := range prices {
		items = append(items, ModelPriceAdminInfoFromModel(&prices[i]))
	}
	return items
}
