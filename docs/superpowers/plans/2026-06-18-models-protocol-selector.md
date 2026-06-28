# Models Protocol Selector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `routerx_protocol` query and `X-RouterX-Protocol` header support to `GET /v1/models` while preserving existing `format` and Anthropic header behavior.

**Architecture:** Keep model-list rendering in `RelayHandler.listModelsForRequest`. Extract a small format resolver that normalizes aliases and enforces format precedence: `format` wins, then `routerx_protocol`, then `X-RouterX-Protocol`, then `anthropic-version`, then OpenAI default. Reuse the existing `RelayService.ListModels`, `ListGeminiModels`, and `ListAnthropicModels` response builders.

**Tech Stack:** Go, Gin router integration tests, existing API Key auth middleware, Apifox OpenAPI YAML.

---

### Task 1: Red Tests

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add protocol selector coverage**

Add `TestModelListSupportsRouterXProtocolSelector` near the existing `/v1/models` tests:

```go
func TestModelListSupportsRouterXProtocolSelector(t *testing.T) {
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
	tokenResp := performJSON(r, http.MethodPost, "/v0/user/token", rootJWT, map[string]interface{}{
		"name":         "models-protocol",
		"quota_limit": 10,
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
		"name":     "models-protocol-channel",
		"models":   "gpt-protocol",
		"base_url": "http://127.0.0.1",
		"api_key":  "upstream-secret",
	})
	if channelResp.Code != http.StatusOK {
		t.Fatalf("create channel failed: %d %s", channelResp.Code, channelResp.Body.String())
	}

	geminiResp := performJSON(r, http.MethodGet, "/v1/models?routerx_protocol=gemini", "Bearer "+tokenPayload.Data.Key, nil)
	if geminiResp.Code != http.StatusOK || !strings.Contains(geminiResp.Body.String(), `"models"`) || !strings.Contains(geminiResp.Body.String(), `"name":"models/gpt-protocol"`) {
		t.Fatalf("routerx_protocol=gemini should return Gemini model shape, got %d %s", geminiResp.Code, geminiResp.Body.String())
	}
	anthropicResp := performRawWithHeaders(r, http.MethodGet, "/v1/models", "Bearer "+tokenPayload.Data.Key, "", map[string]string{
		"X-RouterX-Protocol": "anthropic",
	})
	if anthropicResp.Code != http.StatusOK || !strings.Contains(anthropicResp.Body.String(), `"has_more":false`) || !strings.Contains(anthropicResp.Body.String(), `"type":"model"`) {
		t.Fatalf("X-RouterX-Protocol=anthropic should return Anthropic model shape, got %d %s", anthropicResp.Code, anthropicResp.Body.String())
	}
	precedenceResp := performRawWithHeaders(r, http.MethodGet, "/v1/models?format=gemini&routerx_protocol=anthropic", "Bearer "+tokenPayload.Data.Key, "", map[string]string{
		"X-RouterX-Protocol": "openai",
	})
	if precedenceResp.Code != http.StatusOK || !strings.Contains(precedenceResp.Body.String(), `"name":"models/gpt-protocol"`) {
		t.Fatalf("format should keep precedence over routerx protocol selectors, got %d %s", precedenceResp.Code, precedenceResp.Body.String())
	}
	openAIResp := performJSON(r, http.MethodGet, "/v1/models?routerx_protocol=openai", "Bearer "+tokenPayload.Data.Key, nil)
	if openAIResp.Code != http.StatusOK || !strings.Contains(openAIResp.Body.String(), `"object":"list"`) || !strings.Contains(openAIResp.Body.String(), `"id":"gpt-protocol"`) {
		t.Fatalf("routerx_protocol=openai should return OpenAI model shape, got %d %s", openAIResp.Code, openAIResp.Body.String())
	}
}
```

- [x] **Step 2: Verify red**

Run:

```powershell
go test ./internal/router -run "TestModelListSupportsRouterXProtocolSelector" -count=1
```

Expected: failure because `routerx_protocol=gemini` and `X-RouterX-Protocol=anthropic` are ignored.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/handler/relay_handler.go`
- Modify: `internal/middleware/apikey_auth.go`

- [x] **Step 1: Add model list protocol resolver**

Implement in `relay_handler.go`:

```go
func modelListProtocol(c *gin.Context) string {
	format := normalizeModelListProtocol(c.Query("format"))
	if format != "" {
		return format
	}
	queryProtocol := normalizeModelListProtocol(c.Query("routerx_protocol"))
	if queryProtocol != "" {
		return queryProtocol
	}
	headerProtocol := normalizeModelListProtocol(c.GetHeader("X-RouterX-Protocol"))
	if headerProtocol != "" {
		return headerProtocol
	}
	if strings.TrimSpace(c.GetHeader("anthropic-version")) != "" {
		return "anthropic"
	}
	return "openai"
}

func normalizeModelListProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "google", "gemini":
		return "gemini"
	case "claude", "anthropic":
		return "anthropic"
	case "openai", "openai-compatible", "openai_compatible":
		return "openai"
	default:
		return ""
	}
}
```

Change `listModelsForRequest` to switch on `modelListProtocol(c)`.

- [x] **Step 2: Align auth error protocol detection**

Update `entryProtocol` in `internal/middleware/apikey_auth.go` so invalid/forbidden API Key errors on `/v1/models?routerx_protocol=gemini` or `X-RouterX-Protocol: anthropic` return the requested protocol error shape:

```go
routerxProtocol := normalizeEntryProtocol(c.Query("routerx_protocol"))
headerProtocol := normalizeEntryProtocol(c.GetHeader("X-RouterX-Protocol"))
```

Add a small `normalizeEntryProtocol` helper with the same aliases used by the handler.

- [x] **Step 3: Verify green**

Run:

```powershell
go test ./internal/router -run "TestModelListSupportsRouterXProtocolSelector" -count=1
```

Expected: PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/API.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/PROTOCOLS.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Update docs**

Replace the “target can extend” wording for `/v1/models` protocol conflict resolution with the current behavior: `format`, `routerx_protocol`, `X-RouterX-Protocol`, `anthropic-version`, OpenAI default.

- [x] **Step 2: Update testing and traceability**

Add `TestModelListSupportsRouterXProtocolSelector` to the multi-entry protocol evidence.

- [x] **Step 3: Update Apifox**

Add `routerx_protocol` query parameter and `X-RouterX-Protocol` header parameter to `/v1/models`.

### Task 4: Verification And Git Archive

**Files:**
- All modified files.

- [x] **Step 1: Run full tests**

```powershell
go test ./... -count=1
```

- [x] **Step 2: Validate Apifox YAML**

```powershell
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
```

- [x] **Step 3: Check whitespace**

```powershell
git diff --check
```

- [x] **Step 4: Commit**

```powershell
git add internal/router/router_test.go internal/handler/relay_handler.go internal/middleware/apikey_auth.go docs/API.md docs/RELAY.md docs/PROTOCOLS.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-models-protocol-selector.md
git commit -m "feat: select model list protocol"
```
