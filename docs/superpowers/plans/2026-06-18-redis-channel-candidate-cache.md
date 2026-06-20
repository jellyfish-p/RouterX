# Redis Channel Candidate Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Redis-backed shared snapshot for channel candidate cache so multiple RouterX instances can reuse the same ordered channel list and invalidate it through the existing `routing.channel_cache.version`.

**Architecture:** Keep the existing in-process cache as the first layer. On a local miss, `ChannelService` will try a Redis snapshot keyed by the current cache version and TTL metadata, then fall back to the database and warm both local memory and Redis. Channel mutations continue bumping `routing.channel_cache.version`, which makes other instances ignore stale local/Redis snapshots on their next selection without adding a new API or table.

**Tech Stack:** Go, GORM service tests, go-redis client, existing fake Redis test server, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Shared Snapshot Behavior

**Files:**
- Modify: `internal/service/channel_service_test.go`

- [x] **Step 1: Write the failing test**

Add a service-level regression test near the other channel cache tests:

```go
func TestChannelCandidateCacheUsesRedisSharedSnapshotAcrossServices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:channel_service_redis_cache_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Channel{}, &model.Setting{}); err != nil {
		t.Fatal(err)
	}
	redisServer := newFakeRedisServer(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), Protocol: 2, DisableIdentity: true})
	oldDB, oldRDB := internal.DB, internal.RDB
	internal.DB = db
	internal.RDB = rdb
	t.Cleanup(func() {
		_ = rdb.Close()
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
		Name:     "redis-stable",
		Models:   "gpt-redis-cache",
		BaseURL:  "http://redis-stable.example",
		APIKey:   "redis-stable-key",
		Priority: 1,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	firstSvc := NewChannelService()
	first, _, err := firstSvc.SelectChannelCandidatesWithRouteFacts("gpt-redis-cache", RoutePreference{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Name != "redis-stable" {
		t.Fatalf("initial service should load stable channel, got %+v", first)
	}
	if cached, ok := redisServer.HashValue("routing:channel_candidate_cache", "snapshot"); !ok || !strings.Contains(cached, "redis-stable") {
		t.Fatalf("first service should warm Redis shared snapshot, ok=%v value=%q", ok, cached)
	}

	if err := db.Create(&model.Channel{
		Type:     common.ChannelTypeOpenAICompat,
		Name:     "redis-newer-db-only",
		Models:   "gpt-redis-cache",
		BaseURL:  "http://redis-newer.example",
		APIKey:   "redis-newer-key",
		Priority: 99,
		Weight:   1,
		Status:   common.ChannelStatusEnabled,
	}).Error; err != nil {
		t.Fatal(err)
	}

	secondSvc := NewChannelService()
	shared, _, err := secondSvc.SelectChannelCandidatesWithRouteFacts("gpt-redis-cache", RoutePreference{})
	if err != nil {
		t.Fatal(err)
	}
	if len(shared) != 1 || shared[0].Name != "redis-stable" {
		t.Fatalf("second service should reuse Redis snapshot before version bump, got %+v", shared)
	}

	if err := NewSettingService().Set("routing.channel_cache.version", "2"); err != nil {
		t.Fatal(err)
	}
	refreshed, _, err := secondSvc.SelectChannelCandidatesWithRouteFacts("gpt-redis-cache", RoutePreference{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed) != 2 || refreshed[0].Name != "redis-newer-db-only" || refreshed[1].Name != "redis-stable" {
		t.Fatalf("version bump should bypass stale Redis snapshot and reload DB, got %+v", refreshed)
	}
}
```

Add the missing imports:

```go
import (
    "strings"

    "github.com/redis/go-redis/v9"
)
```

- [x] **Step 2: Run the failing test**

Run: `go test ./internal/service -run TestChannelCandidateCacheUsesRedisSharedSnapshotAcrossServices -count=1`

Expected: FAIL because the second service currently misses local memory and loads the changed database instead of a Redis snapshot.

### Task 2: Redis Snapshot Implementation

**Files:**
- Modify: `internal/service/channel_service.go`

- [x] **Step 1: Add snapshot constants and payload**

Add near the channel cache types:

```go
const (
	channelCandidateCacheRedisHash  = "routing:channel_candidate_cache"
	channelCandidateCacheRedisField = "snapshot"
)

type channelCandidateCacheRedisSnapshot struct {
	Version       int             `json:"version"`
	ExpiresAtUnix int64          `json:"expires_at_unix"`
	Channels      []model.Channel `json:"channels"`
}
```

- [x] **Step 2: Read Redis on local cache miss**

Inside `channelsForCandidateSelection`, after the in-memory cache check and before database load:

