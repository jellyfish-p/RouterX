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
	Name        string                `json:"name" binding:"required,max=64"`
	RemainQuota int64                 `json:"remain_quota"`
	Unlimited   bool                  `json:"unlimited"`
	ExpiredAt   *int64                `json:"expired_at"`
	Metadata    *TokenMetadataRequest `json:"metadata"`
}

type UpdateTokenRequest struct {
	Name        *string               `json:"name"`
	Status      *int                  `json:"status"`
	RemainQuota *int64                `json:"remain_quota"`
	Unlimited   *bool                 `json:"unlimited"`
	ExpiredAt   *int64                `json:"expired_at"`
	Metadata    *TokenMetadataRequest `json:"metadata"`
}

type ReportTokenLeakRequest struct {
	Reason string `json:"reason"`
}

type BatchDisableTokensRequest struct {
	TokenIDs []uint `json:"token_ids"`
	UserID   *uint  `json:"user_id"`
	Reason   string `json:"reason"`
}

type BatchExpireTokensRequest struct {
	TokenIDs []uint `json:"token_ids"`
	UserID   *uint  `json:"user_id"`
	Reason   string `json:"reason"`
}

type UpdateTokenScopeRequest struct {
	AllowModels    []string `json:"allow_models"`
	APITypes       []string `json:"api_types"`
	ChannelGroups  []string `json:"channel_groups"`
	EntryProtocols []string `json:"entry_protocols"`
	IPCIDRs        []string `json:"ip_cidrs"`
	Methods        []string `json:"methods"`
	DailyQuota     *int64   `json:"daily_quota"`
	MonthlyQuota   *int64   `json:"monthly_quota"`
	MaxConcurrency *int64   `json:"max_concurrency"`
	RPM            *int64   `json:"rpm"`
	TPM            *int64   `json:"tpm"`
}

type TokenScopeResponse struct {
	AllowModels    []string `json:"allow_models,omitempty"`
	APITypes       []string `json:"api_types,omitempty"`
	ChannelGroups  []string `json:"channel_groups,omitempty"`
	EntryProtocols []string `json:"entry_protocols,omitempty"`
	IPCIDRs        []string `json:"ip_cidrs,omitempty"`
	Methods        []string `json:"methods,omitempty"`
	DailyQuota     *int64   `json:"daily_quota,omitempty"`
	MonthlyQuota   *int64   `json:"monthly_quota,omitempty"`
	MaxConcurrency *int64   `json:"max_concurrency,omitempty"`
	RPM            *int64   `json:"rpm,omitempty"`
	TPM            *int64   `json:"tpm,omitempty"`
}

type TokenMetadataRequest struct {
	Environment   string   `json:"environment"`
	Team          string   `json:"team"`
	App           string   `json:"app"`
	Tags          []string `json:"tags"`
	ExternalID    string   `json:"external_id"`
	Note          string   `json:"note"`
	PrincipalType string   `json:"principal_type"`
	PrincipalID   string   `json:"principal_id"`
	PrincipalName string   `json:"principal_name"`
}

