package dto

import (
	"time"

	"routerx/internal/model"
)

// UserIdentityBrief is the safe, credential-free shape returned to users.
type UserIdentityBrief struct {
	ID         uint       `json:"id"`
	Method     string     `json:"method"`
	Provider   string     `json:"provider"`
	Identifier string     `json:"identifier"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func UserIdentityBriefFromModel(identity *model.UserIdentity) UserIdentityBrief {
	if identity == nil {
		return UserIdentityBrief{}
	}
	return UserIdentityBrief{
		ID:         identity.ID,
		Method:     identity.Method,
		Provider:   identity.Provider,
		Identifier: identity.Identifier,
		VerifiedAt: identity.VerifiedAt,
		LastUsedAt: identity.LastUsedAt,
		CreatedAt:  identity.CreatedAt,
	}
}

func UserIdentityBriefsFromModels(identities []model.UserIdentity) []UserIdentityBrief {
	items := make([]UserIdentityBrief, 0, len(identities))
	for i := range identities {
		items = append(items, UserIdentityBriefFromModel(&identities[i]))
	}
	return items
}
