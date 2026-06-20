# Model Rate Limit Dimension Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Redis-backed `rate_limit.per_model_per_min` setting so a single hot model can be throttled before any upstream call.

**Architecture:** Keep global/IP/Token/User rate limits in the Gin middleware, because those dimensions are known before the request body is parsed. Enforce model rate limit inside `RelayService` after `reqInfo.Model` is parsed and before candidate selection or upstream calls. Use the existing fixed-minute Redis counter semantics and policy snapshot logging.

**Tech Stack:** Go, Gin router tests, RelayService, Redis fake server, settings defaults/validation, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Red Test

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add model-level rate limit coverage**

Add `TestRateLimitPerModelRejectsBeforeUpstream` near the existing rate limit tests. It should initialize RouterX, attach fake Redis, set `rate_limit.global_per_min=0`, `rate_limit.per_ip_per_min=0`, `rate_limit.per_token_per_min=0`, `rate_limit.per_user_per_min=0`, and `rate_limit.per_model_per_min=1`, then send two `/v1/chat/completions` requests for the same model. The first request should succeed; the second should return OpenAI-compatible 429 `rate_limit_exceeded`, not call upstream, not deduct additional quota, and write a failed log with `scope_result.rate_limit_dimension=model`.

- [x] **Step 2: Verify red**

Run:

```powershell
go test ./internal/router -run "TestRateLimitPerModelRejectsBeforeUpstream" -count=1
```

Expected: FAIL because the model setting is not enforced yet.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/relay_service.go`
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`

- [x] **Step 1: Add model rate limit helper**

Add a RelayService helper that:
- Returns immediately when Redis is unavailable, settings are unavailable, the model is empty, or `rate_limit.enabled=false`.
- Reads `rate_limit.per_model_per_min`.
- Uses key `rl:model:{model}:{minute}`.
- Records a failed policy log with `reject_code=rate_limit_exceeded`, `quota_precheck=rate_limit_exceeded`, `scope_result.rate_limit=deny`, and `scope_result.rate_limit_dimension=model`.
- Returns `HTTPError{Status: 429, Message: "rate limit exceeded", Type: "rate_limit_error", Code: "rate_limit_exceeded"}`.

- [x] **Step 2: Call helper before routing**

Call the helper after token scope and balance precheck, before `buildRelayPolicySnapshot` and channel candidate selection in both non-stream and stream Relay paths.

- [x] **Step 3: Register default and validation**

Add default:

```go
"rate_limit.per_model_per_min": "0",
```

Validate it with other non-negative `rate_limit.*` settings. `0` disables the model dimension.

- [x] **Step 4: Verify green**

Run:

```powershell
go test ./internal/router -run "TestRateLimitPerModelRejectsBeforeUpstream" -count=1
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
- Modify: `docs/API.md`
- Modify: `docs/DATA_MODEL.md`
- Modify: `docs/OBSERVABILITY.md`
- Modify: `docs/SNAPSHOTS.md`
- Modify: `docs/apifox/openapi.yaml`

- [x] **Step 1: Document the setting**

Document `rate_limit.per_model_per_min`, default `0`, `>=0`, hot setting, and `0` means disabled.

- [x] **Step 2: Update coverage statements**

Update reliability/rate-limit coverage language from global/IP/Token/User to global/IP/Token/User/Model where appropriate, while leaving channel dimension as future work.

- [x] **Step 3: Update Apifox**

Add `rate_limit.per_model_per_min` to settings descriptions and examples so Apifox imports the key meaning.

### Task 4: Verification And Git Archive

**Files:**
- All modified files.

- [x] **Step 1: Run targeted tests**

```powershell
go test ./internal/router -run "TestRateLimitPerModelRejectsBeforeUpstream|TestRateLimitPerUserAppliesAcrossAPIKeys|TestRateLimitUsesSettingsAndEntryProtocolErrorShape|TestSettingsValidationAndReadiness|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestMetricsEndpointIncludesRelayPaymentAndInfrastructureSignals" -count=1
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
git add internal/router/router_test.go internal/service/relay_service.go internal/service/setup_service.go internal/service/setting_service.go docs/SETTINGS.md docs/POLICIES.md docs/RELAY.md docs/OPERATIONS.md docs/TESTING.md docs/TRACEABILITY.md docs/ROADMAP.md docs/API.md docs/DATA_MODEL.md docs/OBSERVABILITY.md docs/SNAPSHOTS.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-model-rate-limit-dimension.md
git commit -m "feat: add model rate limit dimension"
```