type TokenMetadataResponse struct {
	Environment   string   `json:"environment,omitempty"`
	Team          string   `json:"team,omitempty"`
	App           string   `json:"app,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	ExternalID    string   `json:"external_id,omitempty"`
	Note          string   `json:"note,omitempty"`
	PrincipalType string   `json:"principal_type,omitempty"`
	PrincipalID   string   `json:"principal_id,omitempty"`
	PrincipalName string   `json:"principal_name,omitempty"`
}

type TokenResponse struct {
	ID                uint                   `json:"id"`
	UserID            uint                   `json:"user_id"`
	Name              string                 `json:"name"`
	Status            int                    `json:"status"`
	ExpiredAt         *time.Time             `json:"expired_at,omitempty"`
	RemainQuota       int64                  `json:"remain_quota"`
	Unlimited         bool                   `json:"unlimited"`
	RotatedFromID     *uint                  `json:"rotated_from_id,omitempty"`
	RevokedReason     string                 `json:"revoked_reason,omitempty"`
	Scope             *TokenScopeResponse    `json:"scope,omitempty"`
	Metadata          *TokenMetadataResponse `json:"metadata,omitempty"`
	LastUsedAt        *time.Time             `json:"last_used_at,omitempty"`
	LastUsedIPHash    string                 `json:"last_used_ip_hash,omitempty"`
	LastUserAgentHash string                 `json:"last_user_agent_hash,omitempty"`
	LastModel         string                 `json:"last_model,omitempty"`
	LastErrorCode     string                 `json:"last_error_code,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
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

type TokenRiskResponse struct {
	Token               TokenResponse `json:"token"`
	CallCount           int64         `json:"call_count"`
	SuccessCount        int64         `json:"success_count"`
	ErrorCount          int64         `json:"error_count"`
	TotalQuota          int64         `json:"total_quota"`
	TotalTokens         int64         `json:"total_tokens"`
	LastUsedAt          *time.Time    `json:"last_used_at,omitempty"`
	LastModel           string        `json:"last_model,omitempty"`
	LastStatus          int           `json:"last_status,omitempty"`
	LastErrorCode       string        `json:"last_error_code,omitempty"`
	RiskLevel           string        `json:"risk_level"`
	RiskReasons         []string      `json:"risk_reasons"`
	RecommendedAction   string        `json:"recommended_action"`
	RotationRecommended bool          `json:"rotation_recommended"`
	RotationReason      string        `json:"rotation_reason,omitempty"`
	WindowStart         time.Time     `json:"window_start"`
}

type TokenLeakWindowCounterResponse struct {
	Value      string     `json:"value"`
	Count      int64      `json:"count"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

type TokenEventCounterResponse struct {
	Value      string     `json:"value"`
	Count      int64      `json:"count"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

type TokenLeakWindowResponse struct {
	Token             TokenResponse                    `json:"token"`
	TokenID           uint                             `json:"token_id"`
	WindowHours       int                              `json:"window_hours"`
	WindowStart       time.Time                        `json:"window_start"`
	WindowEnd         time.Time                        `json:"window_end"`
	CallCount         int64                            `json:"call_count"`
	SuccessCount      int64                            `json:"success_count"`
	ErrorCount        int64                            `json:"error_count"`
	TotalQuota        int64                            `json:"total_quota"`
	TotalTokens       int64                            `json:"total_tokens"`
	FirstUsedAt       *time.Time                       `json:"first_used_at,omitempty"`
	LastUsedAt        *time.Time                       `json:"last_used_at,omitempty"`
	Models            []TokenLeakWindowCounterResponse `json:"models"`
	ErrorCodes        []TokenLeakWindowCounterResponse `json:"error_codes"`
	SourceIPHashes    []TokenLeakWindowCounterResponse `json:"source_ip_hashes"`
	LastUsedIPHash    string                           `json:"last_used_ip_hash,omitempty"`
	LastUserAgentHash string                           `json:"last_user_agent_hash,omitempty"`
}

type TokenEventWindowResponse struct {
	Token               TokenResponse               `json:"token"`
	TokenID             uint                        `json:"token_id"`
	WindowHours         int                         `json:"window_hours"`
	WindowStart         time.Time                   `json:"window_start"`
	WindowEnd           time.Time                   `json:"window_end"`
	EventCount          int64                       `json:"event_count"`
	ErrorCount          int64                       `json:"error_count"`
	RateLimitCount      int64                       `json:"rate_limit_count"`
	FirstSeenAt         *time.Time                  `json:"first_seen_at,omitempty"`
	LastSeenAt          *time.Time                  `json:"last_seen_at,omitempty"`
	ErrorCodes          []TokenEventCounterResponse `json:"error_codes"`
	ErrorSources        []TokenEventCounterResponse `json:"error_sources"`
	UpstreamStatuses    []TokenEventCounterResponse `json:"upstream_statuses"`
	RateLimitDimensions []TokenEventCounterResponse `json:"rate_limit_dimensions"`
	Models              []TokenEventCounterResponse `json:"models"`
	LastUsedIPHash      string                      `json:"last_used_ip_hash,omitempty"`
	LastUserAgentHash   string                      `json:"last_user_agent_hash,omitempty"`
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

type BatchExpireTokensResponse struct {
	MatchedCount int64     `json:"matched_count"`
	ExpiredCount int64     `json:"expired_count"`
	Reason       string    `json:"reason"`
	ExpiredAt    time.Time `json:"expired_at"`
	TokenIDs     []uint    `json:"token_ids"`
}

func TokenFromModel(token model.Token) TokenResponse {
	return TokenResponse{
		ID:                token.ID,
		UserID:            token.UserID,
		Name:              token.Name,
		Status:            token.Status,
		ExpiredAt:         token.ExpiredAt,
		RemainQuota:       token.RemainQuota,
		Unlimited:         token.Unlimited,
		RotatedFromID:     token.RotatedFromID,
		RevokedReason:     token.RevokedReason,
		Scope:             TokenScopeFromJSON(token.ScopeJSON),
		Metadata:          TokenMetadataFromJSON(token.MetadataJSON),
		LastUsedAt:        token.LastUsedAt,
		LastUsedIPHash:    token.LastUsedIPHash,
		LastUserAgentHash: token.LastUserAgentHash,
		LastModel:         token.LastModel,
		LastErrorCode:     token.LastErrorCode,
		CreatedAt:         token.CreatedAt,
		UpdatedAt:         token.UpdatedAt,
	}
}

func TokenMetadataFromJSON(raw model.JSONValue) *TokenMetadataResponse {
	if len(raw) == 0 {
		return nil
	}
	var metadata TokenMetadataResponse
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil
	}
	if metadata.Environment == "" && metadata.Team == "" && metadata.App == "" && len(metadata.Tags) == 0 &&
		metadata.ExternalID == "" && metadata.Note == "" && metadata.PrincipalType == "" &&
		metadata.PrincipalID == "" && metadata.PrincipalName == "" {
		return nil
	}
	return &metadata
}

func TokenScopeFromJSON(raw model.JSONValue) *TokenScopeResponse {
	if len(raw) == 0 {
		return nil
	}
	var scope TokenScopeResponse
	if err := json.Unmarshal(raw, &scope); err != nil {
		return nil
	}
	if len(scope.AllowModels) == 0 && len(scope.APITypes) == 0 && len(scope.ChannelGroups) == 0 && len(scope.EntryProtocols) == 0 && len(scope.IPCIDRs) == 0 && len(scope.Methods) == 0 && scope.DailyQuota == nil && scope.MonthlyQuota == nil && scope.MaxConcurrency == nil && scope.RPM == nil && scope.TPM == nil {
		return nil
	}
	return &scope
}
