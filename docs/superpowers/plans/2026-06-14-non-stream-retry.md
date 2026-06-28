# Non-Stream Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add configurable, safe retry for non-stream relay calls when the selected upstream fails with retryable errors before any response is sent to the client.

**Architecture:** Keep retry inside `RelayService.Relay`, after local validation, quota precheck, route filtering, and candidate selection. `ChannelService` will expose an ordered candidate list while preserving existing single-channel selection semantics. Only non-stream requests retry; 400/401/403, conversion failures, billing failures, and all stream requests stay single-attempt.

**Tech Stack:** Go, Gin router tests, httptest upstream stubs, GORM SQLite test DB, existing `SettingService` and relay adapters.

---

### Task 1: Retry Success Regression Test

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Write the failing test**

Add this test near the existing upstream error mapping tests:

```go
func TestChatCompletionRetriesRetryableUpstreamAndDeductsOnce(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	firstCalls := 0
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		firstCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
	}))
	defer firstUpstream.Close()

	secondCalls := 0
	secondAuth := ""
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		secondCalls++
		secondAuth = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-retry","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer secondUpstream.Close()

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
	if err := internal.DB.Model(&model.Setting{}).Where("key = ?", "relay.retry_count").Update("value", "1").Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "retry",
		"quota_limit": 50,
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

	firstChannelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "primary",
		"models":   "gpt-test",
		"base_url": firstUpstream.URL,
		"api_key":  "first-secret",
		"priority": 20,
		"idx":      1,
	})
	var firstChannelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(firstChannelResp.Body.Bytes(), &firstChannelPayload); err != nil {
		t.Fatal(err)
	}
	if firstChannelResp.Code != http.StatusOK || firstChannelPayload.Data.ID == 0 {
		t.Fatalf("create first channel failed: %d %s", firstChannelResp.Code, firstChannelResp.Body.String())
	}
	secondChannelResp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
		"type":     common.ChannelTypeOpenAICompat,
		"name":     "backup",
		"models":   "gpt-test",
		"base_url": secondUpstream.URL,
		"api_key":  "second-secret",
		"priority": 10,
		"idx":      2,
	})
	var secondChannelPayload struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(secondChannelResp.Body.Bytes(), &secondChannelPayload); err != nil {
		t.Fatal(err)
	}
	if secondChannelResp.Code != http.StatusOK || secondChannelPayload.Data.ID == 0 {
		t.Fatalf("create second channel failed: %d %s", secondChannelResp.Code, secondChannelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("retry should succeed through backup channel, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("expected one call to each upstream, first=%d second=%d", firstCalls, secondCalls)
	}
	if secondAuth != "Bearer second-secret" {
		t.Fatalf("backup request should use backup secret, got %q", secondAuth)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.QuotaLimit != 45 {
		t.Fatalf("retry success should deduct once by usage total 5, got %d", storedToken.QuotaLimit)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 95 {
		t.Fatalf("retry success should deduct user quota once by 5, got %d", root.Quota)
	}
	var firstChannel, secondChannel model.Channel
	if err := internal.DB.First(&firstChannel, firstChannelPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := internal.DB.First(&secondChannel, secondChannelPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if firstChannel.ErrorCount != 1 || secondChannel.ErrorCount != 0 {
		t.Fatalf("retry should mark failed channel once and successful channel healthy, first=%d second=%d", firstChannel.ErrorCount, secondChannel.ErrorCount)
	}
	var logs []model.Log
	if err := internal.DB.Order("id ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].Status != common.LogStatusFailed || logs[1].Status != common.LogStatusSuccess || logs[1].QuotaUsed != 5 {
		t.Fatalf("retry should record failed attempt and final success, logs=%+v", logs)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/router -run TestChatCompletionRetriesRetryableUpstreamAndDeductsOnce -count=1`

Expected: FAIL because the first `500` response returns `upstream_500` and the backup channel is not called.

### Task 2: Non-Retryable Error Test

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Write the failing or guarding test**

Add this test near `TestChatCompletionRetriesRetryableUpstreamAndDeductsOnce`:

