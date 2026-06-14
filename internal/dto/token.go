package dto

import (
	"encoding/json"
	"time"

	"routerx/internal/model"
)

type TokenListRequest struct {
	Page     int `form:"page" binding:"omitempty,min=1"`
	PageSize int `form:"page_size" binding:"omitempty,min=1,max=100"`
}

type CreateTokenRequest struct {
	Name        string `json:"name" binding:"required,max=64"`
	RemainQuota int64  `json:"remain_quota"`
	Unlimited   bool   `json:"unlimited"`
	ExpiredAt   *int64 `json:"expired_at"`
}

type UpdateTokenRequest struct {
	Name        *string `json:"name"`
	Status      *int    `json:"status"`
	RemainQuota *int64  `json:"remain_quota"`
	Unlimited   *bool   `json:"unlimited"`
	ExpiredAt   *int64  `json:"expired_at"`
}

type ReportTokenLeakRequest struct {
	Reason string `json:"reason"`
}

type BatchDisableTokensRequest struct {
	TokenIDs []uint `json:"token_ids"`
	UserID   *uint  `json:"user_id"`
	Reason   string `json:"reason"`
}

type UpdateTokenScopeRequest struct {
	AllowModels   []string `json:"allow_models"`
	APITypes      []string `json:"api_types"`
	ChannelGroups []string `json:"channel_groups"`
	IPCIDRs       []string `json:"ip_cidrs"`
}

type TokenScopeResponse struct {
	AllowModels   []string `json:"allow_models,omitempty"`
	APITypes      []string `json:"api_types,omitempty"`
	ChannelGroups []string `json:"channel_groups,omitempty"`
	IPCIDRs       []string `json:"ip_cidrs,omitempty"`
}

type TokenResponse struct {
	ID            uint                `json:"id"`
	UserID        uint                `json:"user_id"`
	Name          string              `json:"name"`
	Status        int                 `json:"status"`
	ExpiredAt     *time.Time          `json:"expired_at,omitempty"`
	RemainQuota   int64               `json:"remain_quota"`
	Unlimited     bool                `json:"unlimited"`
	RotatedFromID *uint               `json:"rotated_from_id,omitempty"`
	RevokedReason string              `json:"revoked_reason,omitempty"`
	Scope         *TokenScopeResponse `json:"scope,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

type CreateTokenResponse struct {
	TokenResponse
	Key string `json:"key"`
}

type TokenUsageResponse struct {
	TokenID      uint       `json:"token_id"`
	CallCount    int64      `json:"call_count"`
	SuccessCount int64      `json:"success_count"`
	ErrorCount   int64      `json:"error_count"`
	TotalQuota   int64      `json:"total_quota"`
	TotalTokens  int64      `json:"total_tokens"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
	LastModel    string     `json:"last_model,omitempty"`
	LastStatus   int        `json:"last_status,omitempty"`
	LastErrorMsg string     `json:"last_error_msg,omitempty"`
}

type ReportTokenLeakResponse struct {
	ID                     uint   `json:"id"`
	Status                 int    `json:"status"`
	RevokedReason          string `json:"revoked_reason"`
	ReplacementRecommended bool   `json:"replacement_recommended"`
}

type BatchDisableTokensResponse struct {
	MatchedCount  int64  `json:"matched_count"`
	DisabledCount int64  `json:"disabled_count"`
	Reason        string `json:"reason"`
	TokenIDs      []uint `json:"token_ids"`
}

func TokenFromModel(token model.Token) TokenResponse {
	return TokenResponse{
		ID:            token.ID,
		UserID:        token.UserID,
		Name:          token.Name,
		Status:        token.Status,
		ExpiredAt:     token.ExpiredAt,
		RemainQuota:   token.RemainQuota,
		Unlimited:     token.Unlimited,
		RotatedFromID: token.RotatedFromID,
		RevokedReason: token.RevokedReason,
		Scope:         TokenScopeFromJSON(token.ScopeJSON),
		CreatedAt:     token.CreatedAt,
		UpdatedAt:     token.UpdatedAt,
	}
}

func TokenScopeFromJSON(raw model.JSONValue) *TokenScopeResponse {
	if len(raw) == 0 {
		return nil
	}
	var scope TokenScopeResponse
	if err := json.Unmarshal(raw, &scope); err != nil {
		return nil
	}
	if len(scope.AllowModels) == 0 && len(scope.APITypes) == 0 && len(scope.ChannelGroups) == 0 && len(scope.IPCIDRs) == 0 {
		return nil
	}
	return &scope
}
