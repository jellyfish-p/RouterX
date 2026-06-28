# Security Billing Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the first safety and accounting batch: reliable streaming billing, SQLite in-process rate limiting when Redis is absent, mandatory Redis for external SQL deployments, Stripe webhook payload redaction, and deterministic backend test scope.

**Architecture:** Keep the current service boundaries. Add a process-local fixed-window limiter for SQLite/no-Redis deployments, keep Redis as the only distributed limiter for external SQL deployments, and make external SQL fail fast when Redis cannot initialize. Treat an already delivered stream as billable work: if usage is known after forwarding, apply a post-delivery debit even when concurrent quota consumption has depleted the token or user balance. Store Stripe webhook event payloads as redacted, structured facts instead of raw provider JSON.

**Tech Stack:** Go 1.24.3, Gin, GORM, go-redis/v9, SQLite/MySQL/Postgres, httptest.

---

## Scope Decisions

In scope:
- SQLite or empty `SQL_DSN`: Redis is optional; rate limiting uses in-process memory when `internal.RDB == nil`.
- External SQL `SQL_DSN`: Redis is mandatory; startup fails if Redis initialization fails, `/ready` is not ready when Redis is absent/unavailable, and request-time rate limiting fails closed.
- Streaming billing: once stream chunks have been forwarded successfully and usage is available, quota is debited as delivered work even if the final balance becomes negative because of concurrent depletion.
- Stripe webhook storage: `payment_events.payload` contains redacted, useful facts only.
- Backend verification docs: use `go test ./cmd/... ./internal/...` for backend scope.

Deferred to a later plan:
- APIType-aware channel candidate filtering.
- Redis/cache invalidation failure semantics and cluster stale-window policy.

## File Structure

- Modify `internal/middleware/ratelimit.go`
  - Add process-local fixed-window limiter.
  - Keep Redis-backed limiter unchanged for external SQL mode.
  - Export a small reset hook used by router initialization/tests.
- Modify `internal/router/router.go`
  - Reset process-local limiter state in `NewRouter`, alongside existing metric resets.
- Modify `cmd/server/main.go`
  - Fail fast on Redis init failure when `service.RedisRequiredForCurrentMode()` is true.
  - Keep warning-only behavior for SQLite/no-Redis mode.
- Create `cmd/server/main_test.go`
  - Unit-test the Redis init failure policy without invoking `log.Fatal`.
- Modify `internal/service/token_service.go`
  - Add post-delivery quota debit for streaming settlement.
- Modify `internal/service/relay_service.go`
  - Use post-delivery debit only in stream settlement after forwarding succeeds.
  - Add billing snapshot evidence for post-delivery debit and overdrawn balances.
- Modify `internal/service/user_service.go`
  - Add Stripe payload redaction helper and use it for `PaymentEvent.Payload`.
- Modify `internal/router/router_test.go`
  - Add regression tests for memory limiter, Stripe redaction, and stream post-delivery debit.
- Modify `README.md` and `docs/TESTING.md`
  - Replace backend `go test ./...` guidance with `go test ./cmd/... ./internal/...`.
- Optionally modify `docs/BILLING.md` or `docs/OPERATIONS.md`
  - Document delivered-stream debit and SQLite memory limiter behavior if those docs already describe the same area.

---

### Task 1: SQLite Memory Rate Limiter

**Files:**
- Modify: `internal/middleware/ratelimit.go`
- Modify: `internal/router/router.go`
- Test: `internal/router/router_test.go`

- [ ] **Step 1: Write the failing router test**

Add this test near the existing rate limit tests in `internal/router/router_test.go`:

```go
func TestRateLimitFallsBackToMemoryWithoutRedisInSQLiteMode(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")
	t.Setenv("SQL_DSN", "")

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-memory-limit","object":"chat.completion","model":"gpt-memory-limit","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
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

	settingSvc := service.NewSettingService()
	for key, value := range map[string]string{
		"rate_limit.enabled":           "true",
		"rate_limit.global_per_min":    "0",
		"rate_limit.per_token_per_min": "1",
		"rate_limit.per_ip_per_min":    "0",
		"rate_limit.per_user_per_min":  "0",
	} {
		if err := settingSvc.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}

	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":        "memory-limit",
		"quota_limit": 50,
	})
	var tokenPayload struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}

	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "memory-limit",
		"models":   "gpt-memory-limit",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	internal.RDB = nil
	body := map[string]interface{}{
		"model": "gpt-memory-limit",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	first := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, body)
	second := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, body)

	if first.Code != http.StatusOK {
		t.Fatalf("first request should pass through memory limiter, got %d %s", first.Code, first.Body.String())
	}
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), `"code":"rate_limit_exceeded"`) {
		t.Fatalf("second request should be denied by memory limiter, got %d %s", second.Code, second.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("memory limiter should reject before upstream on the second request, got %d upstream calls", upstreamCalls)
	}
}
```

