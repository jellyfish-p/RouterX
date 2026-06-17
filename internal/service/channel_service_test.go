package service

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
)

func TestChannelCandidateCacheUsesVersionInvalidation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:channel_service_cache_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Channel{}, &model.Setting{}); err != nil {
		t.Fatal(err)
	}
	oldDB, oldRDB := internal.DB, internal.RDB
	internal.DB = db
	internal.RDB = nil
	t.Cleanup(func() {
		internal.DB = oldDB
		internal.RDB = oldRDB
	})

	if err := db.Create([]model.Setting{
		{Key: "routing.channel_cache.enabled", Value: "true", Category: "routing"},
		{Key: "routing.channel_cache.ttl_seconds", Value: "60", Category: "routing"},
		{Key: "routing.channel_cache.version", Value: "1", Category: "routing"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Channel{
		Type:     common.ChannelTypeOpenAICompat,
		Name:     "stable",
		Models:   "gpt-cache",
		BaseURL:  "http://stable.example",
		APIKey:   "stable-key",
		Priority: 1,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewChannelService()
	first, _, err := svc.SelectChannelCandidatesWithRouteFacts("gpt-cache", RoutePreference{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Name != "stable" {
		t.Fatalf("initial candidates should include stable channel only, got %+v", first)
	}

	if err := db.Create(&model.Channel{
		Type:     common.ChannelTypeOpenAICompat,
		Name:     "newer",
		Models:   "gpt-cache",
		BaseURL:  "http://newer.example",
		APIKey:   "newer-key",
		Priority: 99,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	cached, _, err := svc.SelectChannelCandidatesWithRouteFacts("gpt-cache", RoutePreference{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) != 1 || cached[0].Name != "stable" {
		t.Fatalf("unchanged cache version should reuse cached candidates, got %+v", cached)
	}

	if err := NewSettingService().Set("routing.channel_cache.version", "2"); err != nil {
		t.Fatal(err)
	}
	refreshed, _, err := svc.SelectChannelCandidatesWithRouteFacts("gpt-cache", RoutePreference{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed) != 2 || refreshed[0].Name != "newer" || refreshed[1].Name != "stable" {
		t.Fatalf("version bump should reload ordered candidates, got %+v", refreshed)
	}
}
