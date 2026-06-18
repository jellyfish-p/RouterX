# User Rate Limit Dimension Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Redis-backed `rate_limit.per_user_per_min` setting so one user cannot bypass request limits by creating multiple API Keys.

**Architecture:** Extend the existing `RateLimit` middleware and fixed-minute Redis keys. Keep the current global, IP and token dimensions unchanged, then add a user dimension keyed by the authenticated API Key owner. A value of `0` disables the dimension, matching existing `rate_limit.*` semantics.

**Tech Stack:** Go, Gin middleware, Redis fake server used by router tests, settings defaults/validation, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Red Test

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add user-level rate limit coverage**

Add `TestRateLimitPerUserAppliesAcrossAPIKeys` near `TestRateLimitUsesSettingsAndEntryProtocolErrorShape`. It should:

```go
func TestRateLimitPerUserAppliesAcrossAPIKeys(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-user-limit","object":"chat.completion","model":"gpt-user-limit","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}

	redisServer := newRouterFakeRedisServer(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), Protocol: 2, DisableIdentity: true})
	internal.RDB = rdb
	t.Cleanup(func() {
		_ = rdb.Close()
		internal.RDB = nil
	})
	settingSvc := service.NewSettingService()
	if err := settingSvc.Set("rate_limit.global_per_min", "0"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("rate_limit.per_ip_per_min", "0"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("rate_limit.per_token_per_min", "0"); err != nil {
		t.Fatal(err)
	}
	if err := settingSvc.Set("rate_limit.per_user_per_min", "1"); err != nil {
		t.Fatal(err)
	}

	createToken := func(name string) (uint, string) {
		resp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
			"name":         name,
			"remain_quota": 50,
		})
		var payload struct {
			Data struct {
				ID  uint   `json:"id"`
				Key string `json:"key"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if resp.Code != http.StatusOK || payload.Data.ID == 0 || payload.Data.Key == "" {
			t.Fatalf("create token %s failed: %d %s", name, resp.Code, resp.Body.String())
		}
		return payload.Data.ID, payload.Data.Key
	}
	firstTokenID, firstKey := createToken("user-limit-a")
	secondTokenID, secondKey := createToken("user-limit-b")

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "user-limit",
		"models":   "gpt-user-limit",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}
	body := map[string]interface{}{
		"model": "gpt-user-limit",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	first := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+firstKey, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first request should pass user limit, got %d %s", first.Code, first.Body.String())
	}
	second := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+secondKey, body)
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), `"code":"rate_limit_exceeded"`) {
		t.Fatalf("second token for same user should be blocked by user limit, got %d %s", second.Code, second.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("user-level limited request should not call upstream, got %d", upstreamCalls)
	}
	var firstToken, secondToken model.Token
	if err := internal.DB.First(&firstToken, firstTokenID).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.First(&secondToken, secondTokenID).Error; err != nil {
		t.Fatal(err)
	}
	if firstToken.RemainQuota != 48 || secondToken.RemainQuota != 50 {
		t.Fatalf("only first token should be charged, first=%d second=%d", firstToken.RemainQuota, secondToken.RemainQuota)
	}
	var failedLog model.Log
	if err := internal.DB.Where("status = ? AND token_id = ? AND error_msg LIKE ?", common.LogStatusFailed, secondTokenID, "%user rate limit%").First(&failedLog).Error; err != nil {
		t.Fatal(err)
	}
	var policySnapshot map[string]interface{}
	if err := json.Unmarshal([]byte(failedLog.PolicySnapshot), &policySnapshot); err != nil {
		t.Fatalf("user rate limit rejection should store policy snapshot JSON, got %q: %v", failedLog.PolicySnapshot, err)
	}
	scopeResult, ok := policySnapshot["scope_result"].(map[string]interface{})
	if !ok ||
		policySnapshot["reject_code"] != "rate_limit_exceeded" ||
		policySnapshot["quota_precheck"] != "rate_limit_exceeded" ||
		scopeResult["rate_limit"] != "deny" ||
		scopeResult["rate_limit_dimension"] != "user" {
		t.Fatalf("unexpected user rate limit policy snapshot: %+v", policySnapshot)
	}
}
```

- [x] **Step 2: Verify red**

Run:

```powershell
go test ./internal/router -run "TestRateLimitPerUserAppliesAcrossAPIKeys" -count=1
```

Expected: FAIL because `rate_limit.per_user_per_min` is not registered or enforced.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/middleware/ratelimit.go`
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`

- [x] **Step 1: Add the user limit to config**

Extend `rateLimitConfig`:

```go
perUserPerMin int64
```

Default it to `0` in `loadRateLimitConfig` so existing installs only enable it when the setting exists and is positive.

- [x] **Step 2: Read the new setting**

In `loadRateLimitConfig`, read:

```go
if limit, err := settingSvc.GetInt("rate_limit.per_user_per_min"); err == nil && limit >= 0 {
	cfg.perUserPerMin = int64(limit)
}
```

- [x] **Step 3: Enforce the user dimension**

In `RateLimit()`, after token-level limit:

```go
if token, ok := CurrentAPIToken(c); ok {
	if cfg.perUserPerMin > 0 && token.UserID > 0 && exceeded(fmt.Sprintf("rl:user:%d:%d", token.UserID, now), cfg.perUserPerMin) {
		writeRateLimitError(c, "user")
		c.Abort()
		return
	}
}
```

- [x] **Step 4: Register default and validation**

Add default:

```go
"rate_limit.per_user_per_min": "0",
```

Validate it with other non-negative `rate_limit.*` settings. `0` disables the dimension.

- [x] **Step 5: Verify green**

Run:

```powershell
go test ./internal/router -run "TestRateLimitPerUserAppliesAcrossAPIKeys" -count=1
```

Expected: PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/SETTINGS.md`
- Modify: `docs/POLICIES.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/OPERATIONS.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document the setting**

Document `rate_limit.per_user_per_min`, default `0`, `>=0`, hot setting, and `0` means disabled.

- [x] **Step 2: Update coverage statements**

Update reliability/rate-limit coverage language from global/IP/Token only to global/IP/Token/User where appropriate, while leaving model/channel dimensions as future work.

- [x] **Step 3: Update Apifox**

Add `rate_limit.per_user_per_min` to settings descriptions and examples so Apifox imports the key meaning.

### Task 4: Verification And Git Archive

**Files:**
- All modified files.

- [x] **Step 1: Run targeted tests**

```powershell
go test ./internal/router -run "TestRateLimitPerUserAppliesAcrossAPIKeys|TestRateLimitUsesSettingsAndEntryProtocolErrorShape|TestSettingsValidationAndReadiness|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestMetricsEndpointIncludesRelayPaymentAndInfrastructureSignals" -count=1
```

- [x] **Step 2: Run full tests**

```powershell
go test ./... -count=1
```

- [x] **Step 3: Validate Apifox YAML**

```powershell
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
```

- [x] **Step 4: Check whitespace**

```powershell
git diff --check
```

- [x] **Step 5: Commit**

```powershell
git add internal/router/router_test.go internal/middleware/ratelimit.go internal/service/setup_service.go internal/service/setting_service.go docs/SETTINGS.md docs/POLICIES.md docs/RELAY.md docs/OPERATIONS.md docs/TESTING.md docs/TRACEABILITY.md docs/ROADMAP.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-user-rate-limit-dimension.md
git commit -m "feat: add user rate limit dimension"
```