- [ ] **Step 2: Run the failing test**

Run:

```powershell
go test ./internal/router -run TestRateLimitFallsBackToMemoryWithoutRedisInSQLiteMode -count=1
```

Expected: FAIL because `internal.RDB == nil` currently bypasses rate limiting in SQLite mode.

- [ ] **Step 3: Implement the process-local limiter**

In `internal/middleware/ratelimit.go`, add `sync` to imports and add this limiter near `rateLimitExceeded`:

```go
type processRateLimitEntry struct {
	count     int64
	expiresAt time.Time
}

type processRateLimitCounter struct {
	mu      sync.Mutex
	entries map[string]processRateLimitEntry
	now     func() time.Time
}

var localRateLimitCounter = newProcessRateLimitCounter(time.Now)

func newProcessRateLimitCounter(now func() time.Time) *processRateLimitCounter {
	if now == nil {
		now = time.Now
	}
	return &processRateLimitCounter{
		entries: map[string]processRateLimitEntry{},
		now:     now,
	}
}

func ResetRateLimitState() {
	localRateLimitCounter.mu.Lock()
	defer localRateLimitCounter.mu.Unlock()
	localRateLimitCounter.entries = map[string]processRateLimitEntry{}
}

func processRateLimitExceeded(key string, limit int64, window time.Duration) (bool, int64) {
	if limit <= 0 {
		return false, 0
	}
	now := localRateLimitCounter.now()
	localRateLimitCounter.mu.Lock()
	defer localRateLimitCounter.mu.Unlock()

	entry := localRateLimitCounter.entries[key]
	if entry.expiresAt.IsZero() || !entry.expiresAt.After(now) {
		entry = processRateLimitEntry{expiresAt: now.Add(window)}
	}
	entry.count++
	localRateLimitCounter.entries[key] = entry

	if len(localRateLimitCounter.entries) > 10000 {
		for k, value := range localRateLimitCounter.entries {
			if !value.expiresAt.After(now) {
				delete(localRateLimitCounter.entries, k)
			}
		}
	}
	return entry.count > limit, entry.count
}
```

Update `rateLimitExceeded`:

```go
func rateLimitExceeded(key string, limit int64) (bool, int64, bool) {
	if limit <= 0 {
		return false, 0, false
	}
	if internal.RDB == nil {
		if service.RedisRequiredForCurrentMode() {
			return false, 0, true
		}
		exceeded, count := processRateLimitExceeded(key, limit, 2*time.Minute)
		return exceeded, count, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	count, err := internal.RDB.Incr(ctx, key).Result()
	if err != nil {
		service.RecordRedisError("rate_limit_incr")
		return false, 0, service.RedisRequiredForCurrentMode()
	}
	if count == 1 {
		if err := internal.RDB.Expire(ctx, key, 2*time.Minute).Err(); err != nil {
			service.RecordRedisError("rate_limit_expire")
			return false, count, service.RedisRequiredForCurrentMode()
		}
	}
	return count > limit, count, false
}
```

In `internal/router/router.go`, reset state in `NewRouter`:

```go
middleware.ResetRateLimitState()
```

Place it near the existing metric resets.

- [ ] **Step 4: Run the focused tests**

Run:

```powershell
go test ./internal/router -run "TestRateLimitFallsBackToMemoryWithoutRedisInSQLiteMode|TestRateLimitRedisUnavailableFailsClosedInExternalDatabaseMode|TestPublicUserAuthRoutesApplyIPRateLimit" -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit this slice**

```powershell
git add internal/middleware/ratelimit.go internal/router/router.go internal/router/router_test.go
git commit -m "fix: add sqlite memory rate limit fallback"
```

---

### Task 2: External DB Redis Fail Fast

**Files:**
- Modify: `cmd/server/main.go`
- Create: `cmd/server/main_test.go`
- Test: `cmd/server/main_test.go`

- [ ] **Step 1: Write the failing policy test**

Create `cmd/server/main_test.go`:

```go
package main

