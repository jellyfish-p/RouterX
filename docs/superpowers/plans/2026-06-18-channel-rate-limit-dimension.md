# Channel Rate Limit Dimension Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Redis-backed `rate_limit.per_channel_per_min` setting so a saturated upstream channel can be throttled before RouterX sends another request to it.

**Architecture:** Keep request-known dimensions in middleware and keep model/channel dimensions inside `RelayService`, where parsed model and selected channel are available. The channel check runs immediately before a concrete upstream attempt, uses the existing fixed-minute Redis counter semantics, and records a policy denial snapshot when the selected channel is over limit.

**Tech Stack:** Go, Gin router tests, RelayService, Redis fake server, settings defaults/validation, Markdown docs, Apifox OpenAPI YAML.

---

### Task 1: Red Test

**Files:**
- Modify: `internal/router/router_test.go`

- [x] **Step 1: Add channel-level rate limit coverage**

Add `TestRateLimitPerChannelRejectsBeforeUpstream` near the existing rate limit tests. It should initialize RouterX, attach fake Redis, disable global/IP/Token/User/Model limits, set `rate_limit.per_channel_per_min=1`, create one OpenAI-compatible channel, then send two `/v1/chat/completions` requests. The first request should succeed; the second should return OpenAI-compatible 429 `rate_limit_exceeded`, not call upstream a second time, not deduct additional quota, and write a failed log with `channel_id` and `scope_result.rate_limit_dimension=channel`.

- [x] **Step 2: Verify red**

```powershell
go test ./internal/router -run "TestRateLimitPerChannelRejectsBeforeUpstream" -count=1
```

Expected: FAIL because no channel-level setting is enforced yet.

### Task 2: Backend Implementation

**Files:**
- Modify: `internal/service/relay_service.go`
- Modify: `internal/service/setup_service.go`
- Modify: `internal/service/setting_service.go`

- [x] **Step 1: Add channel rate limit helper**

Add a RelayService helper with this behavior:

```go
func (s *RelayService) enforceChannelRateLimit(ctx context.Context, token *model.Token, channel *model.Channel, modelName, clientIP string) error
```

It should skip when Redis is unavailable, the channel is nil, the setting service is unavailable, `rate_limit.enabled=false`, or `rate_limit.per_channel_per_min<=0`. When the fixed-minute key `rl:channel:{channel_id}:{minute}` exceeds the limit, it records a failed log with a policy snapshot containing:

```json
{
  "api_type": "allow",
  "model": "allow",
  "channel_group": "allow",
  "rate_limit": "deny",
  "rate_limit_dimension": "channel"
}
```

and returns `HTTPError{Status: 429, Message: "rate limit exceeded", Type: "rate_limit_error", Code: "rate_limit_exceeded"}`.

- [x] **Step 2: Call helper before upstream attempts**

Call `enforceChannelRateLimit` in `relayNonStreamAttempt` after a channel is selected and before adapter/upstream work begins. Call it in `RelayStream` after the route snapshot is attached and before stream support/upstream setup checks.

- [x] **Step 3: Register default and validation**

Add default:

```go
"rate_limit.per_channel_per_min": "0",
```

Validate it with other non-negative `rate_limit.*` settings. `0` disables the channel dimension.

- [x] **Step 4: Verify green**

```powershell
go test ./internal/router -run "TestRateLimitPerChannelRejectsBeforeUpstream" -count=1
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

Document `rate_limit.per_channel_per_min`, default `0`, `>=0`, hot setting, and `0` means disabled.

- [x] **Step 2: Update coverage statements**

Update reliability/rate-limit coverage language from global/IP/Token/User/Model to global/IP/Token/User/Model/Channel where appropriate.

- [x] **Step 3: Update Apifox**

Add `rate_limit.per_channel_per_min` to settings descriptions and examples so Apifox imports the key meaning.

### Task 4: Verification And Git Archive

**Files:**
- All modified files.

- [x] **Step 1: Run targeted tests**

```powershell
go test ./internal/router -run "TestRateLimitPerChannelRejectsBeforeUpstream|TestRateLimitPerModelRejectsBeforeUpstream|TestRateLimitPerUserAppliesAcrossAPIKeys|TestRateLimitUsesSettingsAndEntryProtocolErrorShape|TestSettingsValidationAndReadiness|TestSetupBootstrapAdminQuotaAndSettingsDefaults|TestMetricsEndpointIncludesRelayPaymentAndInfrastructureSignals" -count=1
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
git add internal/router/router_test.go internal/service/relay_service.go internal/service/setup_service.go internal/service/setting_service.go docs/SETTINGS.md docs/POLICIES.md docs/RELAY.md docs/OPERATIONS.md docs/TESTING.md docs/TRACEABILITY.md docs/ROADMAP.md docs/API.md docs/DATA_MODEL.md docs/OBSERVABILITY.md docs/SNAPSHOTS.md docs/apifox/openapi.yaml docs/superpowers/plans/2026-06-18-channel-rate-limit-dimension.md
git commit -m "feat: add channel rate limit dimension"
```
