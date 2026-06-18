package service

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestChannelCandidateCachePreloadWarmsCache(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:channel_service_preload_warm_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
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
		{Key: "routing.channel_cache.preload", Value: "true", Category: "routing"},
		{Key: "routing.channel_cache.ttl_seconds", Value: "60", Category: "routing"},
		{Key: "routing.channel_cache.version", Value: "1", Category: "routing"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Channel{
		Type:     common.ChannelTypeOpenAICompat,
		Name:     "preloaded",
		Models:   "gpt-preload",
		BaseURL:  "http://preloaded.example",
		APIKey:   "preloaded-key",
		Priority: 1,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewChannelService()
	if err := svc.PreloadCandidateCache(); err != nil {
		t.Fatalf("preload should succeed: %v", err)
	}
	if err := db.Create(&model.Channel{
		Type:     common.ChannelTypeOpenAICompat,
		Name:     "created-after-preload",
		Models:   "gpt-preload",
		BaseURL:  "http://created-after-preload.example",
		APIKey:   "created-after-preload-key",
		Priority: 99,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	cached, _, err := svc.SelectChannelCandidatesWithRouteFacts("gpt-preload", RoutePreference{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) != 1 || cached[0].Name != "preloaded" {
		t.Fatalf("preload should warm cache before later DB changes, got %+v", cached)
	}
}

func TestChannelCandidateCachePreloadWarmsAfterChannelChange(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:channel_service_preload_after_change_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
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
		{Key: "routing.channel_cache.preload", Value: "true", Category: "routing"},
		{Key: "routing.channel_cache.ttl_seconds", Value: "60", Category: "routing"},
		{Key: "routing.channel_cache.version", Value: "1", Category: "routing"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Channel{
		Type:     common.ChannelTypeOpenAICompat,
		Name:     "initial",
		Models:   "gpt-preload-change",
		BaseURL:  "http://initial.example",
		APIKey:   "initial-key",
		Priority: 1,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewChannelService()
	if err := svc.PreloadCandidateCache(); err != nil {
		t.Fatalf("preload should succeed: %v", err)
	}
	if err := svc.Create(&model.Channel{
		Type:     common.ChannelTypeOpenAICompat,
		Name:     "created-through-service",
		Models:   "gpt-preload-change",
		BaseURL:  "http://created-through-service.example",
		APIKey:   "created-through-service-key",
		Priority: 50,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}); err != nil {
		t.Fatalf("channel create should succeed: %v", err)
	}
	if err := db.Create(&model.Channel{
		Type:     common.ChannelTypeOpenAICompat,
		Name:     "created-after-service-change",
		Models:   "gpt-preload-change",
		BaseURL:  "http://created-after-service-change.example",
		APIKey:   "created-after-service-change-key",
		Priority: 99,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	candidates, _, err := svc.SelectChannelCandidatesWithRouteFacts("gpt-preload-change", RoutePreference{})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].Name != "created-through-service" || candidates[1].Name != "initial" {
		t.Fatalf("channel changes should warm cache before later DB changes, got %+v", candidates)
	}
}

func TestChannelCandidateCachePreloadSkipsWhenDisabled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:channel_service_preload_disabled_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
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
		{Key: "routing.channel_cache.preload", Value: "false", Category: "routing"},
		{Key: "routing.channel_cache.ttl_seconds", Value: "60", Category: "routing"},
		{Key: "routing.channel_cache.version", Value: "1", Category: "routing"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Channel{
		Type:     common.ChannelTypeOpenAICompat,
		Name:     "initial",
		Models:   "gpt-preload-disabled",
		BaseURL:  "http://initial.example",
		APIKey:   "initial-key",
		Priority: 1,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewChannelService()
	if err := svc.PreloadCandidateCache(); err != nil {
		t.Fatalf("disabled preload should be a no-op without error: %v", err)
	}
	if err := db.Create(&model.Channel{
		Type:     common.ChannelTypeOpenAICompat,
		Name:     "visible-without-preload",
		Models:   "gpt-preload-disabled",
		BaseURL:  "http://visible-without-preload.example",
		APIKey:   "visible-without-preload-key",
		Priority: 99,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	candidates, _, err := svc.SelectChannelCandidatesWithRouteFacts("gpt-preload-disabled", RoutePreference{})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].Name != "visible-without-preload" || candidates[1].Name != "initial" {
		t.Fatalf("disabled preload should leave first selection to load fresh DB state, got %+v", candidates)
	}
}

func TestChannelBreakerCooldownAllowsProbeAfterWindow(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:channel_service_breaker_cooldown_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
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
		{Key: "routing.channel_cache.enabled", Value: "false", Category: "routing"},
		{Key: "relay.error_auto_ban", Value: "true", Category: "relay"},
		{Key: "relay.error_ban_threshold", Value: "2", Category: "relay"},
		{Key: "relay.error_ban_cooldown_seconds", Value: "60", Category: "relay"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	freshFailure := time.Now().Add(-30 * time.Second)
	cooledFailure := time.Now().Add(-2 * time.Minute)
	if err := db.Create([]model.Channel{
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "freshly-tripped",
			Models:     "gpt-cooldown",
			BaseURL:    "http://fresh.example",
			APIKey:     "fresh-key",
			Priority:   30,
			Weight:     1,
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 2,
			UpdatedAt:  freshFailure,
		},
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "cooled-probe",
			Models:     "gpt-cooldown",
			BaseURL:    "http://cooled.example",
			APIKey:     "cooled-key",
			Priority:   20,
			Weight:     1,
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 2,
			UpdatedAt:  cooledFailure,
		},
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "healthy-backup",
			Models:     "gpt-cooldown",
			BaseURL:    "http://healthy.example",
			APIKey:     "healthy-key",
			Priority:   10,
			Weight:     1,
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 0,
		},
	}).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewChannelService()
	candidates, reasons, err := svc.SelectChannelCandidatesWithRouteFacts("gpt-cooldown", RoutePreference{})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].Name != "cooled-probe" || candidates[1].Name != "healthy-backup" {
		t.Fatalf("cooled tripped channel should be allowed as a probe while fresh trip remains blocked, candidates=%+v reasons=%+v", candidates, reasons)
	}
	if reasons[routeFilterReasonHealthBlocked] != 1 {
		t.Fatalf("fresh tripped channel should still be counted as health_blocked, got %+v", reasons)
	}
}

func TestChannelBreakerProbeRecoversCooledTrippedChannel(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		if req.URL.Path != "/v1/models" {
			t.Fatalf("probe should call model list endpoint, got %s", req.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-probe"}]}`))
	}))
	defer upstream.Close()

	db, err := gorm.Open(sqlite.Open("file:channel_service_breaker_probe_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
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
		{Key: "routing.channel_cache.enabled", Value: "false", Category: "routing"},
		{Key: "relay.error_auto_ban", Value: "true", Category: "relay"},
		{Key: "relay.error_ban_threshold", Value: "2", Category: "relay"},
		{Key: "relay.error_ban_cooldown_seconds", Value: "60", Category: "relay"},
		{Key: "relay.error_probe_enabled", Value: "true", Category: "relay"},
	}).Error; err != nil {
		t.Fatal(err)
	}

	cooledFailure := time.Now().Add(-2 * time.Minute)
	freshFailure := time.Now().Add(-30 * time.Second)
	if err := db.Create([]model.Channel{
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "cooled-tripped",
			Models:     "gpt-probe",
			BaseURL:    upstream.URL,
			APIKey:     "probe-key",
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 2,
			UpdatedAt:  cooledFailure,
		},
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "fresh-tripped",
			Models:     "gpt-probe",
			BaseURL:    upstream.URL,
			APIKey:     "probe-key",
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 2,
			UpdatedAt:  freshFailure,
		},
		{
			Type:       common.ChannelTypeOpenAICompat,
			Name:       "healthy",
			Models:     "gpt-probe",
			BaseURL:    upstream.URL,
			APIKey:     "probe-key",
			Status:     common.ChannelStatusEnabled,
			ErrorCount: 0,
		},
	}).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewChannelService()
	summary, err := svc.ProbeTrippedChannelsOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("breaker probe should not fail: %v", err)
	}
	if summary.Checked != 1 || summary.Succeeded != 1 || summary.Failed != 0 {
		t.Fatalf("probe should only test cooled tripped channel, got %+v", summary)
	}
	if upstreamCalls != 1 {
		t.Fatalf("probe should call upstream once, got %d", upstreamCalls)
	}

	var cooled model.Channel
	if err := db.Where("name = ?", "cooled-tripped").First(&cooled).Error; err != nil {
		t.Fatal(err)
	}
	if cooled.ErrorCount != 0 {
		t.Fatalf("successful probe should reset cooled channel error_count, got %+v", cooled)
	}
	var fresh model.Channel
	if err := db.Where("name = ?", "fresh-tripped").First(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.ErrorCount != 2 {
		t.Fatalf("fresh tripped channel should stay blocked until cooldown elapses, got %+v", fresh)
	}
}