import (
	"errors"
	"testing"
)

func TestRedisInitFailureIsFatalOnlyForExternalDatabaseMode(t *testing.T) {
	err := errors.New("redis unavailable")

	t.Setenv("SQL_DSN", "sqlite://data/routerx.db")
	if redisInitFailureIsFatal(err) {
		t.Fatal("sqlite mode should not treat Redis init failure as fatal")
	}

	t.Setenv("SQL_DSN", "")
	if redisInitFailureIsFatal(err) {
		t.Fatal("default sqlite mode should not treat Redis init failure as fatal")
	}

	t.Setenv("SQL_DSN", "postgres://routerx:secret@db.example/routerx?sslmode=disable")
	if !redisInitFailureIsFatal(err) {
		t.Fatal("external database mode must treat Redis init failure as fatal")
	}

	if redisInitFailureIsFatal(nil) {
		t.Fatal("nil Redis init error must not be fatal")
	}
}
```

- [ ] **Step 2: Run the failing test**

Run:

```powershell
go test ./cmd/server -run TestRedisInitFailureIsFatalOnlyForExternalDatabaseMode -count=1
```

Expected: FAIL because `redisInitFailureIsFatal` does not exist.

- [ ] **Step 3: Implement the startup policy**

In `cmd/server/main.go`, replace the Redis init block with:

```go
if err := internal.InitRedis(); err != nil {
	if redisInitFailureIsFatal(err) {
		log.Fatalf("[FATAL] redis init failed in external database mode: %v", err)
	}
	log.Printf("[WARN] redis init failed (non-fatal in sqlite mode): %v", err)
}
```

Add this helper in the same file:

```go
func redisInitFailureIsFatal(err error) bool {
	return err != nil && service.RedisRequiredForCurrentMode()
}
```

- [ ] **Step 4: Run focused Redis mode tests**

Run:

```powershell
go test ./cmd/server ./internal/router -run "TestRedisInitFailureIsFatalOnlyForExternalDatabaseMode|TestReadinessRequiresRedisForExternalDatabaseMode|TestRateLimitRedisUnavailableFailsClosedInExternalDatabaseMode" -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit this slice**

```powershell
git add cmd/server/main.go cmd/server/main_test.go
git commit -m "fix: require redis for external database startup"
```

---

### Task 3: Stripe Webhook Payload Redaction

**Files:**
- Modify: `internal/service/user_service.go`
- Modify: `internal/router/router_test.go`

- [ ] **Step 1: Write the failing redaction test**

Add this near the Stripe webhook tests in `internal/router/router_test.go`:

```go
func TestStripeWebhookStoresRedactedPayload(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	r := newTestRouter(t)
	initResp := performJSON(r, http.MethodPost, "/v0/setup/init", "", map[string]interface{}{
		"username": "root",
		"password": "password123",
	})
	if initResp.Code != http.StatusOK {
		t.Fatalf("setup init failed: %d %s", initResp.Code, initResp.Body.String())
	}
	rootJWT := loginBearer(t, r, "root", "password123")
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(0)).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.Create(&model.PaymentProduct{
		ProductID: "quota_stripe_redaction",
		Name:      "Stripe redaction credits",
		Amount:    "9.99",
		Currency:  "usd",
		Quota:     100,
		Enabled:   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingSvc := service.NewSettingService()
	for key, value := range map[string]string{
		"payment.stripe.enabled":        "true",
		"payment.stripe.webhook_secret": "whsec_test_secret",
	} {
		if err := settingSvc.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}

	createResp := performJSON(r, http.MethodPost, "/v0/user/payment/orders", rootJWT, map[string]interface{}{
		"provider":   "stripe",
		"product_id": "quota_stripe_redaction",
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create payment order failed: %d %s", createResp.Code, createResp.Body.String())
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	var order model.PaymentOrder
	if err := internal.DB.Where("user_id = ? AND provider = ?", root.ID, common.PaymentProviderStripe).First(&order).Error; err != nil {
		t.Fatal(err)
	}

	body := stripeCheckoutCompletedPayload("evt_stripe_redacted", &order, root.ID, 999, "pi_redacted")
	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	object := envelope["data"].(map[string]interface{})["object"].(map[string]interface{})
	object["customer_email"] = "buyer@example.com"
	object["client_secret"] = "pi_redacted_secret_should_not_persist"
	object["metadata"].(map[string]interface{})["internal_note"] = "do-not-store-this"
	raw, _ := json.Marshal(envelope)

	resp := performStripeWebhook(r, string(raw), "whsec_test_secret")
	if resp.Code != http.StatusOK || strings.TrimSpace(resp.Body.String()) != "success" {
		t.Fatalf("signed stripe event should be acknowledged, got %d %s", resp.Code, resp.Body.String())
	}

	var event model.PaymentEvent
	if err := internal.DB.Where("provider = ? AND provider_event_id = ?", common.PaymentProviderStripe, "evt_stripe_redacted").First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event.Payload, "evt_stripe_redacted") || !strings.Contains(event.Payload, order.OrderNo) || !strings.Contains(event.Payload, "pi_redacted") {
		t.Fatalf("redacted payload should keep operational identifiers, got %s", event.Payload)
	}
	for _, forbidden := range []string{"buyer@example.com", "client_secret", "secret_should_not_persist", "internal_note", "do-not-store-this"} {
		if strings.Contains(event.Payload, forbidden) {
			t.Fatalf("stripe payload persisted sensitive field %q: %s", forbidden, event.Payload)
		}
	}
}
```

