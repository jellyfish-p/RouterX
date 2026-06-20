# Relay Retry Status Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `relay.retry_on_status` a real hot setting that controls which upstream HTTP status codes can trigger non-stream retry.

**Architecture:** Keep retry counting in `RelayService.relayRetryCount`, add a small status whitelist parser beside it, and use that whitelist at the single upstream non-2xx decision point. Settings bootstrap and validation own the default value and input safety; docs and Apifox describe the same runtime behavior.

**Tech Stack:** Go, Gin router integration tests, GORM settings table, YAML OpenAPI for Apifox.

---

### Task 1: Red Tests

**Files:**
- Modify: `internal/router/router_test.go`

- [ ] **Step 1: Add bootstrap/default coverage**

Add `relay.retry_on_status` to `TestSetupBootstrapAdminQuotaAndSettingsDefaults` so setup must seed the setting.

- [ ] **Step 2: Add validation coverage**

In the settings validation test, set `relay.retry_on_status` to invalid values such as `[200]` and assert HTTP 400. Status codes below 400 must be rejected because retrying success/local request classes is unsafe.

- [ ] **Step 3: Add retry behavior coverage**

Add `TestChatCompletionUsesConfiguredRetryStatuses`: configure `relay.retry_count=1` and `relay.retry_on_status=[400]`, make the primary upstream return 400, and assert the backup channel succeeds with a single final deduction and a failed attempt log carrying `upstream_400`.

- [ ] **Step 4: Verify red**

Run:

```powershell
go test ./internal/router -run "TestChatCompletionUsesConfiguredRetryStatuses|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
```

Expected: failure because `relay.retry_on_status` is not seeded/validated/read and 400 is still not retryable.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`
- Modify: `internal/service/relay_service.go`

- [ ] **Step 1: Seed the default**

Add:

```go
"relay.retry_on_status": "[429,500,502,503,504]",
```

under the relay default settings.

- [ ] **Step 2: Validate JSON integer arrays**

Add `relay.retry_on_status` to `validateSettingValue` and implement a validator that requires a non-empty JSON integer array with unique values in the HTTP error range `400..599`.

- [ ] **Step 3: Read the setting in RelayService**

Replace the package-level hard-coded retry check with a RelayService method that parses `relay.retry_on_status`, falls back to `[429,500,502,503,504]` on missing/invalid config, and checks membership.

- [ ] **Step 4: Verify green**

Run:

```powershell
go test ./internal/router -run "TestChatCompletionUsesConfiguredRetryStatuses|TestChatCompletionRetriesRetryableUpstreamAndDeductsOnce|TestChatCompletionDoesNotRetryNonRetryableUpstreamStatus|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestSettingsValidationAndReadiness" -count=1
```

Expected: PASS.

### Task 3: Documentation And Apifox

**Files:**
- Modify: `docs/SETTINGS.md`
- Modify: `docs/RELAY.md`
- Modify: `docs/IMPLEMENTATION.md`
- Modify: `docs/ERRORS.md`
- Modify: `docs/DEVELOPER_EXPERIENCE.md`
- Modify: `docs/TESTING.md`
- Modify: `docs/apifox/openapi.yaml`

- [ ] **Step 1: Move setting from target-only to current registry**

Document `relay.retry_on_status` as hot, JSON integer array, default `[429,500,502,503,504]`, and current implementation.

- [ ] **Step 2: Update retry wording**

Replace fixed “429/5xx” language with “configured by `relay.retry_on_status`, defaulting to 429/500/502/503/504” while keeping network/timeout/read failures retryable under `relay.retry_count`.

- [ ] **Step 3: Update Apifox OpenAPI descriptions**

Mention the setting in the `/v1/chat/completions` description and settings schema description so Apifox imports match the backend.

### Task 4: Verification And Git Archive

**Files:**
- All modified files.

- [ ] **Step 1: Run full backend tests**

```powershell
go test ./... -count=1
```

- [ ] **Step 2: Validate Apifox YAML**

```powershell
python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('docs/apifox/openapi.yaml').read_text(encoding='utf-8')); print('yaml ok')"
```

- [ ] **Step 3: Check whitespace**

```powershell
git diff --check
```

- [ ] **Step 4: Commit**

```powershell
git add internal/router/router_test.go internal/service/setup_service.go internal/service/setting_service.go internal/service/relay_service.go docs/SETTINGS.md docs/RELAY.md docs/IMPLEMENTATION.md docs/ERRORS.md docs/DEVELOPER_EXPERIENCE.md docs/TESTING.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-17-relay-retry-on-status.md
git commit -m "feat: configure retry statuses"
```
