package service

import (
	"errors"
	"testing"
	"time"

	"routerx/internal/common"
	"routerx/internal/model"
)

func TestValidateAndGetTokenRejectsExpirationBoundary(t *testing.T) {
	db := newLogServiceTestDB(t, "token-expiration-boundary")
	withMainDB(t, db)

	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	oldTokenNow := tokenNow
	tokenNow = func() time.Time { return now }
	t.Cleanup(func() { tokenNow = oldTokenNow })

	username := "boundary-user"
	user := model.User{
		Username: &username,
		Role:     common.RoleUser,
		Status:   common.UserStatusEnabled,
		Quota:    100,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	key := "sk-expiration-boundary"
	token := model.Token{
		UserID:      user.ID,
		Name:        "boundary-key",
		Key:         common.SHA256Hex(key),
		Status:      common.TokenStatusEnabled,
		ExpiredAt:   &now,
		RemainQuota: 100,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}

	_, err := NewTokenService().ValidateAndGetToken(key)
	if !errors.Is(err, ErrAPIKeyExpired) {
		t.Fatalf("api key should expire when current time reaches expired_at, got %v", err)
	}
}
