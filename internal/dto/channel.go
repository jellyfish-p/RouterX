package dto

import (
	"encoding/json"
	"time"
)

// ChannelListRequest 通道列表查询
type ChannelListRequest struct {
	Page     int  `form:"page" binding:"omitempty,min=1"`
	PageSize int  `form:"page_size" binding:"omitempty,min=1,max=100"`
	Type     *int `form:"type"`   // 厂商类型筛选
	Status   *int `form:"status"` // 状态筛选
}

type ChannelUpstreamRequest struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

type ChannelUpstreamPublic struct {
	BaseURL   string `json:"base_url"`
	HasAPIKey bool   `json:"has_api_key"`
}

type ChannelInfo struct {
	ID               uint                    `json:"id"`
	Idx              int                     `json:"idx"`
	Type             int                     `json:"type"`
	Name             string                  `json:"name"`
	Models           string                  `json:"models"`
	BaseURL          string                  `json:"base_url"`
	BaseURLs         []string                `json:"base_urls,omitempty"`
	KeySelectionMode string                  `json:"key_selection_mode"`
	APIKeyCount      int                     `json:"api_key_count"`
	Upstreams        []ChannelUpstreamPublic `json:"upstreams,omitempty"`
	ModelRewrites    json.RawMessage         `json:"model_rewrites,omitempty"`
	Group            string                  `json:"group"`
	UpstreamOptions  json.RawMessage         `json:"upstream_options,omitempty"`
	Priority         int                     `json:"priority"`
	Weight           int                     `json:"weight"`
	Status           int                     `json:"status"`
	ResponseMs       int                     `json:"response_ms"`
	Balance          int64                   `json:"balance"`
	ErrorCount       int                     `json:"error_count"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at"`
}

// CreateChannelRequest 创建通道
type CreateChannelRequest struct {
	Idx              int                      `json:"idx"`
	Type             int                      `json:"type" binding:"required,min=1"`
	Name             string                   `json:"name" binding:"required,max=64"`
	Models           string                   `json:"models" binding:"required"`
	BaseURL          string                   `json:"base_url"`
	BaseURLs         []string                 `json:"base_urls"`
	APIKey           string                   `json:"api_key"`
	APIKeys          []string                 `json:"api_keys"`
	KeySelectionMode string                   `json:"key_selection_mode"`
	Upstreams        []ChannelUpstreamRequest `json:"upstreams"`
	ModelRewrites    json.RawMessage          `json:"model_rewrites"`
	Group            string                   `json:"group"`
	UpstreamOptions  json.RawMessage          `json:"upstream_options"`
	Priority         int                      `json:"priority"`
	Weight           int                      `json:"weight"`
	Status           int                      `json:"status"`
}

// UpdateChannelRequest 编辑通道
type UpdateChannelRequest struct {
	Idx              *int                      `json:"idx"`
	Type             *int                      `json:"type"`
	Name             *string                   `json:"name"`
	Models           *string                   `json:"models"`
	BaseURL          *string                   `json:"base_url"`
	BaseURLs         *[]string                 `json:"base_urls"`
	APIKey           *string                   `json:"api_key"`
	APIKeys          *[]string                 `json:"api_keys"`
	KeySelectionMode *string                   `json:"key_selection_mode"`
	Upstreams        *[]ChannelUpstreamRequest `json:"upstreams"`
	ModelRewrites    *json.RawMessage          `json:"model_rewrites"`
	Group            *string                   `json:"group"`
	UpstreamOptions  *json.RawMessage          `json:"upstream_options"`
	Priority         *int                      `json:"priority"`
	Weight           *int                      `json:"weight"`
	Status           *int                      `json:"status"`
}

// TestChannelResult 通道连通性测试结果
type TestChannelResult struct {
	Success    bool   `json:"success"`
	ResponseMs int64  `json:"response_ms"`
	Error      string `json:"error,omitempty"`
	ModelCount int    `json:"model_count,omitempty"`
}

type FetchChannelModelsResult struct {
	Models []string `json:"models"`
}
