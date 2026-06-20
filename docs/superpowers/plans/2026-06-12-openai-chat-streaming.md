# OpenAI Chat Streaming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement OpenAI-compatible `POST /v1/chat/completions` `stream=true` forwarding with SSE output, usage-based quota deduction, and call logging.

**Architecture:** Keep this slice limited to OpenAI-compatible ingress and OpenAI-compatible upstreams. `RelayHandler` should detect `stream=true`, ask `RelayService` to prepare a successful upstream stream before writing headers, then forward SSE lines while `RelayService` extracts the final OpenAI `usage` object and records billing facts after the stream ends. Non-stream behavior stays on the existing `Relay` path.

**Tech Stack:** Go, Gin, `net/http/httptest`, GORM SQLite test DB, existing `relay.Adapter` and `TokenService`/`LogService`.

---

### Task 1: Streaming Regression Test

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Write the failing test**

Add this test near `TestChatCompletionSuccessLogsAndDeductsQuota`:

```go
func TestChatCompletionStreamForwardsSSEAndDeductsUsage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key")

	upstreamCalls := 0
	upstreamAuth := ""
	var upstreamBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls++
		upstreamAuth = req.Header.Get("Authorization")
		if err := json.NewDecoder(req.Body).Decode(&upstreamBody); err != nil {
			t.Errorf("upstream received invalid JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n"))
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
	if err := internal.DB.Model(&model.User{}).Where("username = ?", "root").Update("quota", int64(100)).Error; err != nil {
		t.Fatal(err)
	}
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "stream",
		"remain_quota": 50,
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
		"name":     "stream",
		"models":   "gpt-test",
		"base_url": upstream.URL,
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	chatResp := performJSON(r, http.MethodPost, "/v1/chat/completions", "Bearer "+tokenPayload.Data.Key, map[string]interface{}{
		"model": "gpt-test",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"stream": true,
		"routerx": map[string]interface{}{
			"route": map[string]string{"channel": "stream"},
		},
	})
	if chatResp.Code != http.StatusOK {
		t.Fatalf("stream chat should succeed, got %d %s", chatResp.Code, chatResp.Body.String())
	}
	if ct := chatResp.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("stream response should be SSE, got content-type %q", ct)
	}
	body := chatResp.Body.String()
	if !strings.Contains(body, "chat.completion.chunk") || !strings.Contains(body, "data: [DONE]") || strings.Contains(body, `"success"`) {
		t.Fatalf("stream body should forward OpenAI SSE chunks without RouterX wrapper: %s", body)
	}
	if upstreamCalls != 1 || upstreamAuth != "Bearer upstream-secret" {
		t.Fatalf("stream should call upstream once with channel secret, calls=%d auth=%q", upstreamCalls, upstreamAuth)
	}
	if upstreamBody["stream"] != true || upstreamBody["model"] != "gpt-test" {
		t.Fatalf("stream request should preserve stream=true and model, got %#v", upstreamBody)
	}
	if _, ok := upstreamBody["routerx"]; ok {
		t.Fatalf("routerx private field leaked to upstream: %#v", upstreamBody)
	}
	var storedToken model.Token
	if err := internal.DB.First(&storedToken, tokenPayload.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedToken.RemainQuota != 43 {
		t.Fatalf("stream usage should deduct token budget by 7, got %d", storedToken.RemainQuota)
	}
	var root model.User
	if err := internal.DB.Where("username = ?", "root").First(&root).Error; err != nil {
		t.Fatal(err)
	}
	if root.Quota != 93 {
		t.Fatalf("stream usage should deduct user quota by 7, got %d", root.Quota)
	}
	var callLog model.Log
	if err := internal.DB.First(&callLog).Error; err != nil {
		t.Fatal(err)
	}
	if callLog.Status != common.LogStatusSuccess || callLog.QuotaUsed != 7 || callLog.TotalTokens != 7 || callLog.PromptTokens != 3 || callLog.CompletionTokens != 4 {
		t.Fatalf("unexpected stream success log: %+v", callLog)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/router -run TestChatCompletionStreamForwardsSSEAndDeductsUsage -count=1`

Expected: FAIL with `unsupported_stream` because `RelayService.Relay` currently rejects `stream=true`.

### Task 2: Stream Relay Service

**Files:**
- Modify: `internal/service/relay_service.go`
- Modify: `internal/relay/openai.go` if a small SSE usage helper is clearer there

- [x] **Step 1: Add the service streaming result type**

