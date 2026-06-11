package dto

import (
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

type TokenResponse struct {
	ID          uint       `json:"id"`
	UserID      uint       `json:"user_id"`
	Name        string     `json:"name"`
	Status      int        `json:"status"`
	ExpiredAt   *time.Time `json:"expired_at,omitempty"`
	RemainQuota int64      `json:"remain_quota"`
	Unlimited   bool       `json:"unlimited"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type CreateTokenResponse struct {
	TokenResponse
	Key string `json:"key"`
}

func TokenFromModel(token model.Token) TokenResponse {
	return TokenResponse{
		ID:          token.ID,
		UserID:      token.UserID,
		Name:        token.Name,
		Status:      token.Status,
		ExpiredAt:   token.ExpiredAt,
		RemainQuota: token.RemainQuota,
		Unlimited:   token.Unlimited,
		CreatedAt:   token.CreatedAt,
		UpdatedAt:   token.UpdatedAt,
	}
}