```go
func TestChatCompletionDoesNotRetryNonRetryableUpstreamStatus(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	firstCalls := 0
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		firstCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer firstUpstream.Close()

	secondCalls := 0
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-should-not-call","choices":[]}`))
	}))
	defer secondUpstream.Close()

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
	if err := internal.DB.Model(&model.Setting{}).Where("key = ?", "relay.retry_count").Update("value", "1").Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "no-retry",
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
	for _, channel := range []struct {
		name     string
		baseURL  string
		priority int
	}{
		{name: "bad-request", baseURL: firstUpstream.URL, priority: 20},
		{name: "backup", baseURL: secondUpstream.URL, priority: 10},
	} {
		resp := performJSON(r, http.MethodPost, "/v0/admin/channel", rootJWT, map[string]interface{}{
			"type":     common.ChannelTypeOpenAICompat,
			"name":     channel.name,
			"models":   "gpt-test",
			"base_url": channel.baseURL,
			"api_key":  channel.name + "-secret",
			"priority": channel.priority,
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create channel %q failed: %d %s", channel.name, resp.Code, resp.Body.String())
		}
	}
	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})
	if chatResp.Code != http.StatusBadRequest || !strings.Contains(chatResp.Body.String(), `"code":"upstream_400"`) {
		t.Fatalf("upstream 400 should not retry, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if firstCalls != 1 || secondCalls != 0 {
		t.Fatalf("non-retryable upstream status should not call backup, first=%d second=%d", firstCalls, secondCalls)
	}
}
```

- [x] **Step 2: Run focused tests**

Run: `go test ./internal/router -run "TestChatCompletionRetriesRetryableUpstreamAndDeductsOnce|TestChatCompletionDoesNotRetryNonRetryableUpstreamStatus" -count=1`

Expected: first test FAILs before implementation; second test should PASS or continue guarding 400 behavior.

### Task 3: Candidate Selection and Retry Implementation

**Files:**
- Modify: `internal/service/channel_service.go`
- Modify: `internal/service/relay_service.go`

- [x] **Step 1: Add ordered candidate selection**

In `ChannelService`, add `SelectChannelCandidatesWithRoute(modelName string, route RoutePreference) ([]model.Channel, error)` that applies the same DB filter, model filter, and route filter as `SelectChannelWithRoute`, but returns all candidates in existing order. Keep `SelectChannelWithRoute` choosing the first weighted group as before.

- [x] **Step 2: Add retry count helper**

In `RelayService`, add:

```go
func (s *RelayService) relayRetryCount() int {
	if s.settingService == nil {
		return 0
	}
	value, err := s.settingService.GetInt("relay.retry_count")
	if err != nil || value < 0 {
		return 0
	}
	return value
}
```

- [x] **Step 3: Refactor one-attempt non-stream relay**

Extract the current upstream attempt body into a helper that accepts one `*model.Channel` and returns response bytes, usage, a retryable flag, and error. Retryable must be true only for network request errors, context deadline exceeded, upstream response read failure, upstream status 429, and upstream status >= 500. It must be false for local conversion errors, upstream 400/401/403, response conversion failures, and billing failures.

- [x] **Step 4: Loop through candidates**

In `Relay`, after quota precheck:

```go
candidates, err := s.channelService.SelectChannelCandidatesWithRoute(reqInfo.Model, reqInfo.Route)
if err != nil {
	_ = s.recordLog(token, nil, reqInfo.Model, nil, common.LogStatusFailed, 0, "no available channel", clientIP)
	return nil, nil, &HTTPError{Status: 502, Message: "no available upstream channel", Type: "upstream_error", Code: "no_available_channel"}
}
maxAttempts := 1 + s.relayRetryCount()
if maxAttempts > len(candidates) {
	maxAttempts = len(candidates)
}
```

Attempt candidates in order until one succeeds, a non-retryable error happens, or `maxAttempts` is reached. Return the last error if all retryable attempts fail.

- [x] **Step 5: Run focused tests**

Run: `go test ./internal/router -run "TestChatCompletionRetriesRetryableUpstreamAndDeductsOnce|TestChatCompletionDoesNotRetryNonRetryableUpstreamStatus|TestChatCompletionUpstreamErrorStatusMapping|TestChatCompletionSuccessLogsAndDeductsQuota" -count=1`

Expected: PASS.

### Task 4: Documentation and Checkpoint

**Files:**
- Modify: `docs/RELAY.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/ERRORS.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Update docs**

Record that non-stream retry is implemented for retryable upstream failures when `relay.retry_count > 0`; 400/401/403 and all stream requests are not retried. Keep circuit breaker, rate limit, and half-open recovery as remaining P1 reliability work.

- [x] **Step 2: Run verification**

Run:

```powershell
gofmt -w internal\service\channel_service.go internal\service\relay_service.go internal\router\router_test.go
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
git diff --check
go test ./... -count=1
```

Expected: YAML parses, diff check exits 0, and all Go tests pass.

- [x] **Step 3: Commit**

Run:

```powershell
git add internal\service\channel_service.go internal\service\relay_service.go internal\router\router_test.go docs\RELAY.md docs\ROADMAP.md docs\TESTING.md docs\TRACEABILITY.md docs\ERRORS.md docs\apifox\openapi.yaml docs\superpowers\plans\2026-06-14-non-stream-retry.md
git commit -m "feat: retry non-stream relay failures"
```
