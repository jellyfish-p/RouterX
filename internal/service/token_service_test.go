package service

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"routerx/internal"
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

func TestValidateAndGetTokenResolvesFromRedisAuthCache(t *testing.T) {
	db := newLogServiceTestDB(t, "token-auth-cache-resolve")
	withMainDB(t, db)
	redisServer := newFakeRedisServer(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), Protocol: 2, DisableIdentity: true})
	oldRDB := internal.RDB
	internal.RDB = rdb
	t.Cleanup(func() {
		_ = rdb.Close()
		internal.RDB = oldRDB
	})

	username := "cached-user"
	user := model.User{Username: &username, Role: common.RoleUser, Status: common.UserStatusEnabled, Quota: 100}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserID: user.ID, Name: "cached-key", Key: common.SHA256Hex("sk-real-key"), Status: common.TokenStatusEnabled, RemainQuota: 100}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}

	redisServer.SetString(apiKeyAuthCacheKey(common.SHA256Hex("sk-cache-only")), strconv.FormatUint(uint64(token.ID), 10))

	got, err := NewTokenService().ValidateAndGetToken("sk-cache-only")
	if err != nil || got.ID != token.ID || got.User == nil || got.User.ID != user.ID {
		t.Fatalf("cached api key hash should resolve token id through Redis, token=%+v err=%v", got, err)
	}
}

func TestAPIKeyAuthCacheWarmsAndClearsOnDisable(t *testing.T) {
	db := newLogServiceTestDB(t, "token-auth-cache-disable")
	withMainDB(t, db)
	redisServer := newFakeRedisServer(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), Protocol: 2, DisableIdentity: true})
	oldRDB := internal.RDB
	internal.RDB = rdb
	t.Cleanup(func() {
		_ = rdb.Close()
		internal.RDB = oldRDB
	})

	username := "disable-cache-user"
	user := model.User{Username: &username, Role: common.RoleUser, Status: common.UserStatusEnabled, Quota: 100}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	key := "sk-disable-cache"
	token := model.Token{UserID: user.ID, Name: "disable-cache-key", Key: common.SHA256Hex(key), Status: common.TokenStatusEnabled, RemainQuota: 100}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := NewTokenService().ValidateAndGetToken(key); err != nil {
		t.Fatal(err)
	}
	cacheKey := apiKeyAuthCacheKey(common.SHA256Hex(key))
	if cached, ok := redisServer.StringValue(cacheKey); !ok || cached != strconv.FormatUint(uint64(token.ID), 10) {
		t.Fatalf("validation should warm auth cache, ok=%v value=%q", ok, cached)
	}

	if _, err := NewTokenService().DisableForUser(token.ID, user.ID, "test_disable"); err != nil {
		t.Fatal(err)
	}
	if cached, ok := redisServer.StringValue(cacheKey); ok {
		t.Fatalf("disable should clear auth cache, value=%q", cached)
	}
}