```go
if channels, expiresAt, ok := s.loadCandidateCacheFromRedis(cfg, now); ok {
	s.candidateCache = channelCandidateCache{
		loaded:    true,
		version:   cfg.version,
		expiresAt: expiresAt,
		channels:  cloneChannels(channels),
	}
	return cloneChannels(channels), nil
}
```

- [x] **Step 3: Warm Redis after database load and preload**

After loading channels from the database in `channelsForCandidateSelection` and `PreloadCandidateCache`, call:

```go
s.storeCandidateCacheInRedis(cfg, now, channels)
```

Redis write errors must be ignored because the database path remains authoritative.

- [x] **Step 4: Add helper methods**

Add helpers:

```go
func (s *ChannelService) loadCandidateCacheFromRedis(cfg channelCandidateCacheConfig, now time.Time) ([]model.Channel, time.Time, bool) {
	if internal.RDB == nil {
		return nil, time.Time{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := internal.RDB.HGet(ctx, channelCandidateCacheRedisHash, channelCandidateCacheRedisField).Result()
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil, time.Time{}, false
	}
	var snapshot channelCandidateCacheRedisSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, time.Time{}, false
	}
	if snapshot.Version != cfg.version {
		return nil, time.Time{}, false
	}
	expiresAt := time.Unix(snapshot.ExpiresAtUnix, 0)
	if cfg.ttl > 0 && (snapshot.ExpiresAtUnix <= 0 || !now.Before(expiresAt)) {
		return nil, time.Time{}, false
	}
	if cfg.ttl == 0 {
		expiresAt = time.Time{}
	}
	return cloneChannels(snapshot.Channels), expiresAt, true
}

func (s *ChannelService) storeCandidateCacheInRedis(cfg channelCandidateCacheConfig, now time.Time, channels []model.Channel) {
	if internal.RDB == nil {
		return
	}
	expiresAtUnix := int64(0)
	if cfg.ttl > 0 {
		expiresAtUnix = now.Add(cfg.ttl).Unix()
	}
	raw, err := json.Marshal(channelCandidateCacheRedisSnapshot{
		Version:       cfg.version,
		ExpiresAtUnix: expiresAtUnix,
		Channels:      cloneChannels(channels),
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = internal.RDB.HSet(ctx, channelCandidateCacheRedisHash, channelCandidateCacheRedisField, string(raw)).Err()
}
```

- [x] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/service -run TestChannelCandidateCacheUsesRedisSharedSnapshotAcrossServices -count=1`

Expected: PASS.

### Task 3: Documentation and Apifox

**Files:**
- Modify: `docs/RELAY.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/SETTINGS.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document Redis shared snapshot behavior**

Update Relay/settings docs to say:

```text
When Redis is configured and routing.channel_cache.enabled=true, the ordered channel candidate snapshot is also stored in Redis under routing:channel_candidate_cache. Instances reuse it when routing.channel_cache.version matches and the embedded TTL has not expired. Channel mutations bump routing.channel_cache.version, so other instances ignore stale snapshots on their next selection.
```

- [x] **Step 2: Update traceability and roadmap**

Replace “Redis 共享快照和跨实例广播仍需补齐” with a narrower remaining gap:

```text
Redis 共享快照和基于 version 的跨实例被动失效已完成；主动 pub/sub 广播仍属于集群增强。
```

- [x] **Step 3: Update Apifox settings description**

In `docs/apifox/openapi.yaml`, extend the settings endpoint description where `routing.channel_cache.*` is listed so Apifox imports the Redis shared-cache behavior.

- [x] **Step 4: Validate OpenAPI YAML**

Run: `bun -e "const fs=require('fs'); const yaml=require('yaml'); yaml.parse(fs.readFileSync('docs/apifox/openapi.yaml','utf8')); console.log('openapi yaml ok')"`

Expected: prints `openapi yaml ok`.

### Task 4: Verification and Commit

**Files:**
- Verify all modified files

- [x] **Step 1: Run focused tests**

Run: `go test ./internal/service -run "TestChannelCandidateCache(UsesRedisSharedSnapshotAcrossServices|UsesVersionInvalidation|PreloadWarmsCache|PreloadWarmsAfterChannelChange|PreloadSkipsWhenDisabled)" -count=1`

Expected: PASS.

- [x] **Step 2: Run full backend tests**

Run: `go test ./... -count=1`

Expected: PASS.

- [x] **Step 3: Check diffs**

Run: `git diff --check`

Expected: no output.

- [x] **Step 4: Stage and commit**

Run:

```bash
git add internal/service/channel_service.go internal/service/channel_service_test.go docs/RELAY.md docs/ROADMAP.md docs/TRACEABILITY.md docs/SETTINGS.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-redis-channel-candidate-cache.md
git commit -m "feat: share channel candidate cache via redis"
```

Expected: commit succeeds on branch `codex/backend-p0-closure`.