- [ ] **Step 2: Run the failing test**

Run:

```powershell
go test ./internal/router -run TestStripeWebhookStoresRedactedPayload -count=1
```

Expected: FAIL because the current Stripe payload is stored raw.

- [ ] **Step 3: Implement Stripe redaction**

In `internal/service/user_service.go`, add:

```go
func redactedStripePayload(event stripeWebhookEvent, session stripeCheckoutSession) map[string]interface{} {
	payload := map[string]interface{}{
		"id":   event.ID,
		"type": event.Type,
	}
	object := map[string]interface{}{
		"id":             session.ID,
		"amount":         session.Amount,
		"amount_total":   session.AmountTotal,
		"currency":       session.Currency,
		"payment_status": session.PaymentStatus,
		"payment_intent": session.PaymentIntent,
		"status":         session.Status,
		"reason":         session.Reason,
	}
	metadata := map[string]string{}
	for _, key := range []string{"order_no", "product_id", "user_id"} {
		if value := strings.TrimSpace(session.Metadata[key]); value != "" {
			metadata[key] = value
		}
	}
	if len(metadata) > 0 {
		object["metadata"] = metadata
	}
	payload["data"] = map[string]interface{}{"object": object}
	return payload
}
```

Replace `Payload: string(raw),` in `ProcessStripeWebhook` with:

```go
redactedPayload, _ := json.Marshal(redactedStripePayload(event, session))
paymentEvent := model.PaymentEvent{
	Provider:        common.PaymentProviderStripe,
	ProviderEventID: event.ID,
	OrderNo:         orderNo,
	EventType:       event.Type,
	Payload:         string(redactedPayload),
	SignatureValid:  true,
}
```

- [ ] **Step 4: Run Stripe webhook tests**

Run:

