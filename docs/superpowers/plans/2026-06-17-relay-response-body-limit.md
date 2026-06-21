# Relay Response Body Limit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the documented `relay.max_response_body_bytes` setting so oversized non-stream upstream responses fail safely instead of being silently truncated or forwarded.

**Architecture:** Keep the limit inside `RelayService` because upstream response bodies are read there for all non-stream JSON, multipart-result, and raw binary relay calls. Add the setting to setup defaults and validation, expose a typed accessor, and replace the hardcoded `io.LimitReader(resp.Body, 20<<20)` with a helper that reads at most the configured limit plus a sentinel byte and returns an explicit `upstream_response_too_large` error.

**Tech Stack:** Go, Gin router integration tests, RouterX settings service, OpenAPI/Apifox docs.

---

### Task 1: Configurable Upstream Response Body Limit

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `internal/service/relay_service.go`
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`
- Modify: `docs/API.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/SETTINGS.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Write the failing tests**

Add `relay.max_response_body_bytes` to `TestSetupBootstrapAdminQuotaAndSettingsDefaults`.

Extend `TestSettingsValidationAndReadiness` with:

```go
badRelayResponseBodyLimit := performJSON(r, http.MethodPut, "/v0/admin/setting", rootJWT, map[string]interface{}{
    "relay.max_response_body_bytes": "-1",
})
if badRelayResponseBodyLimit.Code != http.StatusBadRequest {
    t.Fatalf("relay max response body bytes should reject negative values, got %d %s", badRelayResponseBodyLimit.Code, badRelayResponseBodyLimit.Body.String())
}
```

Add `TestRelayMaxResponseBodyBytesRejectsOversizedUpstream` near the Chat upstream error tests. The test should:

1. Initialize setup and create an API key.
2. Set `relay.max_response_body_bytes` to a small positive value.
3. Create an enabled OpenAI-compatible channel.
4. Have the upstream return HTTP 200 with a response body larger than the configured limit.
5. Assert HTTP 502, OpenAI-compatible code `upstream_response_too_large`, one upstream call, no user/API Key deduction, a failed log with zero quota, and no leaked oversized body content.

- [x] **Step 2: Run tests to verify they fail**

Run:

```powershell
go test ./internal/router -run "TestRelayMaxResponseBodyBytesRejectsOversizedUpstream|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
```

Expected: FAIL because the setting is not initialized or validated yet, and the relay currently uses a hardcoded response reader that does not return `upstream_response_too_large`.

- [x] **Step 3: Implement minimal backend changes**

Add `relay.max_response_body_bytes` default `10485760` in `SetupService.buildDefaultSettings`.

Validate `relay.max_response_body_bytes` as a non-negative integer in `SettingService`.

Add a RelayService accessor:

```go
func (s *RelayService) MaxResponseBodyBytes() int64 {
    // 0 disables the limit; invalid or missing values fall back to the configured 20MiB default.
}
```

Add a response-body helper in `RelayService`:

```go
var errRelayResponseBodyTooLarge = errors.New("relay upstream response body too large")

func (s *RelayService) readUpstreamResponseBody(body io.Reader) ([]byte, error) {
    limit := s.MaxResponseBodyBytes()
    if limit <= 0 {
        return io.ReadAll(body)
    }
    payload, err := io.ReadAll(io.LimitReader(body, limit+1))
    if err != nil {
        return nil, err
    }
    if int64(len(payload)) > limit {
        return nil, errRelayResponseBodyTooLarge
    }
    return payload, nil
}
```

Replace the hardcoded `io.ReadAll(io.LimitReader(resp.Body, 20<<20))` in `relayNonStreamAttempt`. If the helper returns `errRelayResponseBodyTooLarge`, record a failed log, mark the channel failure, and return:

```go
&HTTPError{
    Status:  http.StatusBadGateway,
    Message: "upstream response body too large",
    Type:    "upstream_error",
    Code:    "upstream_response_too_large",
}
```

- [x] **Step 4: Update docs and Apifox**

Update docs to mark `relay.max_response_body_bytes` as implemented, document 502 `upstream_response_too_large`, and note that raw binary responses such as Audio Speech are protected by the same non-stream limit.

- [x] **Step 5: Run verification**

Run:

```powershell
go test ./internal/router -run "TestRelayMaxResponseBodyBytesRejectsOversizedUpstream|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
go test ./... -count=1
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
git diff --check
```

- [ ] **Step 6: Commit**

Run:

```powershell
git add internal/router/router_test.go internal/service/relay_service.go internal/service/setup_service.go internal/service/setting_service.go docs/API.md docs/RELAY.md docs/SETTINGS.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-relay-response-body-limit.md
git commit -m "feat: enforce relay response body limit"
```