Add a small result type near `RelayService`:

```go
type RelayStreamResult struct {
	ContentType string
	forward     func(write func([]byte) error, flush func()) (*relay.Usage, error)
}

func (r *RelayStreamResult) Forward(write func([]byte) error, flush func()) (*relay.Usage, error) {
	if r == nil || r.forward == nil {
		return nil, errors.New("stream result is not initialized")
	}
	return r.forward(write, flush)
}
```

- [x] **Step 2: Implement `RelayStream` by reusing the existing precheck and upstream path**

Add:

```go
func (s *RelayService) RelayStream(ctx context.Context, token *model.Token, apiType relay.APIType, body []byte, clientIP string) (*RelayStreamResult, error) {
	reqInfo, err := parseRelayRequest(apiType, body)
	if err != nil {
		return nil, &HTTPError{Status: 400, Message: err.Error(), Type: "invalid_request_error", Code: relayRequestErrorCode(err)}
	}
	if !reqInfo.Stream {
		return nil, &HTTPError{Status: 400, Message: "stream is required", Type: "invalid_request_error", Code: "stream_required"}
	}
	// Then mirror Relay(): quota precheck, channel selection, adapter lookup, upstream resolution,
	// model rewrite, ConvertRequest, DoRequest, upstream status handling.
	// On successful 2xx, return RelayStreamResult whose Forward method reads resp.Body line by line,
	// writes each line plus '\n', flushes after blank lines, extracts any OpenAI usage object from data lines,
	// deducts quota, marks channel success, and records a success log.
}
```

The first implementation may duplicate the small upstream preparation block from `Relay`; keep the duplicated block mechanically identical so a later refactor can extract it safely.

- [x] **Step 3: Add SSE usage extraction**

Parse only OpenAI SSE lines:

```go
func usageFromOpenAIStreamLine(line []byte) *relay.Usage {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
		return nil
	}
	var envelope struct {
		Usage *relay.Usage `json:"usage"`
	}
	_ = json.Unmarshal(payload, &envelope)
	return envelope.Usage
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/router -run TestChatCompletionStreamForwardsSSEAndDeductsUsage -count=1`

Expected: PASS.

### Task 3: Handler Streaming Branch

**Files:**
- Modify: `internal/handler/relay_handler.go`

- [x] **Step 1: Detect stream requests before calling non-stream relay**

Add a local helper:

```go
func requestWantsStream(body []byte) bool {
	var payload struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &payload)
	return payload.Stream
}
```

- [x] **Step 2: Wire `relayOpenAI` to streaming service**

In `relayOpenAI`, after reading the body:

```go
if apiType == relay.APIChatCompletions && requestWantsStream(body) {
	result, err := h.svc.RelayStream(c.Request.Context(), token, apiType, body, c.ClientIP())
	if err != nil {
		writeRelayError(c, err)
		return
	}
	c.Header("Content-Type", result.ContentType)
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Status(http.StatusOK)
	_, err = result.Forward(func(chunk []byte) error {
		_, writeErr := c.Writer.Write(chunk)
		return writeErr
	}, func() {
		c.Writer.Flush()
	})
	if err != nil {
		return
	}
	return
}
```

- [x] **Step 3: Run focused regression tests**

Run: `go test ./internal/router -run "TestChatCompletionStreamForwardsSSEAndDeductsUsage|TestChatCompletionInvalidRequestDoesNotCallUpstream|TestChatCompletionSuccessLogsAndDeductsQuota" -count=1`

Expected: PASS for all three tests.

### Task 4: Documentation and Checkpoint

**Files:**
- Modify: `docs/API.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Update docs**

Record that OpenAI-compatible Chat streaming has a first implementation, with remaining gaps for Anthropic/Gemini stream conversion, client disconnect assertions, and stream usage fallbacks.

- [x] **Step 2: Run verification**

Run:

```powershell
gofmt -w internal\handler\relay_handler.go internal\service\relay_service.go internal\router\router_test.go
git diff --check
go test ./...
```

Expected: `git diff --check` exits 0 and `go test ./...` exits 0.

- [x] **Step 3: Commit**

Run:

```powershell
git add internal\handler\relay_handler.go internal\service\relay_service.go internal\router\router_test.go docs\API.md docs\RELAY.md docs\TESTING.md docs\ROADMAP.md docs\apifox\openapi.yaml docs\superpowers\plans\2026-06-12-openai-chat-streaming.md
git commit -m "feat: stream openai chat completions"
```