```powershell
go test ./internal/router -run "TestStripeWebhookStoresRedactedPayload|TestStripeDisputeWebhookRecordsEventAndDisablesTokensByPolicy|TestStripeUnhandledChargeEventIsAcceptedAsGenericEvent" -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit this slice**

```powershell
git add internal/service/user_service.go internal/router/router_test.go
git commit -m "fix: redact stored stripe webhook payloads"
```

---

### Task 4: Delivered Stream Billing Settlement

**Files:**
- Modify: `internal/service/token_service.go`
- Modify: `internal/service/relay_service.go`
- Modify: `internal/router/router_test.go`
- Optional docs: `docs/BILLING.md`

- [ ] **Step 1: Write the failing stream settlement test**

Add this near the stream tests in `internal/router/router_test.go`:

```go
func TestChatCompletionStreamDeliveredUsageDebitsEvenWhenQuotaIsDepleted(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-delivered-debit\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-delivered-debit\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
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
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(1)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":        "delivered-stream",
		"quota_limit": 1,
	})
	var tokenPayload struct {
		Data struct {
			ID  uint   `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatal(err)
	}
	if tokenResp.Code != http.StatusOK || tokenPayload.Data.Key == "" {
		t.Fatalf("create token failed: %d %s", tokenResp.Code, tokenResp.Body.String())
	}
	channelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "delivered-stream",
		"models":   "gpt-delivered-stream",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	streamResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-delivered-stream",
		"stream": true,
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	body := streamResp.Body.String()
	if streamResp.Code != http.StatusOK || !strings.Contains(body, "chatcmpl-delivered-debit") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("delivered stream should still reach client, got %d %s", streamResp.Code, body)
	}

	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.QuotaLimit != -6 || storedToken.QuotaUsed != 7 {
		t.Fatalf("delivered stream should debit token into debt, got quota_limit=%d quota_used=%d", storedToken.QuotaLimit, storedToken.QuotaUsed)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != -6 {
		t.Fatalf("delivered stream should debit user into debt, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 7 {
		t.Fatalf("delivered stream should have settled success log, got %+v", callLog)
	}
	if !strings.Contains(callLog.BillingSnapshot, `"billing_status":"settled"`) ||
		!strings.Contains(callLog.BillingSnapshot, `"post_delivery_debit":true`) ||
		!strings.Contains(callLog.BillingSnapshot, `"overdrawn":true`) {
		t.Fatalf("billing snapshot should mark post-delivery overdrawn debit, got %s", callLog.BillingSnapshot)
	}
}
```

- [ ] **Step 2: Run the failing test**

Run:

```powershell
go test ./internal/router -run TestChatCompletionStreamDeliveredUsageDebitsEvenWhenQuotaIsDepleted -count=1
```

Expected: FAIL because the current stream settlement uses strict `DeductQuotaWithSnapshot` and records failed billing instead of debiting delivered work.

- [ ] **Step 3: Add post-delivery debit API**

In `internal/service/token_service.go`, add:

```go
func (s *TokenService) DeductDeliveredQuotaWithSnapshot(tokenID uint, quota int64) (QuotaDeductionResult, error) {
	result := QuotaDeductionResult{}
	if quota <= 0 {
		return result, nil
	}
	err := internal.DB.Transaction(func(tx *gorm.DB) error {
		var token model.Token
		if err := tx.Preload("User").First(&token, tokenID).Error; err != nil {
			return err
		}
		result.TokenUnlimited = tokenIsUnlimited(token)
		result.TokenQuotaBefore = tokenRemainingQuota(token)
		result.TokenQuotaAfter = result.TokenQuotaBefore - quota
		if result.TokenUnlimited {
			result.TokenQuotaBefore = common.QuotaUnlimited
			result.TokenQuotaAfter = common.QuotaUnlimited
		}
		if token.User != nil {
			result.UserQuotaBefore = token.User.Quota
			result.UserQuotaAfter = token.User.Quota - quota
		}
		tokenUpdates := map[string]interface{}{
			"quota_used": gorm.Expr("quota_used + ?", quota),
		}
		if result.TokenUnlimited {
			tokenUpdates["quota_limit"] = common.QuotaUnlimited
			tokenUpdates["unlimited"] = true
		} else {
			tokenUpdates["quota_limit"] = gorm.Expr("quota_limit - ?", quota)
			tokenUpdates["unlimited"] = false
		}
		res := tx.Model(&model.Token{}).
			Where("id = ? AND status = ?", token.ID, common.TokenStatusEnabled).
			Updates(tokenUpdates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrAPIKeyDisabled
		}
		res = tx.Model(&model.User{}).
			Where("id = ? AND status = ?", token.UserID, common.UserStatusEnabled).
			Update("quota", gorm.Expr("quota - ?", quota))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrAPIUserDisabled
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	s.invalidateAPIKeyAuthCacheByIDs(tokenID)
	return result, nil
}
```

- [ ] **Step 4: Use post-delivery debit for streams**

In `internal/service/relay_service.go`, replace the stream settlement deduction:

```go
deduction, err := s.tokenService.DeductDeliveredQuotaWithSnapshot(token.ID, billing.QuotaUsed)
if err != nil {
	logCtx := ContextWithRelayBillingSnapshot(ctx, buildRelayBillingFailureSnapshot(usage, billing, deduction, err))
	_ = s.recordLog(logCtx, token, channel, reqInfo.Model, usage, common.LogStatusFailed, 0, "stream post-delivery billing failed", clientIP)
	return usage, err
}
```

Add a billing snapshot helper:

```go
func buildRelayDeliveredBillingSnapshot(usage *relay.Usage, billing relayBillingResult, deduction QuotaDeductionResult) string {
	raw := buildRelayBillingSnapshot(usage, billing, deduction)
	if raw == "" {
		return raw
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return raw
	}
	snapshot["post_delivery_debit"] = true
	snapshot["overdrawn"] = deduction.TokenQuotaAfter < 0 || deduction.UserQuotaAfter < 0
	updated, err := json.Marshal(snapshot)
	if err != nil {
		return raw
	}
	return string(updated)
}
```

Use it in the stream success log:

```go
logCtx := ContextWithRelayBillingSnapshot(ctx, buildRelayDeliveredBillingSnapshot(usage, billing, deduction))
_ = s.recordLog(logCtx, token, channel, reqInfo.Model, usage, common.LogStatusSuccess, billing.QuotaUsed, "", clientIP)
```

Leave non-stream and raw-response settlement on `DeductQuotaWithSnapshot` so pre-response calls still fail closed before a successful response body is returned.

- [ ] **Step 5: Run stream regression tests**

Run:

```powershell
go test ./internal/router -run "TestChatCompletionStreamDeliveredUsageDebitsEvenWhenQuotaIsDepleted|TestChatCompletionStreamCancelsUpstreamWhenClientWriteFails|TestChatCompletionStreamForwardsSSEAndDeductsUsage|TestCompletionsStreamForwardsSSEAndDeductsUsage" -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit this slice**

```powershell
git add internal/service/token_service.go internal/service/relay_service.go internal/router/router_test.go
git commit -m "fix: settle delivered stream billing"
```

---

### Task 5: Backend Test Scope Documentation

**Files:**
- Modify: `README.md`
- Modify: `docs/TESTING.md`
- Optional: any local script that documents backend verification, if found during implementation.

- [ ] **Step 1: Write the documentation change**

Replace backend guidance that says:

```powershell
go test ./...
```

with:

```powershell
go test ./cmd/... ./internal/...
```

If a section intentionally documents full workspace package discovery, keep `go test ./...` but add one sentence explaining that backend CI should prefer the explicit `./cmd/... ./internal/...` package set because `frontend/node_modules` can contain Go files after frontend dependency installation.

- [ ] **Step 2: Verify no backend guidance still recommends broad package discovery**

Run:

```powershell
rg -n "go test \./\.\.\." README.md docs/TESTING.md
```

Expected: either no matches, or only explanatory matches that explicitly warn against using it as the backend verification command.

- [ ] **Step 3: Commit this slice**

```powershell
git add README.md docs/TESTING.md
git commit -m "docs: narrow backend go test scope"
```

---

### Task 6: Final Verification

**Files:**
- No new code files unless prior tasks reveal a small missing test helper.

- [ ] **Step 1: Format Go changes**

Run:

```powershell
gofmt -w cmd/server/main.go cmd/server/main_test.go internal/middleware/ratelimit.go internal/router/router.go internal/router/router_test.go internal/service/token_service.go internal/service/relay_service.go internal/service/user_service.go
```

Expected: exit 0.

- [ ] **Step 2: Run focused backend tests**

Run:

```powershell
go test ./cmd/... ./internal/... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run vet**

Run:

```powershell
go vet ./cmd/... ./internal/...
```

Expected: exit 0.

- [ ] **Step 4: Check whitespace and docs diff**

Run:

```powershell
git diff --check
git diff --stat
```

Expected: `git diff --check` exits 0; diff stat only includes planned files.

- [ ] **Step 5: Commit any final doc or test fix**

Only if Task 6 required additional edits:

```powershell
git add README.md docs/TESTING.md docs/BILLING.md docs/OPERATIONS.md cmd/server/main.go cmd/server/main_test.go internal/middleware/ratelimit.go internal/router/router.go internal/router/router_test.go internal/service/token_service.go internal/service/relay_service.go internal/service/user_service.go
git commit -m "test: verify security billing closure"
```

---

## Self-Review Checklist

- Spec coverage:
  - SQLite ignores Redis but still rate-limits through memory: Task 1.
  - External DB requires Redis: Task 2 plus existing readiness/request-time checks.
  - Stream billing semantics: Task 4.
  - Stripe payload redaction: Task 3.
  - Backend test scope isolation: Task 5.
  - Route APIType filtering and cache consistency intentionally deferred.
- Placeholder scan:
  - No TBD/TODO placeholders.
  - Each implementation task includes file paths, code shape, commands, and expected results.
- Type consistency:
  - `DeductDeliveredQuotaWithSnapshot` returns the existing `QuotaDeductionResult`.
  - `ResetRateLimitState` is exported from `middleware` and called by `router.NewRouter`.
  - Stripe redaction uses existing `stripeWebhookEvent` and `stripeCheckoutSession`.
