# API Key Auth Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Redis-backed API Key authentication lookup cache with explicit invalidation for token lifecycle changes.

**Architecture:** Cache only the mapping from `SHA256(api_key)` to `token_id`, never the raw API Key or an authorization decision. Each cache hit still loads the token and user by primary key from the database, so status, expiry, quota, user state, group and scope changes remain authoritative. Token lifecycle mutations delete the lookup cache key; Redis failures fall back to the existing database lookup path.

**Tech Stack:** Go, Gin middleware through `TokenService`, go-redis, existing fake Redis test servers, GORM service tests, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Cache Behavior Tests

**Files:**
- Modify: `internal/service/token_service_test.go`
- Modify: `internal/service/setting_service_test.go`

- [x] **Step 1: Add fake Redis string command support**

Extend `fakeRedisServer` with a `strings map[string]string`, `StringValue(key string)` and `SetString(key, value string)`. Add RESP support for `GET`, `SET` and `DEL`.

- [x] **Step 2: Write cache resolve regression test**

Add `TestValidateAndGetTokenResolvesFromRedisAuthCache`:

```go
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
```

Run: `go test ./internal/service -run TestValidateAndGetTokenResolvesFromRedisAuthCache -count=1`

Expected: FAIL because `ValidateAndGetToken` currently only queries DB by key hash.

- [x] **Step 3: Write cache warm and invalidation regression test**

Add `TestAPIKeyAuthCacheWarmsAndClearsOnDisable`:

```go
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
```

Run: `go test ./internal/service -run TestAPIKeyAuthCacheWarmsAndClearsOnDisable -count=1`

Expected: FAIL because no cache is warmed or cleared yet.

### Task 2: TokenService Cache Implementation

**Files:**
- Modify: `internal/service/token_service.go`

- [x] **Step 1: Add cache constants and helpers**

Add:

```go
const (
	apiKeyAuthCachePrefix = "api_key_auth:"
	apiKeyAuthCacheTTL    = time.Minute
)

func apiKeyAuthCacheKey(hash string) string {
	return apiKeyAuthCachePrefix + strings.TrimSpace(hash)
}
```

- [x] **Step 2: Read cache before hash lookup**

At the start of `ValidateAndGetToken`, after deriving the SHA256 hash, try Redis `GET api_key_auth:<hash>`, parse `token_id`, and load the token by primary key with `User` and `User.Group`. Validate token status, expiry and user status with the same checks as the DB hash path.

- [x] **Step 3: Warm cache after DB validation**

After DB hash or legacy plaintext lookup succeeds and the token is valid, `SET api_key_auth:<hash> <token_id> EX <ttl>`. If `expired_at` is sooner than the default TTL, use the remaining lifetime as the Redis TTL. Redis errors are ignored.

- [x] **Step 4: Add explicit invalidation helpers**

Add helpers to delete cache keys by token hash, token ID or user ID. Token ID/user ID helpers should query token hashes from the database and call Redis `DEL`.

- [x] **Step 5: Call invalidation from lifecycle mutations**

Invalidate cache after successful `RotateForUser`, `DisableForUser`, `ReportLeakForUser` through disable, `UpdateScopeForUser`, `Update`, `Delete`, `BatchDisable`, `BatchExpire` and successful `DeductQuotaWithSnapshot`.

- [x] **Step 6: Run focused tests**

Run:

```bash
go test ./internal/service -run "TestValidateAndGetToken(ResolvesFromRedisAuthCache|RejectsExpirationBoundary)|TestAPIKeyAuthCacheWarmsAndClearsOnDisable" -count=1
```

Expected: PASS.

### Task 3: Router Fake Redis Compatibility

**Files:**
- Modify: `internal/router/redis_test.go`

- [x] **Step 1: Add GET/SET/DEL support**

Mirror the small string command support from the service fake Redis server so router integration tests using Redis continue to exercise the auth cache path without protocol errors.

- [x] **Step 2: Run router auth/cache adjacent tests**

Run:

```bash
go test ./internal/router -run "TestP0BackendFlow|TestRelayPrecheckRejectsBeforeUpstream|TestAdminAPIKeyQueryAndBatchDisable|TestAdminAPIKeyBatchExpire" -count=1
```

Expected: PASS.

### Task 4: Documentation and Apifox

**Files:**
- Modify: `docs/API_KEYS.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document conservative cache semantics**

Document that Redis caches only `SHA256(api_key) -> token_id`; DB remains authoritative for status, expiry, quota, scope and user state. Lifecycle mutations clear lookup cache entries.

- [x] **Step 2: Update traceability/testing status**

Replace the broad “缓存失效仍待补” wording with the completed lookup-cache invalidation slice and keep “更完整泄露窗口分析” as remaining.

- [x] **Step 3: Validate OpenAPI YAML**

Run:

```bash
bun -e "const fs=require('fs'); const yaml=require('yaml'); yaml.parse(fs.readFileSync('docs/apifox/openapi.yaml','utf8')); console.log('openapi yaml ok')"
```

Expected: `openapi yaml ok`.

### Task 5: Verification and Commit

**Files:**
- Verify all modified files.

- [x] **Step 1: Run focused tests**

Run the service and router focused commands from previous tasks.

- [x] **Step 2: Run full backend tests**

Run:

```bash
go test ./... -count=1
```

Expected: PASS.

- [x] **Step 3: Check diffs**

Run:

```bash
git diff --check
git diff --cached --check
```

Expected: no whitespace errors.

- [ ] **Step 4: Commit**

Run:

```bash
git add internal/service/token_service.go internal/service/token_service_test.go internal/service/setting_service_test.go internal/router/redis_test.go docs/API_KEYS.md docs/TRACEABILITY.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-api-key-auth-cache.md
git commit -m "feat: cache api key auth lookups"
```

Expected: commit succeeds on `codex/backend-p0-closure`.
