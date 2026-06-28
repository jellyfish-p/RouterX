# Relay Request Body Limit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the documented `relay.max_request_body_bytes` setting so oversized model API requests fail locally with a clear 413 error before any upstream call.

**Architecture:** Keep request-size enforcement in the HTTP relay handler because every OpenAI, Anthropic, and Gemini entrypoint reads the body there before service routing. Add the setting to setup defaults and setting validation, expose a small RelayService accessor for the hot setting, and use `http.MaxBytesReader` so Gin returns an explicit too-large read error instead of silently truncating the body.

**Tech Stack:** Go, Gin router integration tests, RouterX settings service, OpenAPI/Apifox docs.

---

### Task 1: Configurable Relay Request Body Limit

**Files:**
- Modify: `internal/router/router_test.go`
- Modify: `internal/handler/relay_handler.go`
- Modify: `internal/service/relay_service.go`
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`
- Modify: `docs/API.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/SETTINGS.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/TRACEABILITY.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Write the failing test**

Add `TestRelayMaxRequestBodyBytesRejectsBeforeUpstream` to `internal/router/router_test.go`. The test should:

1. Initialize setup and create an API key.
2. Set `relay.max_request_body_bytes` to a small positive value.
3. Create an enabled OpenAI-compatible channel.
4. Send a `/v1/chat/completions` request larger than the configured limit.
5. Assert HTTP 413, OpenAI-compatible error code `request_body_too_large`, and zero upstream calls.

Also add `relay.max_request_body_bytes` to the existing setup default settings assertion.

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/router -run "TestRelayMaxRequestBodyBytesRejectsBeforeUpstream|TestSetupBootstrapAdminQuotaAndSettingsDefaults" -count=1
```

Expected: FAIL because the setting is not initialized and the handler currently reads up to a hardcoded 20MiB without returning 413 for a small configured limit.

- [x] **Step 3: Implement the minimal code**

Add `relay.max_request_body_bytes` default `10485760` in `SetupService.buildDefaultSettings`, validate it as a non-negative int in `SettingService`, and add a RelayService accessor:

```go
func (s *RelayService) MaxRequestBodyBytes() int64 {
    // 0 disables the limit; invalid values fall back to 20MiB.
}
```

Change relay handlers to call a method that wraps `c.Request.Body` with `http.MaxBytesReader` and maps `*http.MaxBytesError` to 413 `request_body_too_large`.

- [x] **Step 4: Update docs and Apifox**

Update settings, relay, API, testing, traceability, and Apifox docs to describe `relay.max_request_body_bytes`, 413 errors, and the local pre-upstream rejection behavior.

- [x] **Step 5: Run verification**

Run:

```powershell
go test ./internal/router -run "TestRelayMaxRequestBodyBytesRejectsBeforeUpstream|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
go test ./... -count=1
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
git diff --check
```

- [ ] **Step 6: Commit**

Run:

```powershell
git add internal/router/router_test.go internal/handler/relay_handler.go internal/service/relay_service.go internal/service/setup_service.go internal/service/setting_service.go docs/API.md docs/RELAY.md docs/SETTINGS.md docs/TESTING.md docs/TRACEABILITY.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-relay-request-body-limit.md
git commit -m "feat: enforce relay request body limit"
```
